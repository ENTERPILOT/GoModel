package perf

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/server"
	"github.com/enterpilot/gomodel/internal/session"
	"github.com/enterpilot/gomodel/internal/usage"
)

// ablation isolates the per-subsystem cost of the default-on middleware stack.
// Each variant turns exactly one subsystem off relative to the full
// production shape, so the delta attributes cost to that subsystem.
func benchAblation(b *testing.B, mutate func(*server.Config)) {
	cfg := &server.Config{
		LogOnlyModelInteractions: true,
		MasterKey:                "bench-master-key",
		AuditLogger:              benchAuditLogger{cfg: auditlog.Config{Enabled: true, LogBodies: true, LogHeaders: true}},
		UsageLogger:              benchUsageLogger{cfg: usage.Config{Enabled: true}},
		SessionDetector:          session.NewDetector(session.BuiltinRules(), true),
		RateLimiter:              newBenchRateLimiter(b),
	}
	mutate(cfg)

	srv := server.New(newBenchRouter(b, routedCatalogSize), cfg)
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

func BenchmarkAblationFull(b *testing.B) {
	benchAblation(b, func(*server.Config) {})
}

// No content auto-detection: header/body rules still run, the sha256 conversation
// anchor does not.
func BenchmarkAblationNoAutoDetect(b *testing.B) {
	benchAblation(b, func(c *server.Config) {
		c.SessionDetector = session.NewDetector(session.BuiltinRules(), false)
	})
}

func BenchmarkAblationNoSessionKeeping(b *testing.B) {
	benchAblation(b, func(c *server.Config) { c.SessionDetector = nil })
}

func BenchmarkAblationNoAuditBodies(b *testing.B) {
	benchAblation(b, func(c *server.Config) {
		c.AuditLogger = benchAuditLogger{cfg: auditlog.Config{Enabled: true, LogHeaders: true}}
	})
}

func BenchmarkAblationNoAudit(b *testing.B) {
	benchAblation(b, func(c *server.Config) { c.AuditLogger = nil })
}

func BenchmarkAblationNoAuth(b *testing.B) {
	benchAblation(b, func(c *server.Config) { c.MasterKey = "" })
}

func BenchmarkAblationNoUsage(b *testing.B) {
	benchAblation(b, func(c *server.Config) { c.UsageLogger = nil })
}

func BenchmarkAblationNoRateLimit(b *testing.B) {
	benchAblation(b, func(c *server.Config) { c.RateLimiter = nil })
}
