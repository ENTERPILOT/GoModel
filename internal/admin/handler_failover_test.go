package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"

	"gomodel/config"
	"gomodel/internal/core"
	failoverrules "gomodel/internal/failover"
	"gomodel/internal/providers"
)

type failoverHandlerTestStore struct {
	rows map[string]failoverrules.Rule
}

func newFailoverHandlerTestStore(rows ...failoverrules.Rule) *failoverHandlerTestStore {
	store := &failoverHandlerTestStore{rows: make(map[string]failoverrules.Rule, len(rows))}
	for _, row := range rows {
		store.rows[row.Source] = row
	}
	return store
}

func (s *failoverHandlerTestStore) List(context.Context) ([]failoverrules.Rule, error) {
	rows := make([]failoverrules.Rule, 0, len(s.rows))
	for _, row := range s.rows {
		rows = append(rows, row)
	}
	return rows, nil
}

func (s *failoverHandlerTestStore) Get(_ context.Context, source string) (*failoverrules.Rule, error) {
	row, ok := s.rows[source]
	if !ok {
		return nil, failoverrules.ErrNotFound
	}
	return &row, nil
}

func (s *failoverHandlerTestStore) Upsert(_ context.Context, rule failoverrules.Rule) error {
	s.rows[rule.Source] = rule
	return nil
}

func (s *failoverHandlerTestStore) Delete(_ context.Context, source string) error {
	if _, ok := s.rows[source]; !ok {
		return failoverrules.ErrNotFound
	}
	delete(s.rows, source)
	return nil
}

func (s *failoverHandlerTestStore) DeleteAll(context.Context) error {
	s.rows = make(map[string]failoverrules.Rule)
	return nil
}

func (s *failoverHandlerTestStore) Close() error { return nil }

func newFailoverHandlerTestService(t *testing.T, store *failoverHandlerTestStore) *failoverrules.Service {
	t.Helper()
	service, err := failoverrules.NewService(store, config.FallbackConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	return service
}

func TestFailoverEndpointsReturn503WhenRuntimeFlagDisabled(t *testing.T) {
	store := newFailoverHandlerTestStore(failoverrules.Rule{
		Source:  "openai/gpt-4o",
		Targets: []string{"anthropic/claude-3-5-sonnet"},
		Enabled: true,
	})
	service := newFailoverHandlerTestService(t, store)
	h := NewHandler(
		nil,
		nil,
		WithFailover(service),
		WithDashboardRuntimeConfig(DashboardConfigResponse{FailoverEnabled: "off"}),
	)
	e := echo.New()
	h.RegisterRoutes(e.Group("/admin"))

	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{name: "list", method: http.MethodGet, path: "/admin/failover"},
		{name: "upsert", method: http.MethodPut, path: "/admin/failover", body: `{"primary_model":"openai/gpt-4.1","fallback_models":["anthropic/claude-3-haiku"],"enabled":true}`},
		{name: "delete", method: http.MethodDelete, path: "/admin/failover", body: `{"primary_model":"openai/gpt-4o"}`},
		{name: "reset", method: http.MethodPost, path: "/admin/failover/reset"},
		{name: "generate", method: http.MethodPost, path: "/admin/failover/generate"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, bytes.NewBufferString(tt.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			e.ServeHTTP(rec, req)

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503 body=%s", rec.Code, rec.Body.String())
			}
		})
	}

	if _, ok := store.rows["openai/gpt-4.1"]; ok {
		t.Fatal("disabled failover upsert mutated the store")
	}
	if _, ok := store.rows["openai/gpt-4o"]; !ok {
		t.Fatal("disabled failover delete/reset mutated the store")
	}
}

func TestGenerateFailoverRulesFiltersByPrimaryModel(t *testing.T) {
	registry := providers.NewModelRegistry()
	registry.RegisterProviderWithNameAndType(&handlerMockProvider{
		models: &core.ModelsResponse{Data: []core.Model{
			failoverSuggestionTestModel("gpt-4o", 1287),
			failoverSuggestionTestModel("gpt-4.1", 1294),
		}},
	}, "openai", "openai")
	registry.RegisterProviderWithNameAndType(&handlerMockProvider{
		models: &core.ModelsResponse{Data: []core.Model{
			failoverSuggestionTestModel("claude-3-5-sonnet", 1289),
		}},
	}, "anthropic", "anthropic")
	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	service := newFailoverHandlerTestService(t, newFailoverHandlerTestStore())
	h := NewHandler(
		nil,
		registry,
		WithFailover(service),
		WithDashboardRuntimeConfig(DashboardConfigResponse{FailoverEnabled: "on"}),
	)
	e := echo.New()
	h.RegisterRoutes(e.Group("/admin"))

	req := httptest.NewRequest(http.MethodPost, "/admin/failover/generate", bytes.NewBufferString(`{"primary_model":"openai/gpt-4o"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var body []failoverrules.View
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body) != 1 {
		t.Fatalf("len(body) = %d, want 1: %+v", len(body), body)
	}
	if body[0].Source != "openai/gpt-4o" {
		t.Fatalf("Source = %q, want openai/gpt-4o", body[0].Source)
	}
	if len(body[0].Targets) == 0 {
		t.Fatalf("Targets empty, want generated fallback suggestions")
	}
}

func TestUpsertAndDeleteFailoverRuleLifecycle(t *testing.T) {
	store := newFailoverHandlerTestStore()
	service := newFailoverHandlerTestService(t, store)
	h := NewHandler(
		nil,
		nil,
		WithFailover(service),
		WithDashboardRuntimeConfig(DashboardConfigResponse{FailoverEnabled: "on"}),
	)
	e := echo.New()
	h.RegisterRoutes(e.Group("/admin"))

	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	// Creating a rule returns 200 with the persisted view.
	rec := do(http.MethodPut, "/admin/failover", `{"primary_model":"openai/gpt-4.1","fallback_models":["anthropic/claude-3-haiku"],"enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("upsert status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var view failoverrules.View
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode upsert response: %v", err)
	}
	if view.Source != "openai/gpt-4.1" {
		t.Fatalf("view.Source = %q, want openai/gpt-4.1", view.Source)
	}
	if _, ok := store.rows["openai/gpt-4.1"]; !ok {
		t.Fatal("upsert did not persist the rule")
	}

	// Deleting an existing rule returns 204 and removes it.
	rec = do(http.MethodDelete, "/admin/failover", `{"primary_model":"openai/gpt-4.1"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204 body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := store.rows["openai/gpt-4.1"]; ok {
		t.Fatal("delete did not remove the rule")
	}

	// Deleting a missing rule maps ErrNotFound to 404.
	rec = do(http.MethodDelete, "/admin/failover", `{"primary_model":"openai/gpt-4.1"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete-missing status = %d, want 404 body=%s", rec.Code, rec.Body.String())
	}
}

func TestGenerateFailoverRulesRejectsInvalidBody(t *testing.T) {
	// The registry only needs to be non-nil: an invalid body is rejected before
	// any model resolution runs, so no provider initialization is required.
	registry := providers.NewModelRegistry()
	service := newFailoverHandlerTestService(t, newFailoverHandlerTestStore())
	h := NewHandler(
		nil,
		registry,
		WithFailover(service),
		WithDashboardRuntimeConfig(DashboardConfigResponse{FailoverEnabled: "on"}),
	)
	e := echo.New()
	h.RegisterRoutes(e.Group("/admin"))

	req := httptest.NewRequest(http.MethodPost, "/admin/failover/generate", bytes.NewBufferString(`{not-json`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func failoverSuggestionTestModel(id string, elo float64) core.Model {
	return core.Model{
		ID: id,
		Metadata: &core.ModelMetadata{
			Categories: []core.ModelCategory{core.CategoryTextGeneration},
			Rankings: map[string]core.ModelRanking{
				"chatbot_arena": {Elo: &elo},
			},
		},
	}
}
