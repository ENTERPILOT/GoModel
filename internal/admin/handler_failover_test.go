package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"

	"gomodel/config"
	failoverrules "gomodel/internal/failover"
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
