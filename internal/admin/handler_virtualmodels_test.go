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

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

// vmTestStore is an in-memory virtualmodels.Store for admin handler tests.
type vmTestStore struct {
	items map[string]virtualmodels.VirtualModel
}

func newVMTestStore(items ...virtualmodels.VirtualModel) *vmTestStore {
	store := &vmTestStore{items: make(map[string]virtualmodels.VirtualModel, len(items))}
	for _, item := range items {
		store.items[item.Source] = item
	}
	return store
}

func (s *vmTestStore) List(_ context.Context) ([]virtualmodels.VirtualModel, error) {
	result := make([]virtualmodels.VirtualModel, 0, len(s.items))
	for _, item := range s.items {
		result = append(result, item)
	}
	return result, nil
}

func (s *vmTestStore) Get(_ context.Context, source string) (*virtualmodels.VirtualModel, error) {
	item, ok := s.items[source]
	if !ok {
		return nil, virtualmodels.ErrNotFound
	}
	clone := item
	return &clone, nil
}

func (s *vmTestStore) Upsert(_ context.Context, vm virtualmodels.VirtualModel) error {
	s.items[vm.Source] = vm
	return nil
}

func (s *vmTestStore) Delete(_ context.Context, source string) error {
	if _, ok := s.items[source]; !ok {
		return virtualmodels.ErrNotFound
	}
	delete(s.items, source)
	return nil
}

func (s *vmTestStore) Close() error { return nil }

type failingVMStore struct {
	listErr   error
	upsertErr error
	deleteErr error
}

func (s *failingVMStore) List(_ context.Context) ([]virtualmodels.VirtualModel, error) {
	return nil, s.listErr
}
func (s *failingVMStore) Get(_ context.Context, _ string) (*virtualmodels.VirtualModel, error) {
	return nil, virtualmodels.ErrNotFound
}
func (s *failingVMStore) Upsert(_ context.Context, _ virtualmodels.VirtualModel) error {
	return s.upsertErr
}
func (s *failingVMStore) Delete(_ context.Context, _ string) error { return s.deleteErr }
func (s *failingVMStore) Close() error                             { return nil }

type vmTestCatalog struct {
	providerTypes map[string]string
	models        map[string]core.Model
}

func newVMTestCatalog() *vmTestCatalog {
	return &vmTestCatalog{
		providerTypes: map[string]string{},
		models:        map[string]core.Model{},
	}
}

func (c *vmTestCatalog) add(model, providerType string) {
	c.providerTypes[model] = providerType
	c.models[model] = core.Model{ID: model, Object: "model"}
}

func (c *vmTestCatalog) Supports(model string) bool {
	_, ok := c.models[model]
	return ok
}

func (c *vmTestCatalog) ModelAvailable(model string) bool {
	return c.Supports(model)
}

func (c *vmTestCatalog) GetProviderType(model string) string {
	return c.providerTypes[model]
}

func (c *vmTestCatalog) LookupModel(model string) (*core.Model, bool) {
	value, ok := c.models[model]
	if !ok {
		return nil, false
	}
	clone := value
	return &clone, true
}

func (c *vmTestCatalog) ProviderNames() []string {
	seen := map[string]struct{}{}
	names := make([]string, 0)
	for _, providerType := range c.providerTypes {
		if providerType == "" {
			continue
		}
		if _, ok := seen[providerType]; ok {
			continue
		}
		seen[providerType] = struct{}{}
		names = append(names, providerType)
	}
	return names
}

func newVMService(t *testing.T, catalog *vmTestCatalog, store virtualmodels.Store, defaultEnabled bool) *virtualmodels.Service {
	t.Helper()
	service, err := virtualmodels.NewService(store, catalog, defaultEnabled)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	return service
}

func newVMHandler(t *testing.T, items ...virtualmodels.VirtualModel) *Handler {
	t.Helper()
	catalog := newVMTestCatalog()
	catalog.add("openai/gpt-4o", "openai")
	service := newVMService(t, catalog, newVMTestStore(items...), true)
	return NewHandler(nil, nil, WithVirtualModels(service))
}

func redirectVM(source, target string, enabled bool) virtualmodels.VirtualModel {
	selector, _ := core.ParseModelSelector(target, "")
	return virtualmodels.VirtualModel{
		Source:  source,
		Targets: []virtualmodels.Target{{Provider: selector.Provider, Model: selector.Model}},
		Enabled: enabled,
	}
}

func TestListVirtualModels_RedirectAndPolicy(t *testing.T) {
	h := newVMHandler(t,
		redirectVM("smart", "openai/gpt-4o", true),
		virtualmodels.VirtualModel{Source: "openai/gpt-4o", ProviderName: "openai", Model: "gpt-4o", UserPaths: []string{"/team"}, Enabled: true},
	)
	c, rec := newHandlerContext("/admin/virtual-models")

	if err := h.ListVirtualModels(c); err != nil {
		t.Fatalf("ListVirtualModels() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body []virtualmodels.View
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(body) != 2 {
		t.Fatalf("len(body) = %d, want 2 (%#v)", len(body), body)
	}
	kinds := map[string]virtualmodels.View{}
	for _, v := range body {
		kinds[v.Source] = v
	}
	if got := kinds["smart"]; got.Kind != virtualmodels.KindRedirect || !got.Valid {
		t.Fatalf("smart view = %#v, want valid redirect", got)
	}
	if got := kinds["openai/gpt-4o"]; got.Kind != virtualmodels.KindPolicy {
		t.Fatalf("policy view = %#v, want policy", got)
	}
}

func TestVirtualModelEndpointsReturn503WhenUnavailable(t *testing.T) {
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

	listCtx, listRec := newHandlerContext("/admin/virtual-models")
	assertUnavailable("ListVirtualModels", h.ListVirtualModels(listCtx), listRec)

	putReq := httptest.NewRequest(http.MethodPut, "/admin/virtual-models", bytes.NewBufferString(`{"source":"smart","target_model":"openai/gpt-4o"}`))
	putReq.Header.Set("Content-Type", "application/json")
	putRec := httptest.NewRecorder()
	assertUnavailable("UpsertVirtualModel", h.UpsertVirtualModel(e.NewContext(putReq, putRec)), putRec)

	deleteReq := httptest.NewRequest(http.MethodDelete, "/admin/virtual-models", bytes.NewBufferString(`{"source":"smart"}`))
	deleteReq.Header.Set("Content-Type", "application/json")
	deleteRec := httptest.NewRecorder()
	assertUnavailable("DeleteVirtualModel", h.DeleteVirtualModel(e.NewContext(deleteReq, deleteRec)), deleteRec)
}

func TestUpsertAndDeleteRedirectVirtualModel(t *testing.T) {
	h := newVMHandler(t)
	e := echo.New()

	putReq := httptest.NewRequest(http.MethodPut, "/admin/virtual-models", bytes.NewBufferString(`{"source":"smart","target_model":"openai/gpt-4o","description":"primary"}`))
	putReq.Header.Set("Content-Type", "application/json")
	putRec := httptest.NewRecorder()
	if err := h.UpsertVirtualModel(e.NewContext(putReq, putRec)); err != nil {
		t.Fatalf("UpsertVirtualModel() error = %v", err)
	}
	if putRec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200 body=%s", putRec.Code, putRec.Body.String())
	}
	var view virtualmodels.View
	if err := json.Unmarshal(putRec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode upsert response: %v", err)
	}
	if view.Source != "smart" || view.Kind != virtualmodels.KindRedirect {
		t.Fatalf("view = %#v, want redirect smart", view)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/admin/virtual-models", bytes.NewBufferString(`{"source":"smart"}`))
	deleteReq.Header.Set("Content-Type", "application/json")
	deleteRec := httptest.NewRecorder()
	if err := h.DeleteVirtualModel(e.NewContext(deleteReq, deleteRec)); err != nil {
		t.Fatalf("DeleteVirtualModel() error = %v", err)
	}
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", deleteRec.Code)
	}
}

func TestUpsertVirtualModelRenamesViaOldSource(t *testing.T) {
	h := newVMHandler(t, redirectVM("smart", "openai/gpt-4o", false))
	e := echo.New()

	body := `{"source":"smarter","old_source":"smart","target_model":"openai/gpt-4o"}`
	req := httptest.NewRequest(http.MethodPut, "/admin/virtual-models", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	if err := h.UpsertVirtualModel(e.NewContext(req, rec)); err != nil {
		t.Fatalf("UpsertVirtualModel(rename) error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("rename status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var view virtualmodels.View
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode rename response: %v", err)
	}
	if view.Source != "smarter" || view.Kind != virtualmodels.KindRedirect {
		t.Fatalf("view = %#v, want redirect smarter", view)
	}
	// The omitted enabled flag is carried over from the old row (disabled).
	if view.Enabled {
		t.Fatalf("rename flipped a disabled redirect to enabled: %#v", view)
	}
	// The old source no longer exists.
	if _, ok := h.virtualModels.Get("smart"); ok {
		t.Fatalf("old source still present after rename")
	}
}

func TestUpsertVirtualModelRejectsRenameOntoExisting(t *testing.T) {
	h := newVMHandler(t, redirectVM("smart", "openai/gpt-4o", true), redirectVM("taken", "openai/gpt-4o", true))
	e := echo.New()

	body := `{"source":"taken","old_source":"smart","target_model":"openai/gpt-4o"}`
	req := httptest.NewRequest(http.MethodPut, "/admin/virtual-models", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	if err := h.UpsertVirtualModel(e.NewContext(req, rec)); err != nil {
		t.Fatalf("UpsertVirtualModel(rename conflict) error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("rename conflict status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
	// Both rows survive the rejected rename.
	if _, ok := h.virtualModels.Get("smart"); !ok {
		t.Fatalf("source smart was lost after a rejected rename")
	}
	if _, ok := h.virtualModels.Get("taken"); !ok {
		t.Fatalf("source taken was lost after a rejected rename")
	}
}

func TestUpsertPolicyVirtualModelAcceptsEmptyUserPaths(t *testing.T) {
	h := newVMHandler(t)
	e := echo.New()

	putReq := httptest.NewRequest(http.MethodPut, "/admin/virtual-models", bytes.NewBufferString(`{"source":"openai/gpt-4o","enabled":false}`))
	putReq.Header.Set("Content-Type", "application/json")
	putRec := httptest.NewRecorder()
	if err := h.UpsertVirtualModel(e.NewContext(putReq, putRec)); err != nil {
		t.Fatalf("UpsertVirtualModel() error = %v", err)
	}
	if putRec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200 body=%s", putRec.Code, putRec.Body.String())
	}
	var view virtualmodels.View
	if err := json.Unmarshal(putRec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode upsert response: %v", err)
	}
	if view.Kind != virtualmodels.KindPolicy {
		t.Fatalf("view.Kind = %q, want policy", view.Kind)
	}
	if view.Enabled {
		t.Fatalf("view.Enabled = true, want false (disabled policy)")
	}
}

func TestUpsertVirtualModelPreservesEnabledWhenOmitted(t *testing.T) {
	h := newVMHandler(t, redirectVM("smart", "openai/gpt-4o", false))
	e := echo.New()

	req := httptest.NewRequest(http.MethodPut, "/admin/virtual-models", bytes.NewBufferString(`{"source":"smart","target_model":"openai/gpt-4o","description":"after"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	if err := h.UpsertVirtualModel(e.NewContext(req, rec)); err != nil {
		t.Fatalf("UpsertVirtualModel() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var view virtualmodels.View
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if view.Enabled {
		t.Fatalf("enabled = true, want false (preserved)")
	}
	if view.Description != "after" {
		t.Fatalf("description = %q, want after", view.Description)
	}
}

func TestUpsertVirtualModelLoadBalanced(t *testing.T) {
	catalog := newVMTestCatalog()
	catalog.add("openai/gpt-4o", "openai")
	catalog.add("groq/llama", "groq")
	service := newVMService(t, catalog, newVMTestStore(), true)
	h := NewHandler(nil, nil, WithVirtualModels(service))
	e := echo.New()

	body := `{"source":"smart","strategy":"cost","targets":[{"model":"openai/gpt-4o"},{"model":"groq/llama","weight":2}]}`
	req := httptest.NewRequest(http.MethodPut, "/admin/virtual-models", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	if err := h.UpsertVirtualModel(e.NewContext(req, rec)); err != nil {
		t.Fatalf("UpsertVirtualModel() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}

	var view virtualmodels.View
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if view.Kind != virtualmodels.KindRedirect {
		t.Fatalf("kind = %q, want redirect", view.Kind)
	}
	if len(view.Targets) != 2 {
		t.Fatalf("targets = %d, want 2 (%#v)", len(view.Targets), view.Targets)
	}
	if view.Strategy != virtualmodels.StrategyCost {
		t.Fatalf("strategy = %q, want cost", view.Strategy)
	}
	if view.Targets[1].Weight != 2 {
		t.Fatalf("target[1] weight = %v, want 2", view.Targets[1].Weight)
	}
}

func TestUpsertVirtualModelRejectsBlankTargets(t *testing.T) {
	h := newVMHandler(t)
	e := echo.New()

	// A targets list whose only entry has an empty model must not be silently
	// demoted to an access policy.
	req := httptest.NewRequest(http.MethodPut, "/admin/virtual-models", bytes.NewBufferString(`{"source":"smart","targets":[{"model":"  "}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	if err := h.UpsertVirtualModel(e.NewContext(req, rec)); err != nil {
		t.Fatalf("UpsertVirtualModel() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
	if !containsString(rec.Body.String(), "invalid_request_error") {
		t.Fatalf("body = %s, want invalid_request_error", rec.Body.String())
	}
}

func TestUpsertVirtualModelReturns400OnValidationError(t *testing.T) {
	h := newVMHandler(t)
	e := echo.New()

	req := httptest.NewRequest(http.MethodPut, "/admin/virtual-models", bytes.NewBufferString(`{"source":"smart","target_model":"openai/missing"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	if err := h.UpsertVirtualModel(e.NewContext(req, rec)); err != nil {
		t.Fatalf("UpsertVirtualModel() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
	if !containsString(rec.Body.String(), "invalid_request_error") {
		t.Fatalf("body = %s, want invalid_request_error", rec.Body.String())
	}
}

func TestUpsertVirtualModelBubblesProviderErrorOnStoreFailure(t *testing.T) {
	catalog := newVMTestCatalog()
	catalog.add("openai/gpt-4o", "openai")
	service := newVMService(t, catalog, &failingVMStore{upsertErr: errors.New("disk full")}, true)
	h := NewHandler(nil, nil, WithVirtualModels(service))
	e := echo.New()

	req := httptest.NewRequest(http.MethodPut, "/admin/virtual-models", bytes.NewBufferString(`{"source":"smart","target_model":"openai/gpt-4o"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	if err := h.UpsertVirtualModel(e.NewContext(req, rec)); err != nil {
		t.Fatalf("UpsertVirtualModel() error = %v", err)
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeleteVirtualModelNotFound(t *testing.T) {
	h := newVMHandler(t)
	e := echo.New()

	req := httptest.NewRequest(http.MethodDelete, "/admin/virtual-models", bytes.NewBufferString(`{"source":"missing"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	if err := h.DeleteVirtualModel(e.NewContext(req, rec)); err != nil {
		t.Fatalf("DeleteVirtualModel() error = %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 body=%s", rec.Code, rec.Body.String())
	}
}
