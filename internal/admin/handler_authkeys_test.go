package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"gomodel/internal/authkeys"
)

type authKeyTestStore struct {
	keys map[string]authkeys.AuthKey
}

func newAuthKeyTestStore(keys ...authkeys.AuthKey) *authKeyTestStore {
	store := &authKeyTestStore{keys: make(map[string]authkeys.AuthKey, len(keys))}
	for _, key := range keys {
		store.keys[key.ID] = key
	}
	return store
}

func (s *authKeyTestStore) List(_ context.Context) ([]authkeys.AuthKey, error) {
	result := make([]authkeys.AuthKey, 0, len(s.keys))
	for _, key := range s.keys {
		result = append(result, key)
	}
	return result, nil
}

func (s *authKeyTestStore) Create(_ context.Context, key authkeys.AuthKey) error {
	s.keys[key.ID] = key
	return nil
}

func (s *authKeyTestStore) Deactivate(_ context.Context, id string, now time.Time) error {
	key, ok := s.keys[id]
	if !ok {
		return authkeys.ErrNotFound
	}
	key.Enabled = false
	key.UpdatedAt = now.UTC()
	if key.DeactivatedAt == nil {
		deactivatedAt := now.UTC()
		key.DeactivatedAt = &deactivatedAt
	}
	s.keys[id] = key
	return nil
}

func (s *authKeyTestStore) Close() error { return nil }

func newAuthKeyHandler(t *testing.T, store authkeys.Store) *Handler {
	t.Helper()
	service, err := authkeys.NewService(store)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	return NewHandler(nil, nil, WithAuthKeys(service))
}

func TestAuthKeyEndpointsReturn503WhenServiceUnavailable(t *testing.T) {
	h := NewHandler(nil, nil)
	e := echo.New()

	listCtx, listRec := newHandlerContext("/admin/api/v1/auth-keys")
	if err := h.ListAuthKeys(listCtx); err != nil {
		t.Fatalf("ListAuthKeys() error = %v", err)
	}
	if listRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ListAuthKeys() status = %d, want 503", listRec.Code)
	}

	createReq := httptest.NewRequest(http.MethodPost, "/admin/api/v1/auth-keys", bytes.NewBufferString(`{"name":"primary"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	createCtx := e.NewContext(createReq, createRec)
	if err := h.CreateAuthKey(createCtx); err != nil {
		t.Fatalf("CreateAuthKey() error = %v", err)
	}
	if createRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("CreateAuthKey() status = %d, want 503", createRec.Code)
	}
}

func TestCreateListAndDeactivateAuthKey(t *testing.T) {
	h := newAuthKeyHandler(t, newAuthKeyTestStore())
	e := echo.New()

	createReq := httptest.NewRequest(http.MethodPost, "/admin/api/v1/auth-keys", bytes.NewBufferString(`{"name":"primary","description":"prod key"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	createCtx := e.NewContext(createReq, createRec)

	if err := h.CreateAuthKey(createCtx); err != nil {
		t.Fatalf("CreateAuthKey() error = %v", err)
	}
	if createRec.Code != http.StatusCreated {
		t.Fatalf("CreateAuthKey() status = %d, want 201", createRec.Code)
	}

	var issued authkeys.IssuedKey
	if err := json.Unmarshal(createRec.Body.Bytes(), &issued); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	if issued.Value == "" || issued.ID == "" {
		t.Fatalf("issued response = %#v, want id and value", issued)
	}

	listCtx, listRec := newHandlerContext("/admin/api/v1/auth-keys")
	if err := h.ListAuthKeys(listCtx); err != nil {
		t.Fatalf("ListAuthKeys() error = %v", err)
	}
	if listRec.Code != http.StatusOK {
		t.Fatalf("ListAuthKeys() status = %d, want 200", listRec.Code)
	}

	var views []authkeys.View
	if err := json.Unmarshal(listRec.Body.Bytes(), &views); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	if len(views) != 1 || !views[0].Active {
		t.Fatalf("list response = %#v, want one active key", views)
	}

	deactivateReq := httptest.NewRequest(http.MethodPost, "/admin/api/v1/auth-keys/"+issued.ID+"/deactivate", nil)
	deactivateRec := httptest.NewRecorder()
	deactivateCtx := e.NewContext(deactivateReq, deactivateRec)
	deactivateCtx.SetPathValues(echo.PathValues{{Name: "id", Value: issued.ID}})

	if err := h.DeactivateAuthKey(deactivateCtx); err != nil {
		t.Fatalf("DeactivateAuthKey() error = %v", err)
	}
	if deactivateRec.Code != http.StatusNoContent {
		t.Fatalf("DeactivateAuthKey() status = %d, want 204", deactivateRec.Code)
	}

	listCtx, listRec = newHandlerContext("/admin/api/v1/auth-keys")
	if err := h.ListAuthKeys(listCtx); err != nil {
		t.Fatalf("ListAuthKeys() error after deactivate = %v", err)
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &views); err != nil {
		t.Fatalf("unmarshal list response after deactivate: %v", err)
	}
	if len(views) != 1 || views[0].Active {
		t.Fatalf("list response after deactivate = %#v, want one inactive key", views)
	}
}
