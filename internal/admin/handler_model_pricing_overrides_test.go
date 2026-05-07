package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"

	"gomodel/internal/pricingoverrides"
)

type modelPricingOverrideTestStore struct {
	items map[string]pricingoverrides.Override
}

func newModelPricingOverrideTestStore(items ...pricingoverrides.Override) *modelPricingOverrideTestStore {
	store := &modelPricingOverrideTestStore{items: make(map[string]pricingoverrides.Override, len(items))}
	for _, item := range items {
		store.items[item.Selector] = item
	}
	return store
}

func (s *modelPricingOverrideTestStore) List(_ context.Context) ([]pricingoverrides.Override, error) {
	result := make([]pricingoverrides.Override, 0, len(s.items))
	for _, item := range s.items {
		result = append(result, item)
	}
	return result, nil
}

func (s *modelPricingOverrideTestStore) Upsert(_ context.Context, override pricingoverrides.Override) error {
	s.items[override.Selector] = override
	return nil
}

func (s *modelPricingOverrideTestStore) Delete(_ context.Context, selector string) error {
	if _, ok := s.items[selector]; !ok {
		return pricingoverrides.ErrNotFound
	}
	delete(s.items, selector)
	return nil
}

func (s *modelPricingOverrideTestStore) Close() error { return nil }

func newModelPricingOverrideService(t *testing.T, store pricingoverrides.Store) *pricingoverrides.Service {
	t.Helper()
	service, err := pricingoverrides.NewService(store, newModelOverrideRegistry(t), nil)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	return service
}

func TestModelPricingOverrideLifecycle(t *testing.T) {
	service := newModelPricingOverrideService(t, newModelPricingOverrideTestStore())
	h := NewHandler(nil, nil, WithPricingOverrides(service))
	e := echo.New()

	putReq := httptest.NewRequest(http.MethodPut, "/admin/api/v1/model-pricing-overrides/openai%2Fgpt-4o", bytes.NewBufferString(`{"pricing":{"input_per_mtok":1.25}}`))
	putReq.Header.Set("Content-Type", "application/json")
	putRec := httptest.NewRecorder()
	putCtx := e.NewContext(putReq, putRec)
	putCtx.SetPathValues(echo.PathValues{{Name: "selector", Value: "openai/gpt-4o"}})

	if err := h.UpsertModelPricingOverride(putCtx); err != nil {
		t.Fatalf("UpsertModelPricingOverride() error = %v", err)
	}
	if putRec.Code != http.StatusOK {
		t.Fatalf("put status = %d, want 200 body=%s", putRec.Code, putRec.Body.String())
	}

	var body pricingoverrides.View
	if err := json.Unmarshal(putRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode upsert response: %v", err)
	}
	if body.Selector != "openai/gpt-4o" || body.ProviderName != "openai" || body.Model != "gpt-4o" {
		t.Fatalf("body selector parts = (%q, %q, %q), want openai/gpt-4o parts", body.Selector, body.ProviderName, body.Model)
	}
	if body.ScopeKind != pricingoverrides.ScopeProviderModel {
		t.Fatalf("ScopeKind = %q, want %q", body.ScopeKind, pricingoverrides.ScopeProviderModel)
	}
	if body.Pricing.InputPerMtok == nil || *body.Pricing.InputPerMtok != 1.25 {
		t.Fatalf("InputPerMtok = %#v, want 1.25", body.Pricing.InputPerMtok)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/admin/api/v1/model-pricing-overrides", nil)
	listRec := httptest.NewRecorder()
	listCtx := e.NewContext(listReq, listRec)
	if err := h.ListModelPricingOverrides(listCtx); err != nil {
		t.Fatalf("ListModelPricingOverrides() error = %v", err)
	}
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", listRec.Code)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/admin/api/v1/model-pricing-overrides/openai%2Fgpt-4o", nil)
	deleteRec := httptest.NewRecorder()
	deleteCtx := e.NewContext(deleteReq, deleteRec)
	deleteCtx.SetPathValues(echo.PathValues{{Name: "selector", Value: "openai/gpt-4o"}})
	if err := h.DeleteModelPricingOverride(deleteCtx); err != nil {
		t.Fatalf("DeleteModelPricingOverride() error = %v", err)
	}
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", deleteRec.Code)
	}
}

func TestUpsertModelPricingOverrideReturnsBadRequestForValidationErrors(t *testing.T) {
	service := newModelPricingOverrideService(t, newModelPricingOverrideTestStore())
	h := NewHandler(nil, nil, WithPricingOverrides(service))
	e := echo.New()

	req := httptest.NewRequest(http.MethodPut, "/admin/api/v1/model-pricing-overrides/openai%2Fgpt-4o", bytes.NewBufferString(`{"pricing":{}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPathValues(echo.PathValues{{Name: "selector", Value: "openai/gpt-4o"}})

	if err := h.UpsertModelPricingOverride(c); err != nil {
		t.Fatalf("UpsertModelPricingOverride() error = %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
