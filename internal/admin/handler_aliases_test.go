package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"

	"gomodel/internal/aliases"
	"gomodel/internal/core"
)

type aliasTestStore struct {
	items map[string]aliases.Alias
}

func newAliasTestStore(items ...aliases.Alias) *aliasTestStore {
	store := &aliasTestStore{items: make(map[string]aliases.Alias, len(items))}
	for _, item := range items {
		store.items[item.Name] = item
	}
	return store
}

func (s *aliasTestStore) List(_ context.Context) ([]aliases.Alias, error) {
	result := make([]aliases.Alias, 0, len(s.items))
	for _, item := range s.items {
		result = append(result, item)
	}
	return result, nil
}

func (s *aliasTestStore) Get(_ context.Context, name string) (*aliases.Alias, error) {
	item, ok := s.items[name]
	if !ok {
		return nil, aliases.ErrNotFound
	}
	copy := item
	return &copy, nil
}

func (s *aliasTestStore) Upsert(_ context.Context, alias aliases.Alias) error {
	s.items[alias.Name] = alias
	return nil
}

func (s *aliasTestStore) Delete(_ context.Context, name string) error {
	if _, ok := s.items[name]; !ok {
		return aliases.ErrNotFound
	}
	delete(s.items, name)
	return nil
}

func (s *aliasTestStore) Close() error { return nil }

type failingAliasStore struct {
	listErr   error
	getErr    error
	upsertErr error
	deleteErr error
}

func (s *failingAliasStore) List(_ context.Context) ([]aliases.Alias, error) { return nil, s.listErr }
func (s *failingAliasStore) Get(_ context.Context, _ string) (*aliases.Alias, error) {
	return nil, s.getErr
}
func (s *failingAliasStore) Upsert(_ context.Context, _ aliases.Alias) error { return s.upsertErr }
func (s *failingAliasStore) Delete(_ context.Context, _ string) error        { return s.deleteErr }
func (s *failingAliasStore) Close() error                                    { return nil }

type aliasTestCatalog struct {
	providerTypes map[string]string
	models        map[string]core.Model
}

func newAliasTestCatalog() *aliasTestCatalog {
	return &aliasTestCatalog{
		providerTypes: map[string]string{},
		models:        map[string]core.Model{},
	}
}

func (c *aliasTestCatalog) add(model, providerType string) {
	c.providerTypes[model] = providerType
	c.models[model] = core.Model{ID: model, Object: "model"}
}

func (c *aliasTestCatalog) Supports(model string) bool {
	_, ok := c.models[model]
	return ok
}

func (c *aliasTestCatalog) GetProviderType(model string) string {
	return c.providerTypes[model]
}

func (c *aliasTestCatalog) LookupModel(model string) (*core.Model, bool) {
	value, ok := c.models[model]
	if !ok {
		return nil, false
	}
	copy := value
	return &copy, true
}

func newAliasHandler(t *testing.T, items ...aliases.Alias) *Handler {
	t.Helper()
	catalog := newAliasTestCatalog()
	catalog.add("gpt-4o", "openai")
	service, err := aliases.NewService(newAliasTestStore(items...), catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	return NewHandler(nil, nil, WithAliases(service))
}

func newAliasHandlerWithStore(t *testing.T, store aliases.Store) *Handler {
	t.Helper()
	catalog := newAliasTestCatalog()
	catalog.add("gpt-4o", "openai")
	service, err := aliases.NewService(store, catalog)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	return NewHandler(nil, nil, WithAliases(service))
}

func TestListAliases(t *testing.T) {
	h := newAliasHandler(t, aliases.Alias{Name: "smart", TargetModel: "gpt-4o", Enabled: true})
	c, rec := newHandlerContext("/admin/api/v1/aliases")

	if err := h.ListAliases(c); err != nil {
		t.Fatalf("ListAliases() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body []aliases.View
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body) != 1 || body[0].Name != "smart" || !body[0].Valid {
		t.Fatalf("response = %#v, want one valid alias", body)
	}
}

func TestAliasesEndpointsReturn503WhenServiceUnavailable(t *testing.T) {
	h := NewHandler(nil, nil)
	e := echo.New()

	assertUnavailable := func(name string, err error, rec *httptest.ResponseRecorder) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s error = %v", name, err)
		}
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503", name, rec.Code)
		}

		var body map[string]map[string]any
		if decodeErr := json.Unmarshal(rec.Body.Bytes(), &body); decodeErr != nil {
			t.Fatalf("%s decode error = %v", name, decodeErr)
		}
		if got := body["error"]["code"]; got != "feature_unavailable" {
			t.Fatalf("%s error code = %v, want feature_unavailable", name, got)
		}
	}

	listCtx, listRec := newHandlerContext("/admin/api/v1/aliases")
	assertUnavailable("ListAliases", h.ListAliases(listCtx), listRec)

	putReq := httptest.NewRequest(http.MethodPut, "/admin/api/v1/aliases/smart", bytes.NewBufferString(`{"target_model":"gpt-4o"}`))
	putReq.Header.Set("Content-Type", "application/json")
	putRec := httptest.NewRecorder()
	putCtx := e.NewContext(putReq, putRec)
	putCtx.SetPathValues(echo.PathValues{{Name: "name", Value: "smart"}})
	assertUnavailable("UpsertAlias", h.UpsertAlias(putCtx), putRec)

	deleteReq := httptest.NewRequest(http.MethodDelete, "/admin/api/v1/aliases/smart", nil)
	deleteRec := httptest.NewRecorder()
	deleteCtx := e.NewContext(deleteReq, deleteRec)
	deleteCtx.SetPathValues(echo.PathValues{{Name: "name", Value: "smart"}})
	assertUnavailable("DeleteAlias", h.DeleteAlias(deleteCtx), deleteRec)
}

func TestUpsertAliasAndDeleteAlias(t *testing.T) {
	h := newAliasHandler(t)
	e := echo.New()

	putReq := httptest.NewRequest(http.MethodPut, "/admin/api/v1/aliases/smart", bytes.NewBufferString(`{"target_model":"gpt-4o","description":"primary"}`))
	putReq.Header.Set("Content-Type", "application/json")
	putRec := httptest.NewRecorder()
	putCtx := e.NewContext(putReq, putRec)
	putCtx.SetPathValues(echo.PathValues{{Name: "name", Value: "smart"}})

	if err := h.UpsertAlias(putCtx); err != nil {
		t.Fatalf("UpsertAlias() error = %v", err)
	}
	if putRec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200", putRec.Code)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/admin/api/v1/aliases/smart", nil)
	deleteRec := httptest.NewRecorder()
	deleteCtx := e.NewContext(deleteReq, deleteRec)
	deleteCtx.SetPathValues(echo.PathValues{{Name: "name", Value: "smart"}})

	if err := h.DeleteAlias(deleteCtx); err != nil {
		t.Fatalf("DeleteAlias() error = %v", err)
	}
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", deleteRec.Code)
	}
}

func TestUpsertAliasReturns500OnStoreFailure(t *testing.T) {
	h := newAliasHandlerWithStore(t, &failingAliasStore{
		upsertErr: errors.New("disk full"),
	})
	e := echo.New()

	req := httptest.NewRequest(http.MethodPut, "/admin/api/v1/aliases/smart", bytes.NewBufferString(`{"target_model":"gpt-4o"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPathValues(echo.PathValues{{Name: "name", Value: "smart"}})

	if err := h.UpsertAlias(c); err != nil {
		t.Fatalf("UpsertAlias() error = %v", err)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !containsString(rec.Body.String(), "internal_error") {
		t.Fatalf("body = %s, want internal_error", rec.Body.String())
	}
}

func TestUpsertAliasReturns400OnValidationError(t *testing.T) {
	h := newAliasHandler(t)
	e := echo.New()

	req := httptest.NewRequest(http.MethodPut, "/admin/api/v1/aliases/smart", bytes.NewBufferString(`{"description":"missing target"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPathValues(echo.PathValues{{Name: "name", Value: "smart"}})

	if err := h.UpsertAlias(c); err != nil {
		t.Fatalf("UpsertAlias() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !containsString(rec.Body.String(), "invalid_request_error") {
		t.Fatalf("body = %s, want invalid_request_error", rec.Body.String())
	}
}

func TestDeleteAliasReturns500OnStoreFailure(t *testing.T) {
	h := newAliasHandlerWithStore(t, &failingAliasStore{
		deleteErr: errors.New("disk full"),
	})
	e := echo.New()

	req := httptest.NewRequest(http.MethodDelete, "/admin/api/v1/aliases/smart", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPathValues(echo.PathValues{{Name: "name", Value: "smart"}})

	if err := h.DeleteAlias(c); err != nil {
		t.Fatalf("DeleteAlias() error = %v", err)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !containsString(rec.Body.String(), "internal_error") {
		t.Fatalf("body = %s, want internal_error", rec.Body.String())
	}
}
