package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/authkeys"
	"github.com/enterpilot/gomodel/internal/observability"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/enterpilot/gomodel/internal/usagemetrics"
)

// The metrics tap sits inside the markup tap, so an entry written through the
// application's usage logger reaches the counters with its billed cost.
func TestNew_UsageLoggerFeedsTokenAndCostMetrics(t *testing.T) {
	tests := []struct {
		name           string
		metricsEnabled string
		callerUserPath string
		wantUserPath   string
		wantTokens     float64
		wantCost       float64
		wantBilled     float64
	}{
		{name: "metrics disabled", metricsEnabled: "false", callerUserPath: "true"},
		{name: "metrics enabled", metricsEnabled: "true", callerUserPath: "false", wantTokens: 10, wantCost: 0.2, wantBilled: 0.3},
		{name: "metrics enabled with a caller dimension", metricsEnabled: "true", callerUserPath: "true", wantUserPath: "/team/alice", wantTokens: 10, wantCost: 0.2, wantBilled: 0.3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reset := func() {
				observability.ResetMetrics()
				observability.SetRequestCallers(nil)
				usagemetrics.ResetCallerValues()
			}
			reset()
			t.Cleanup(reset)

			dir := t.TempDir()
			t.Chdir(dir)
			t.Setenv("STORAGE_TYPE", "sqlite")
			t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "usage-metrics.db"))
			t.Setenv("ADMIN_UI_ENABLED", "false")
			t.Setenv("ADMIN_ENDPOINTS_ENABLED", "false")
			t.Setenv("MCP_ENABLED", "false")
			t.Setenv("OTEL_ENABLED", "false")
			t.Setenv("PRICING_MARKUPS_ENABLED", "")
			t.Setenv("METRICS_ENABLED", tt.metricsEnabled)
			t.Setenv("METRICS_CALLER_USER_PATH", tt.callerUserPath)
			yaml := "pricing:\n  markups_enabled: true\n  markups:\n    \"/\": 50\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o600))
			loaded, err := config.Load()
			require.NoError(t, err)

			ctx := context.Background()
			application, err := New(ctx, Config{AppConfig: loaded, Factory: providers.NewProviderFactory()})
			require.NoError(t, err)
			t.Cleanup(func() {
				assert.NoError(t, application.Shutdown(ctx))
			})

			application.UsageLogger().Write(&usage.UsageEntry{
				ID: "entry-1", Provider: "openai", Model: "gpt-5", UserPath: "/team/alice/laptop",
				InputTokens: 10, OutputTokens: 5, TotalTokens: 15, TotalCost: new(0.2),
			})

			tokens := testutil.ToFloat64(observability.TokensTotal.WithLabelValues("openai", "gpt-5", observability.TokenTypeInput, tt.wantUserPath, "", ""))
			providerCost := testutil.ToFloat64(observability.ProviderCostTotal.WithLabelValues("openai", "gpt-5", tt.wantUserPath, "", ""))
			billedCost := testutil.ToFloat64(observability.BilledCostTotal.WithLabelValues("openai", "gpt-5", tt.wantUserPath, "", ""))
			assert.InDelta(t, tt.wantTokens, tokens, 0)
			assert.InDelta(t, tt.wantCost, providerCost, 1e-12)
			assert.InDelta(t, tt.wantBilled, billedCost, 1e-12, "provider cost plus the 50% markup")
		})
	}
}

// The auth key dimension reports the key's name, resolved from the managed
// key service that is built after the usage logger is wrapped.
func TestNew_UsageMetricsReportAuthKeyByName(t *testing.T) {
	reset := func() {
		observability.ResetMetrics()
		observability.SetRequestCallers(nil)
		usagemetrics.ResetCallerValues()
	}
	reset()
	t.Cleanup(reset)

	t.Chdir(t.TempDir())
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "usage-metrics.db"))
	t.Setenv("ADMIN_UI_ENABLED", "false")
	t.Setenv("ADMIN_ENDPOINTS_ENABLED", "false")
	t.Setenv("MCP_ENABLED", "false")
	t.Setenv("OTEL_ENABLED", "false")
	t.Setenv("METRICS_ENABLED", "true")
	t.Setenv("METRICS_CALLER_AUTH_KEY", "true")
	loaded, err := config.Load()
	require.NoError(t, err)

	ctx := context.Background()
	application, err := New(ctx, Config{AppConfig: loaded, Factory: providers.NewProviderFactory()})
	require.NoError(t, err)
	t.Cleanup(func() {
		assert.NoError(t, application.Shutdown(ctx))
	})
	issued, err := application.authKeys.Service.Create(ctx, authkeys.CreateInput{Name: "ci-pipeline"})
	require.NoError(t, err)

	logger := application.UsageLogger()
	logger.Write(&usage.UsageEntry{ID: "entry-1", Provider: "openai", Model: "gpt-5", AuthKeyID: issued.ID, OutputTokens: 5})
	logger.Write(&usage.UsageEntry{ID: "entry-2", Provider: "openai", Model: "gpt-5", AuthKeyID: "no-such-key", OutputTokens: 7})
	logger.Write(&usage.UsageEntry{ID: "entry-3", Provider: "openai", Model: "gpt-5", OutputTokens: 11})

	output := func(authKey string) float64 {
		return testutil.ToFloat64(observability.TokensTotal.WithLabelValues("openai", "gpt-5", observability.TokenTypeOutput, "", authKey, ""))
	}
	assert.InDelta(t, 5, output("ci-pipeline"), 0)
	assert.InDelta(t, 7, output(usagemetrics.UnknownAuthKey), 0)
	assert.InDelta(t, 11, output(""), 0, "the master key and unauthenticated traffic have no key")
	assert.Zero(t, output(issued.ID), "the key ID is never a label value")
}

// The per-caller request counter is process-wide. A reload rejected after the
// usage logger was wrapped (here by a guardrail of an unknown type) must leave
// the serving generation's caller resolver in place.
func TestNew_RejectedReloadKeepsRequestCallers(t *testing.T) {
	serving := usagemetrics.NewCallers(config.MetricsCallerDimensionsConfig{RequestLabels: true}, nil)
	require.NotNil(t, serving)
	observability.SetRequestCallers(serving)
	t.Cleanup(func() { observability.SetRequestCallers(nil) })

	t.Chdir(t.TempDir())
	t.Setenv("STORAGE_TYPE", "sqlite")
	t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "usage-metrics.db"))
	t.Setenv("ADMIN_UI_ENABLED", "false")
	t.Setenv("ADMIN_ENDPOINTS_ENABLED", "false")
	t.Setenv("MCP_ENABLED", "false")
	t.Setenv("OTEL_ENABLED", "false")
	t.Setenv("METRICS_ENABLED", "true")
	t.Setenv("METRICS_CALLER_AUTH_KEY", "true")
	loaded, err := config.Load()
	require.NoError(t, err)
	loaded.Config.Guardrails = config.GuardrailsConfig{
		Enabled: true,
		Rules:   []config.GuardrailRuleConfig{{Name: "broken", Type: "no-such-guardrail"}},
	}

	_, err = New(context.Background(), Config{AppConfig: loaded, Factory: providers.NewProviderFactory()})

	require.ErrorContains(t, err, "guardrail")
	assert.Same(t, serving, observability.RequestCallers(), "the rejected generation's caller resolver leaked")
}

// A response-cache hit goes through the same chain: the markup tap prices it,
// and the metrics tap counts only its billed cost, never tokens or provider
// cost, since no provider was called.
func TestNew_ResponseCacheHitFeedsOnlyBilledCost(t *testing.T) {
	tests := []struct {
		name          string
		billCacheHits string
		wantBilled    float64
	}{
		{name: "billed cache hits", billCacheHits: "true", wantBilled: 0.3},
		{name: "free cache hits", billCacheHits: "false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reset := func() {
				observability.ResetMetrics()
				observability.SetRequestCallers(nil)
				usagemetrics.ResetCallerValues()
			}
			reset()
			t.Cleanup(reset)

			dir := t.TempDir()
			t.Chdir(dir)
			t.Setenv("STORAGE_TYPE", "sqlite")
			t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "usage-metrics.db"))
			t.Setenv("ADMIN_UI_ENABLED", "false")
			t.Setenv("ADMIN_ENDPOINTS_ENABLED", "false")
			t.Setenv("MCP_ENABLED", "false")
			t.Setenv("OTEL_ENABLED", "false")
			t.Setenv("PRICING_MARKUPS_ENABLED", "")
			t.Setenv("PRICING_BILL_CACHE_HITS", tt.billCacheHits)
			t.Setenv("METRICS_ENABLED", "true")
			yaml := "pricing:\n  markups_enabled: true\n  markups:\n    \"/\": 50\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o600))
			loaded, err := config.Load()
			require.NoError(t, err)

			ctx := context.Background()
			application, err := New(ctx, Config{AppConfig: loaded, Factory: providers.NewProviderFactory()})
			require.NoError(t, err)
			t.Cleanup(func() {
				assert.NoError(t, application.Shutdown(ctx))
			})

			application.UsageLogger().Write(&usage.UsageEntry{
				ID: "hit-1", Provider: "openai", Model: "gpt-5", CacheType: usage.CacheTypeExact,
				InputTokens: 10, OutputTokens: 5, TotalTokens: 15, TotalCost: new(0.2),
			})

			for _, tokenType := range []string{observability.TokenTypeInput, observability.TokenTypeOutput} {
				assert.Zero(t, testutil.ToFloat64(observability.TokensTotal.WithLabelValues("openai", "gpt-5", tokenType, "", "", "")))
			}
			assert.Zero(t, testutil.ToFloat64(observability.ProviderCostTotal.WithLabelValues("openai", "gpt-5", "", "", "")))
			assert.InDelta(t, tt.wantBilled, testutil.ToFloat64(observability.BilledCostTotal.WithLabelValues("openai", "gpt-5", "", "", "")), 1e-12)
		})
	}
}
