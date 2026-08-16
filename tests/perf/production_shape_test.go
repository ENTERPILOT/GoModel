package perf

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/ratelimit"
	"github.com/enterpilot/gomodel/internal/server"
	"github.com/enterpilot/gomodel/internal/session"
	"github.com/enterpilot/gomodel/internal/usage"
)

// benchRateLimitStore is a minimal in-memory ratelimit.Store carrying one
// non-blocking rule, so the benchmark exercises the real per-request window
// accounting a deployment with any configured rate limit pays.
type benchRateLimitStore struct {
	rules []ratelimit.Rule
}

func (s *benchRateLimitStore) ListRules(context.Context) ([]ratelimit.Rule, error) {
	return append([]ratelimit.Rule(nil), s.rules...), nil
}
func (s *benchRateLimitStore) UpsertRules(_ context.Context, rules []ratelimit.Rule) error {
	s.rules = append(s.rules, rules...)
	return nil
}
func (s *benchRateLimitStore) DeleteRule(context.Context, ratelimit.RuleScope, string, int64) error {
	return nil
}
func (s *benchRateLimitStore) ReplaceConfigRules(context.Context, []ratelimit.Rule) error {
	return nil
}
func (s *benchRateLimitStore) LoadCounters(context.Context) ([]ratelimit.WindowSnapshot, error) {
	return nil, nil
}
func (s *benchRateLimitStore) SaveCounters(context.Context, []ratelimit.WindowSnapshot) error {
	return nil
}
func (s *benchRateLimitStore) DeleteCounter(context.Context, ratelimit.RuleScope, string, int64) error {
	return nil
}
func (s *benchRateLimitStore) DeleteAllCounters(context.Context) error { return nil }
func (s *benchRateLimitStore) Close() error                            { return nil }

// newBenchRateLimiter builds a real ratelimit.Service with one high user-path
// request limit that matches every request but never rejects.
func newBenchRateLimiter(tb testing.TB) *ratelimit.Service {
	tb.Helper()

	maxRequests := int64(1 << 40)
	store := &benchRateLimitStore{rules: []ratelimit.Rule{{
		Scope:         ratelimit.ScopeUserPath,
		Subject:       "/",
		PeriodSeconds: 60,
		MaxRequests:   &maxRequests,
	}}}
	service, err := ratelimit.NewService(context.Background(), store)
	if err != nil {
		tb.Fatalf("new rate limit service: %v", err)
	}
	tb.Cleanup(service.Close)
	return service
}

// BenchmarkGatewayHotPathProductionShape wires the middleware chain the way a
// default deployment actually runs it: master-key auth, audit logging, usage
// tracking, session keeping, and a configured rate limit all enabled. The
// original guard benchmarks leave every one of those nil, so they measure a
// configuration nobody deploys — and therefore cannot see regressions in any
// feature added since the guard was written. TestHotPathPerfGuard enforces
// allocation ceilings on this benchmark too.
func BenchmarkGatewayHotPathProductionShape(b *testing.B) {
	srv := newProductionBenchServer(b, routedCatalogSize)
	body := []byte(sampleChatRequest)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer bench-master-key")

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			b.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
		}
	}
}

func newBenchRouter(tb testing.TB, modelCount int) *providers.Router {
	tb.Helper()

	models := make([]core.Model, 0, modelCount)
	models = append(models, core.Model{ID: "gpt-4o-mini", Object: "model", OwnedBy: "mock", Created: 1700000000})
	for i := 1; i < modelCount; i++ {
		models = append(models, core.Model{
			ID:      fmt.Sprintf("filler-model-%04d", i),
			Object:  "model",
			OwnedBy: "mock",
			Created: 1700000000,
		})
	}

	registry := providers.NewModelRegistry()
	registry.RegisterProviderWithNameAndType(&benchProvider{models: models}, "mock", "mock")
	if err := registry.Initialize(context.Background()); err != nil {
		tb.Fatalf("registry initialize: %v", err)
	}

	router, err := providers.NewRouter(registry)
	if err != nil {
		tb.Fatalf("new router: %v", err)
	}
	return router
}

func newProductionBenchServer(tb testing.TB, modelCount int) *server.Server {
	tb.Helper()

	return server.New(newBenchRouter(tb, modelCount), &server.Config{
		LogOnlyModelInteractions: true,
		MasterKey:                "bench-master-key",
		AuditLogger:              benchAuditLogger{cfg: auditlog.Config{Enabled: true, LogBodies: true, LogHeaders: true}},
		UsageLogger:              benchUsageLogger{cfg: usage.Config{Enabled: true}},
		SessionDetector:          session.NewDetector(session.BuiltinRules(), true),
		RateLimiter:              newBenchRateLimiter(tb),
	})
}
