package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/modelselectors"
	"github.com/enterpilot/gomodel/internal/pricingoverrides"
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

type modelPricingOverrideTestCatalog struct {
	providerNames []string
}

func (c modelPricingOverrideTestCatalog) ProviderNames() []string {
	return append([]string(nil), c.providerNames...)
}

func newModelPricingOverrideService(t *testing.T, store pricingoverrides.Store, providerNames ...string) *pricingoverrides.Service {
	t.Helper()
	if len(providerNames) == 0 {
		providerNames = []string{"openai"}
	}
	service, err := pricingoverrides.NewService(store, modelPricingOverrideTestCatalog{providerNames: providerNames}, nil)
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	return service
}

func TestModelPricingOverrideLifecycle(t *testing.T) {
	tests := []struct {
		name         string
		providers    []string
		selector     string
		price        float64
		wantProvider string
		wantModel    string
		wantScope    modelselectors.ScopeKind
	}{
		{
			name:         "simple provider model selector",
			providers:    []string{"openai"},
			selector:     "openai/gpt-4o",
			price:        1.25,
			wantProvider: "openai",
			wantModel:    "gpt-4o",
			wantScope:    modelselectors.ScopeProviderModel,
		},
		{
			name:         "provider model selector with slash-shaped model id",
			providers:    []string{"openrouter"},
			selector:     "openrouter/meta-llama/llama-3.1-8b-instruct",
			price:        0.18,
			wantProvider: "openrouter",
			wantModel:    "meta-llama/llama-3.1-8b-instruct",
			wantScope:    modelselectors.ScopeProviderModel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := newModelPricingOverrideService(t, newModelPricingOverrideTestStore(), tt.providers...)
			h := NewHandler(nil, nil, WithPricingOverrides(service))
			e := echo.New()
			h.RegisterRoutes(e.Group("/admin"))

			serve := func(method, body string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, "/admin/model-pricing-overrides", strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				return rec
			}

			putRec := serve(http.MethodPut, `{"selector":"`+tt.selector+`","pricing":{"input_per_mtok":`+strconv.FormatFloat(tt.price, 'f', -1, 64)+`}}`)
			require.Equal(t, http.StatusOK, putRec.Code, putRec.Body.String())
			assertPricingOverrideView(t, echotest.Decode[pricingoverrides.View](t, putRec), tt.selector, tt.wantProvider, tt.wantModel, tt.wantScope, tt.price)

			listRec := serve(http.MethodGet, "")
			require.Equal(t, http.StatusOK, listRec.Code)
			listBody := echotest.Decode[[]pricingoverrides.View](t, listRec)
			require.Len(t, listBody, 1)
			assertPricingOverrideView(t, listBody[0], tt.selector, tt.wantProvider, tt.wantModel, tt.wantScope, tt.price)

			deleteRec := serve(http.MethodDelete, `{"selector":"`+tt.selector+`"}`)
			require.Equal(t, http.StatusNoContent, deleteRec.Code)
		})
	}
}

func assertPricingOverrideView(t *testing.T, view pricingoverrides.View, selector, provider, model string, scope modelselectors.ScopeKind, price float64) {
	t.Helper()
	assert.Equal(t, selector, view.Selector)
	assert.Equal(t, provider, view.ProviderName)
	assert.Equal(t, model, view.Model)
	assert.Equal(t, scope, view.ScopeKind)
	require.NotNil(t, view.Pricing.InputPerMtok)
	assert.InDelta(t, price, *view.Pricing.InputPerMtok, 0)
}

func TestUpsertModelPricingOverrideReturnsBadRequestForValidationErrors(t *testing.T) {
	service := newModelPricingOverrideService(t, newModelPricingOverrideTestStore())
	h := NewHandler(nil, nil, WithPricingOverrides(service))
	c, rec := echotest.Request(t, http.MethodPut, "/admin/model-pricing-overrides", `{"selector":"openai/gpt-4o","pricing":{}}`)
	err := h.UpsertModelPricingOverride(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
