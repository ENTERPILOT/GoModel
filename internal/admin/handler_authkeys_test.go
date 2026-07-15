package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/authkeys"
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

func (s *authKeyTestStore) UpdateLabels(_ context.Context, id string, labels []string, now time.Time) error {
	key, ok := s.keys[id]
	if !ok {
		return authkeys.ErrNotFound
	}
	key.Labels = labels
	key.UpdatedAt = now.UTC()
	s.keys[id] = key
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

	listCtx, listRec := newHandlerContext("/admin/auth-keys")
	if err := h.ListAuthKeys(listCtx); err != nil {
		t.Fatalf("ListAuthKeys() error = %v", err)
	}
	if listRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ListAuthKeys() status = %d, want 503", listRec.Code)
	}

	createReq := httptest.NewRequest(http.MethodPost, "/admin/auth-keys", bytes.NewBufferString(`{"name":"primary"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	createCtx := e.NewContext(createReq, createRec)
	if err := h.CreateAuthKey(createCtx); err != nil {
		t.Fatalf("CreateAuthKey() error = %v", err)
	}
	if createRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("CreateAuthKey() status = %d, want 503", createRec.Code)
	}

	deactivateReq := httptest.NewRequest(http.MethodPost, "/admin/auth-keys/test-key/deactivate", nil)
	deactivateRec := httptest.NewRecorder()
	deactivateCtx := e.NewContext(deactivateReq, deactivateRec)
	deactivateCtx.SetPathValues(echo.PathValues{{Name: "id", Value: "test-key"}})
	if err := h.DeactivateAuthKey(deactivateCtx); err != nil {
		t.Fatalf("DeactivateAuthKey() error = %v", err)
	}
	if deactivateRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("DeactivateAuthKey() status = %d, want 503", deactivateRec.Code)
	}

	labelsReq := httptest.NewRequest(http.MethodPut, "/admin/auth-keys/test-key/labels", bytes.NewBufferString(`{"labels":["a"]}`))
	labelsReq.Header.Set("Content-Type", "application/json")
	labelsRec := httptest.NewRecorder()
	labelsCtx := e.NewContext(labelsReq, labelsRec)
	labelsCtx.SetPathValues(echo.PathValues{{Name: "id", Value: "test-key"}})
	if err := h.UpdateAuthKeyLabels(labelsCtx); err != nil {
		t.Fatalf("UpdateAuthKeyLabels() error = %v", err)
	}
	if labelsRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("UpdateAuthKeyLabels() status = %d, want 503", labelsRec.Code)
	}
}

func TestCreateListAndDeactivateAuthKey(t *testing.T) {
	h := newAuthKeyHandler(t, newAuthKeyTestStore())
	e := echo.New()

	createReq := httptest.NewRequest(http.MethodPost, "/admin/auth-keys", bytes.NewBufferString(`{"name":"primary","description":"prod key","user_path":" team//alpha/service/ ","labels":[" team-a ","batch","team-a"]}`))
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
	if issued.UserPath != "/team/alpha/service" {
		t.Fatalf("issued.UserPath = %q, want /team/alpha/service", issued.UserPath)
	}
	if !reflect.DeepEqual(issued.Labels, []string{"team-a", "batch"}) {
		t.Fatalf("issued.Labels = %v, want [team-a batch]", issued.Labels)
	}

	listCtx, listRec := newHandlerContext("/admin/auth-keys")
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
	if views[0].UserPath != "/team/alpha/service" {
		t.Fatalf("views[0].UserPath = %q, want /team/alpha/service", views[0].UserPath)
	}
	if !reflect.DeepEqual(views[0].Labels, []string{"team-a", "batch"}) {
		t.Fatalf("views[0].Labels = %v, want [team-a batch]", views[0].Labels)
	}

	deactivateReq := httptest.NewRequest(http.MethodPost, "/admin/auth-keys/"+issued.ID+"/deactivate", nil)
	deactivateRec := httptest.NewRecorder()
	deactivateCtx := e.NewContext(deactivateReq, deactivateRec)
	deactivateCtx.SetPathValues(echo.PathValues{{Name: "id", Value: issued.ID}})

	if err := h.DeactivateAuthKey(deactivateCtx); err != nil {
		t.Fatalf("DeactivateAuthKey() error = %v", err)
	}
	if deactivateRec.Code != http.StatusNoContent {
		t.Fatalf("DeactivateAuthKey() status = %d, want 204", deactivateRec.Code)
	}

	listCtx, listRec = newHandlerContext("/admin/auth-keys")
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

func TestUpdateAuthKeyLabels(t *testing.T) {
	h := newAuthKeyHandler(t, newAuthKeyTestStore())
	e := echo.New()

	createReq := httptest.NewRequest(http.MethodPost, "/admin/auth-keys", bytes.NewBufferString(`{"name":"primary","labels":["old"]}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	if err := h.CreateAuthKey(e.NewContext(createReq, createRec)); err != nil {
		t.Fatalf("CreateAuthKey() error = %v", err)
	}
	var issued authkeys.IssuedKey
	if err := json.Unmarshal(createRec.Body.Bytes(), &issued); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}

	updateLabels := func(id, body string) (*httptest.ResponseRecorder, error) {
		req := httptest.NewRequest(http.MethodPut, "/admin/auth-keys/"+id+"/labels", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		ctx := e.NewContext(req, rec)
		ctx.SetPathValues(echo.PathValues{{Name: "id", Value: id}})
		return rec, h.UpdateAuthKeyLabels(ctx)
	}

	rec, err := updateLabels(issued.ID, `{"labels":[" prod ","batch","prod"]}`)
	if err != nil {
		t.Fatalf("UpdateAuthKeyLabels() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("UpdateAuthKeyLabels() status = %d, want 200", rec.Code)
	}
	var view authkeys.View
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("unmarshal update response: %v", err)
	}
	if !reflect.DeepEqual(view.Labels, []string{"prod", "batch"}) {
		t.Fatalf("view.Labels = %v, want [prod batch]", view.Labels)
	}

	rec, err = updateLabels(issued.ID, `{"labels":[]}`)
	if err != nil {
		t.Fatalf("UpdateAuthKeyLabels(clear) error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("UpdateAuthKeyLabels(clear) status = %d, want 200", rec.Code)
	}
	var clearedView authkeys.View
	if err := json.Unmarshal(rec.Body.Bytes(), &clearedView); err != nil {
		t.Fatalf("unmarshal clear response: %v", err)
	}
	if clearedView.Labels != nil {
		t.Fatalf("view.Labels after clear = %v, want nil", clearedView.Labels)
	}

	rec, err = updateLabels("missing-id", `{"labels":["x"]}`)
	if err != nil {
		t.Fatalf("UpdateAuthKeyLabels(missing) error = %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("UpdateAuthKeyLabels(missing) status = %d, want 404", rec.Code)
	}
}

func TestCreateAuthKeyRejectsInvalidUserPath(t *testing.T) {
	h := newAuthKeyHandler(t, newAuthKeyTestStore())
	e := echo.New()

	createReq := httptest.NewRequest(http.MethodPost, "/admin/auth-keys", bytes.NewBufferString(`{"name":"primary","user_path":"/team/../alpha"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	createCtx := e.NewContext(createReq, createRec)

	if err := h.CreateAuthKey(createCtx); err != nil {
		t.Fatalf("CreateAuthKey() error = %v", err)
	}
	if createRec.Code != http.StatusBadRequest {
		t.Fatalf("CreateAuthKey() status = %d, want 400", createRec.Code)
	}
}
