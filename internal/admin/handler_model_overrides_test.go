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

	"gomodel/internal/core"
	"gomodel/internal/modeloverrides"
	"gomodel/internal/providers"
)

type modelOverrideTestStore struct {
	items map[string]modeloverrides.Override
}

func newModelOverrideTestStore(items ...modeloverrides.Override) *modelOverrideTestStore {
	store := &modelOverrideTestStore{items: make(map[string]modeloverrides.Override, len(items))}
	for _, item := range items {
		store.items[item.Selector] = item
	}
	return store
}

func (s *modelOverrideTestStore) List(_ context.Context) ([]modeloverrides.Override, error) {
	result := make([]modeloverrides.Override, 0, len(s.items))
	for _, item := range s.items {
		result = append(result, item)
	}
	return result, nil
}

func (s *modelOverrideTestStore) Upsert(_ context.Context, override modeloverrides.Override) error {
	s.items[override.Selector] = override
	return nil
}

func (s *modelOverrideTestStore) Delete(_ context.Context, selector string) error {
	if _, ok := s.items[selector]; !ok {
		return modeloverrides.ErrNotFound
	}
	delete(s.items, selector)
	return nil
}

func (s *modelOverrideTestStore) Close() error { return nil }

type failingModelOverrideStore struct {
	listErr   error
	upsertErr error
	deleteErr error
}

func (s *failingModelOverrideStore) List(_ context.Context) ([]modeloverrides.Override, error) {
	return nil, s.listErr
}

func (s *failingModelOverrideStore) Upsert(_ context.Context, _ modeloverrides.Override) error {
	return s.upsertErr
}

func (s *failingModelOverrideStore) Delete(_ context.Context, _ string) error {
	return s.deleteErr
}

func (s *failingModelOverrideStore) Close() error { return nil }

func newModelOverrideRegistry(t *testing.T) *providers.ModelRegistry {
	t.Helper()
	registry := providers.NewModelRegistry()
	mock := &handlerMockProvider{
		models: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-4o", Object: "model", OwnedBy: "openai"},
			},
		},
	}
	registry.RegisterProviderWithNameAndType(mock, "openai", "openai")
	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	return registry
}

func newModelOverrideService(t *testing.T, store modeloverrides.Store, defaultEnabled bool) *modeloverrides.Service {
	t.Helper()
	service, err := modeloverrides.NewService(store, newModelOverrideRegistry(t), defaultEnabled)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	return service
}

func TestListModels_IncludesModelAccessState(t *testing.T) {
	registry := newModelOverrideRegistry(t)
	service := newModelOverrideService(t, newModelOverrideTestStore(modeloverrides.Override{
		Selector:  "openai/gpt-4o",
		UserPaths: []string{"/team/alpha"},
	}), false)

	h := NewHandler(nil, registry, WithModelOverrides(service))
	c, rec := newHandlerContext("/admin/api/v1/models")

	if err := h.ListModels(c); err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body []modelInventoryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body) != 1 {
		t.Fatalf("len(body) = %d, want 1", len(body))
	}

	row := body[0]
	if row.Model.ID != "gpt-4o" {
		t.Fatalf("row.Model.ID = %q, want gpt-4o", row.Model.ID)
	}
	if row.Access.Selector != "openai/gpt-4o" {
		t.Fatalf("row.Access.Selector = %q, want openai/gpt-4o", row.Access.Selector)
	}
	if row.Access.DefaultEnabled {
		t.Fatal("row.Access.DefaultEnabled = true, want false")
	}
	if !row.Access.EffectiveEnabled {
		t.Fatal("row.Access.EffectiveEnabled = false, want true")
	}
	if len(row.Access.UserPaths) != 1 || row.Access.UserPaths[0] != "/team/alpha" {
		t.Fatalf("row.Access.UserPaths = %#v, want [/team/alpha]", row.Access.UserPaths)
	}
	if row.Access.Override == nil || row.Access.Override.Selector != "openai/gpt-4o" {
		t.Fatalf("row.Access.Override = %#v, want exact override", row.Access.Override)
	}
}

func TestListModels_AppliesProviderWideOverrideToConcreteModels(t *testing.T) {
	registry := newModelOverrideRegistry(t)
	service := newModelOverrideService(t, newModelOverrideTestStore(modeloverrides.Override{
		Selector:  "openai/",
		UserPaths: []string{"/team/provider"},
	}), true)

	h := NewHandler(nil, registry, WithModelOverrides(service))
	c, rec := newHandlerContext("/admin/api/v1/models")

	if err := h.ListModels(c); err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body []modelInventoryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body) != 1 {
		t.Fatalf("len(body) = %d, want 1", len(body))
	}

	row := body[0]
	if row.Access.Selector != "openai/gpt-4o" {
		t.Fatalf("row.Access.Selector = %q, want openai/gpt-4o", row.Access.Selector)
	}
	if row.Access.DefaultEnabled != true {
		t.Fatal("row.Access.DefaultEnabled = false, want true")
	}
	if !row.Access.EffectiveEnabled {
		t.Fatal("row.Access.EffectiveEnabled = false, want true")
	}
	if len(row.Access.UserPaths) != 1 || row.Access.UserPaths[0] != "/team/provider" {
		t.Fatalf("row.Access.UserPaths = %#v, want [/team/provider]", row.Access.UserPaths)
	}
	if row.Access.Override != nil {
		t.Fatalf("row.Access.Override = %#v, want nil for provider-wide override", row.Access.Override)
	}
}

func TestListModels_AppliesGlobalOverrideToConcreteModels(t *testing.T) {
	registry := newModelOverrideRegistry(t)
	service := newModelOverrideService(t, newModelOverrideTestStore(modeloverrides.Override{
		Selector:  "/",
		UserPaths: []string{"/team/global"},
	}), true)

	h := NewHandler(nil, registry, WithModelOverrides(service))
	c, rec := newHandlerContext("/admin/api/v1/models")

	if err := h.ListModels(c); err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body []modelInventoryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body) != 1 {
		t.Fatalf("len(body) = %d, want 1", len(body))
	}

	row := body[0]
	if row.Access.Selector != "openai/gpt-4o" {
		t.Fatalf("row.Access.Selector = %q, want openai/gpt-4o", row.Access.Selector)
	}
	if row.Access.DefaultEnabled != true {
		t.Fatal("row.Access.DefaultEnabled = false, want true")
	}
	if !row.Access.EffectiveEnabled {
		t.Fatal("row.Access.EffectiveEnabled = false, want true")
	}
	if len(row.Access.UserPaths) != 1 || row.Access.UserPaths[0] != "/team/global" {
		t.Fatalf("row.Access.UserPaths = %#v, want [/team/global]", row.Access.UserPaths)
	}
	if row.Access.Override != nil {
		t.Fatalf("row.Access.Override = %#v, want nil for global override", row.Access.Override)
	}
}

func TestModelOverrideEndpointsReturn503WhenServiceUnavailable(t *testing.T) {
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

	listCtx, listRec := newHandlerContext("/admin/api/v1/model-overrides")
	assertUnavailable("ListModelOverrides", h.ListModelOverrides(listCtx), listRec)

	putReq := httptest.NewRequest(http.MethodPut, "/admin/api/v1/model-overrides/openai%2Fgpt-4o", bytes.NewBufferString(`{"user_paths":["/"]}`))
	putReq.Header.Set("Content-Type", "application/json")
	putRec := httptest.NewRecorder()
	putCtx := e.NewContext(putReq, putRec)
	putCtx.SetPathValues(echo.PathValues{{Name: "selector", Value: "openai/gpt-4o"}})
	assertUnavailable("UpsertModelOverride", h.UpsertModelOverride(putCtx), putRec)

	deleteReq := httptest.NewRequest(http.MethodDelete, "/admin/api/v1/model-overrides/openai%2Fgpt-4o", nil)
	deleteRec := httptest.NewRecorder()
	deleteCtx := e.NewContext(deleteReq, deleteRec)
	deleteCtx.SetPathValues(echo.PathValues{{Name: "selector", Value: "openai/gpt-4o"}})
	assertUnavailable("DeleteModelOverride", h.DeleteModelOverride(deleteCtx), deleteRec)
}

func TestUpsertAndDeleteModelOverride(t *testing.T) {
	service := newModelOverrideService(t, newModelOverrideTestStore(), true)
	h := NewHandler(nil, nil, WithModelOverrides(service))
	e := echo.New()

	putReq := httptest.NewRequest(http.MethodPut, "/admin/api/v1/model-overrides/openai%2Fgpt-4o", bytes.NewBufferString(`{"user_paths":["team/alpha"]}`))
	putReq.Header.Set("Content-Type", "application/json")
	putRec := httptest.NewRecorder()
	putCtx := e.NewContext(putReq, putRec)
	putCtx.SetPathValues(echo.PathValues{{Name: "selector", Value: "openai/gpt-4o"}})

	if err := h.UpsertModelOverride(putCtx); err != nil {
		t.Fatalf("UpsertModelOverride() error = %v", err)
	}
	if putRec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200", putRec.Code)
	}

	var body modeloverrides.Override
	if err := json.Unmarshal(putRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode upsert response: %v", err)
	}
	if body.Selector != "openai/gpt-4o" {
		t.Fatalf("body.Selector = %q, want openai/gpt-4o", body.Selector)
	}
	if len(body.UserPaths) != 1 || body.UserPaths[0] != "/team/alpha" {
		t.Fatalf("body.UserPaths = %#v, want [/team/alpha]", body.UserPaths)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/admin/api/v1/model-overrides/openai%2Fgpt-4o", nil)
	deleteRec := httptest.NewRecorder()
	deleteCtx := e.NewContext(deleteReq, deleteRec)
	deleteCtx.SetPathValues(echo.PathValues{{Name: "selector", Value: "openai/gpt-4o"}})

	if err := h.DeleteModelOverride(deleteCtx); err != nil {
		t.Fatalf("DeleteModelOverride() error = %v", err)
	}
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", deleteRec.Code)
	}
}

func TestUpsertAndDeleteProviderWideModelOverride(t *testing.T) {
	service := newModelOverrideService(t, newModelOverrideTestStore(), true)
	h := NewHandler(nil, nil, WithModelOverrides(service))
	e := echo.New()

	putReq := httptest.NewRequest(http.MethodPut, "/admin/api/v1/model-overrides/openai%2F", bytes.NewBufferString(`{"user_paths":["/non-existing"]}`))
	putReq.Header.Set("Content-Type", "application/json")
	putRec := httptest.NewRecorder()
	putCtx := e.NewContext(putReq, putRec)
	putCtx.SetPathValues(echo.PathValues{{Name: "selector", Value: "openai/"}})

	if err := h.UpsertModelOverride(putCtx); err != nil {
		t.Fatalf("UpsertModelOverride() error = %v", err)
	}
	if putRec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200", putRec.Code)
	}

	var body modeloverrides.Override
	if err := json.Unmarshal(putRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode upsert response: %v", err)
	}
	if body.Selector != "openai/" {
		t.Fatalf("body.Selector = %q, want openai/", body.Selector)
	}
	if len(body.UserPaths) != 1 || body.UserPaths[0] != "/non-existing" {
		t.Fatalf("body.UserPaths = %#v, want [/non-existing]", body.UserPaths)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/admin/api/v1/model-overrides/openai%2F", nil)
	deleteRec := httptest.NewRecorder()
	deleteCtx := e.NewContext(deleteReq, deleteRec)
	deleteCtx.SetPathValues(echo.PathValues{{Name: "selector", Value: "openai/"}})

	if err := h.DeleteModelOverride(deleteCtx); err != nil {
		t.Fatalf("DeleteModelOverride() error = %v", err)
	}
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", deleteRec.Code)
	}
}

func TestUpsertAndDeleteGlobalModelOverride(t *testing.T) {
	service := newModelOverrideService(t, newModelOverrideTestStore(), true)
	h := NewHandler(nil, nil, WithModelOverrides(service))
	e := echo.New()

	putReq := httptest.NewRequest(http.MethodPut, "/admin/api/v1/model-overrides/%2F", bytes.NewBufferString(`{"user_paths":["/"]}`))
	putReq.Header.Set("Content-Type", "application/json")
	putRec := httptest.NewRecorder()
	putCtx := e.NewContext(putReq, putRec)
	putCtx.SetPathValues(echo.PathValues{{Name: "selector", Value: "/"}})

	if err := h.UpsertModelOverride(putCtx); err != nil {
		t.Fatalf("UpsertModelOverride() error = %v", err)
	}
	if putRec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200", putRec.Code)
	}

	var body modeloverrides.Override
	if err := json.Unmarshal(putRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode upsert response: %v", err)
	}
	if body.Selector != "/" {
		t.Fatalf("body.Selector = %q, want /", body.Selector)
	}
	if len(body.UserPaths) != 1 || body.UserPaths[0] != "/" {
		t.Fatalf("body.UserPaths = %#v, want [/]", body.UserPaths)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/admin/api/v1/model-overrides/%2F", nil)
	deleteRec := httptest.NewRecorder()
	deleteCtx := e.NewContext(deleteReq, deleteRec)
	deleteCtx.SetPathValues(echo.PathValues{{Name: "selector", Value: "/"}})

	if err := h.DeleteModelOverride(deleteCtx); err != nil {
		t.Fatalf("DeleteModelOverride() error = %v", err)
	}
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", deleteRec.Code)
	}
}

func TestUpsertModelOverrideReturnsBadRequestForValidationErrors(t *testing.T) {
	service := newModelOverrideService(t, newModelOverrideTestStore(), true)
	h := NewHandler(nil, nil, WithModelOverrides(service))
	e := echo.New()

	req := httptest.NewRequest(http.MethodPut, "/admin/api/v1/model-overrides/openai%2Fgpt-4o", bytes.NewBufferString(`{"user_paths":[]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPathValues(echo.PathValues{{Name: "selector", Value: "openai/gpt-4o"}})

	if err := h.UpsertModelOverride(c); err != nil {
		t.Fatalf("UpsertModelOverride() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestModelOverrideWriteErrorsBubbleProviderErrors(t *testing.T) {
	service := newModelOverrideService(t, &failingModelOverrideStore{
		upsertErr: errors.New("boom"),
	}, true)
	h := NewHandler(nil, nil, WithModelOverrides(service))
	e := echo.New()

	req := httptest.NewRequest(http.MethodPut, "/admin/api/v1/model-overrides/openai%2Fgpt-4o", bytes.NewBufferString(`{"user_paths":["/"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPathValues(echo.PathValues{{Name: "selector", Value: "openai/gpt-4o"}})

	if err := h.UpsertModelOverride(c); err != nil {
		t.Fatalf("UpsertModelOverride() error = %v", err)
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}

	var body map[string]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := body["error"]["type"]; got != string(core.ErrorTypeProvider) {
		t.Fatalf("error type = %v, want %s", got, core.ErrorTypeProvider)
	}
	if got := body["error"]["message"]; got != "upsert model override: boom" {
		t.Fatalf("error message = %v, want upsert model override: boom", got)
	}
}

func TestDeleteModelOverrideWriteErrorsBubbleProviderErrors(t *testing.T) {
	service := newModelOverrideService(t, &failingModelOverrideStore{
		deleteErr: errors.New("boom"),
	}, true)
	h := NewHandler(nil, nil, WithModelOverrides(service))
	e := echo.New()

	req := httptest.NewRequest(http.MethodDelete, "/admin/api/v1/model-overrides/openai%2Fgpt-4o", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPathValues(echo.PathValues{{Name: "selector", Value: "openai/gpt-4o"}})

	if err := h.DeleteModelOverride(c); err != nil {
		t.Fatalf("DeleteModelOverride() error = %v", err)
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}

	var body map[string]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := body["error"]["type"]; got != string(core.ErrorTypeProvider) {
		t.Fatalf("error type = %v, want %s", got, core.ErrorTypeProvider)
	}
	if got := body["error"]["message"]; got != "delete model override: boom" {
		t.Fatalf("error message = %v, want delete model override: boom", got)
	}
}
