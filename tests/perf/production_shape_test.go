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
	"github.com/enterpilot/gomodel/internal/live"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/ratelimit"
	"github.com/enterpilot/gomodel/internal/server"
	"github.com/enterpilot/gomodel/internal/session"
	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/stretchr/testify/require"
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
	require.NoError(tb, err)

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
	err := registry.Initialize(context.Background())
	require.NoError(tb, err)

	router, err := providers.NewRouter(registry)
	require.NoError(tb, err)

	return router
}

// benchLiveAuditLogger is the bench audit logger with the live broker a
// dashboard subscribes to. The audit middleware publishes lifecycle events
// through whichever logger implements auditlog.LiveEventEmitter, so this is
// what a default deployment pays on every request — the broker runs whether or
// not anyone is watching, which is the case the benchmark measures.
type benchLiveAuditLogger struct {
	cfg    auditlog.Config
	broker *live.Broker
}

func (l benchLiveAuditLogger) Write(entry *auditlog.LogEntry) {
	// The real logger publishes the terminal event as it persists the entry.
	l.broker.PublishAuditEvent(auditlog.LiveEventAuditFlushed, entry)
}

func (l benchLiveAuditLogger) Config() auditlog.Config { return l.cfg }
func (l benchLiveAuditLogger) Close() error            { return nil }

func (l benchLiveAuditLogger) PublishLiveEvent(eventType string, entry *auditlog.LogEntry) {
	l.broker.PublishAuditEvent(eventType, entry)
}

// BenchmarkGatewayHotPathProductionShapeLiveBroker is the production shape with
// live logs enabled and no dashboard connected — the default configuration of a
// running gateway. The broker builds and retains an event for every lifecycle
// step of every request, which the stub audit logger in the other benchmarks
// never exercises.
func BenchmarkGatewayHotPathProductionShapeLiveBroker(b *testing.B) {
	srv := newLiveBrokerBenchServer(b, routedCatalogSize)
	body := []byte(sampleChatRequest)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer bench-master-key")

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		require.Equal(b, http.StatusOK, rec.Code, "body = %s", rec.Body.String())
	}
}

func newLiveBrokerBenchServer(tb testing.TB, modelCount int) *server.Server {
	tb.Helper()

	broker := live.NewBroker(live.Config{Enabled: true})
	tb.Cleanup(broker.Close)

	return server.New(newBenchRouter(tb, modelCount), &server.Config{
		LogOnlyModelInteractions: true,
		MasterKey:                "bench-master-key",
		AuditLogger: benchLiveAuditLogger{
			cfg:    auditlog.Config{Enabled: true, LogBodies: true, LogHeaders: true},
			broker: broker,
		},
		UsageLogger:     benchUsageLogger{cfg: usage.Config{Enabled: true}},
		SessionDetector: session.NewDetector(session.BuiltinRules(), true),
		RateLimiter:     newBenchRateLimiter(tb),
	})
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
