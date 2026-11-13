package observability

import (
	"context"
	"net/http"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/enterpilot/gomodel/internal/usagemetrics"
)

type discardLogger struct{ entries int }

func (l *discardLogger) Write(*usage.UsageEntry) { l.entries++ }
func (l *discardLogger) Config() usage.Config    { return usage.Config{Enabled: true} }
func (l *discardLogger) Close() error            { return nil }

// markupLogger stands in for the markup tap, which wraps the metrics tap and
// stamps the billed cost first.
type markupLogger struct {
	usage.LoggerInterface
	factor float64
}

func (l markupLogger) Write(entry *usage.UsageEntry) {
	if entry.TotalCost != nil {
		entry.BilledCost = new(*entry.TotalCost * l.factor)
	}
	l.LoggerInterface.Write(entry)
}

type fixedPricing struct{ pricing core.ModelPricing }

func (p fixedPricing) ResolvePricing(string, string) *core.ModelPricing { return &p.pricing }

// testPricing charges $2 per million input tokens and $10 per million output
// tokens, so 1000 input and 500 output tokens cost $0.007.
var testPricing = core.ModelPricing{InputPerMtok: new(2.0), OutputPerMtok: new(10.0)}

func resetUsageMetrics(t *testing.T) {
	t.Helper()
	reset := func() {
		ResetMetrics()
		SetRequestCallers(nil)
		usagemetrics.ResetCallerValues()
	}
	reset()
	t.Cleanup(reset)
}

func usageSeriesCount() int {
	count := 0
	for _, c := range []prometheus.Collector{TokensTotal, TokenDetailsTotal, ProviderCostTotal, BilledCostTotal, CallerRequestsTotal} {
		count += testutil.CollectAndCount(c)
	}
	return count
}

// chatUsage is 1000 input tokens (400 of them cached) and 500 output tokens
// (100 of them reasoning).
func chatUsage() core.Usage {
	return core.Usage{
		PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500,
		PromptTokensDetails:     &core.PromptTokensDetails{CachedTokens: 400},
		CompletionTokensDetails: &core.CompletionTokensDetails{ReasoningTokens: 100},
	}
}

func assertChatUsageRecorded(t *testing.T, provider, model string, caller usagemetrics.Caller, billed float64) {
	t.Helper()
	tokens := func(vec *prometheus.CounterVec, tokenType string) float64 {
		return testutil.ToFloat64(vec.WithLabelValues(provider, model, tokenType, caller.UserPath, caller.AuthKey, caller.RequestLabels))
	}
	assert.InDelta(t, 1000, tokens(TokensTotal, TokenTypeInput), 0)
	assert.InDelta(t, 500, tokens(TokensTotal, TokenTypeOutput), 0)
	assert.InDelta(t, 400, tokens(TokenDetailsTotal, TokenTypeInputCached), 0)
	assert.InDelta(t, 100, tokens(TokenDetailsTotal, TokenTypeOutputReasoning), 0)
	assert.Equal(t, 2, testutil.CollectAndCount(TokensTotal))
	assert.Equal(t, 2, testutil.CollectAndCount(TokenDetailsTotal), "no cache-write series without cache writes")

	cost := func(vec *prometheus.CounterVec) float64 {
		return testutil.ToFloat64(vec.WithLabelValues(provider, model, caller.UserPath, caller.AuthKey, caller.RequestLabels))
	}
	assert.InDelta(t, 0.007, cost(ProviderCostTotal), 1e-12)
	assert.InDelta(t, billed, cost(BilledCostTotal), 1e-12)
}

func TestUsageMetricsNonStreaming(t *testing.T) {
	resetUsageMetrics(t)
	inner := &discardLogger{}
	logger := markupLogger{LoggerInterface: usagemetrics.NewTap(inner, nil, UsageRecorder{}), factor: 1.5}

	entry := usage.ExtractFromChatResponse(&core.ChatResponse{ID: "chatcmpl-1", Model: "gpt-5", Usage: chatUsage()},
		"req-1", "openai", "/v1/chat/completions", &testPricing)
	entry.ProviderName = "openai-eu"
	entry.UserPath, entry.AuthKeyID, entry.Labels = "/team/alice", "key-1", []string{"prod"}
	logger.Write(entry)

	assertChatUsageRecorded(t, "openai-eu", "gpt-5", usagemetrics.Caller{}, 0.0105)
	assert.Equal(t, 1, inner.entries)
}

func TestUsageMetricsStreaming(t *testing.T) {
	resetUsageMetrics(t)
	callers := usagemetrics.NewCallers(
		config.MetricsCallerDimensionsConfig{UserPath: true, UserPathDepth: 2, AuthKey: true, RequestLabels: true},
		func(id string) string { return map[string]string{"key-1": "ci-pipeline"}[id] },
	)
	logger := markupLogger{LoggerInterface: usagemetrics.NewTap(&discardLogger{}, callers, UsageRecorder{}), factor: 1.5}

	observer := usage.NewStreamUsageObserver(logger, "gpt-5", "openai", "req-1", "/v1/chat/completions", fixedPricing{testPricing}, "/team/alice/laptop")
	observer.SetProviderName("openai-eu")
	observer.SetAuthKeyID("key-1")
	observer.SetLabels([]string{"team-a", "prod"})
	observer.OnJSONEvent(map[string]any{
		"id":    "chatcmpl-1",
		"model": "gpt-5",
		"usage": map[string]any{
			"prompt_tokens": float64(1000), "completion_tokens": float64(500), "total_tokens": float64(1500),
			"prompt_tokens_details":     map[string]any{"cached_tokens": float64(400)},
			"completion_tokens_details": map[string]any{"reasoning_tokens": float64(100)},
		},
	})
	require.Zero(t, usageSeriesCount(), "a stream is counted once, when it closes")
	observer.OnStreamClose()

	assertChatUsageRecorded(t, "openai-eu", "gpt-5",
		usagemetrics.Caller{UserPath: "/team/alice", AuthKey: "ci-pipeline", RequestLabels: "prod,team-a"}, 0.0105)
}

func TestUsageRecorderKeepsProviderAndBilledCostApart(t *testing.T) {
	tests := []struct {
		name         string
		sample       usagemetrics.Sample
		wantProvider float64
		wantBilled   float64
	}{
		{name: "markups off records no billed cost", sample: usagemetrics.Sample{ProviderCost: 0.2}, wantProvider: 0.2},
		{name: "markup", sample: usagemetrics.Sample{ProviderCost: 0.2, BilledCost: 0.25}, wantProvider: 0.2, wantBilled: 0.25},
		{name: "billed response-cache hit", sample: usagemetrics.Sample{BilledCost: 0.25}, wantBilled: 0.25},
		{name: "free or unknown cost", sample: usagemetrics.Sample{InputTokens: 5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetUsageMetrics(t)
			tt.sample.Provider, tt.sample.Model = "openai", "gpt-5"

			UsageRecorder{}.RecordUsage(tt.sample)

			assert.InDelta(t, tt.wantProvider, testutil.ToFloat64(ProviderCostTotal.WithLabelValues("openai", "gpt-5", "", "", "")), 1e-12)
			assert.InDelta(t, tt.wantBilled, testutil.ToFloat64(BilledCostTotal.WithLabelValues("openai", "gpt-5", "", "", "")), 1e-12)
		})
	}
}

func TestUsageRecorderSkipsZeroValues(t *testing.T) {
	resetUsageMetrics(t)

	UsageRecorder{}.RecordUsage(usagemetrics.Sample{Provider: "openai", Model: "gpt-5"})
	UsageRecorder{}.RecordUsage(usagemetrics.Sample{Provider: "openai", Model: "gpt-5", ProviderCost: -1, BilledCost: -1})

	assert.Zero(t, usageSeriesCount())
}

func TestUsageMetricsStayEmptyWithoutARecorder(t *testing.T) {
	resetUsageMetrics(t)
	inner := &discardLogger{}
	logger := usagemetrics.NewTap(inner, nil)
	require.Same(t, inner, logger, "with Prometheus and OpenTelemetry off the logger is not wrapped")

	logger.Write(&usage.UsageEntry{Provider: "openai", Model: "gpt-5", InputTokens: 10, OutputTokens: 5, TotalCost: new(0.1)})
	hooks := NewPrometheusHooks()
	call := llmclient.RequestInfo{Provider: "openai", Model: "gpt-5", Endpoint: "/chat/completions"}
	hooks.OnRequestEnd(hooks.OnRequestStart(context.Background(), call),
		llmclient.ResponseInfo{Provider: "openai", Model: "gpt-5", Endpoint: "/chat/completions", StatusCode: http.StatusOK})

	assert.Zero(t, usageSeriesCount())
	assert.Equal(t, 1, testutil.CollectAndCount(RequestsTotal))
}

func TestCallerRequestsTotal(t *testing.T) {
	resetUsageMetrics(t)
	SetRequestCallers(usagemetrics.NewCallers(
		config.MetricsCallerDimensionsConfig{UserPath: true, UserPathDepth: 1, AuthKey: true, RequestLabels: true},
		func(string) string { return "ci-pipeline" },
	))
	hooks := NewPrometheusHooks()
	ctx := core.WithEffectiveUserPath(context.Background(), "/team/alice")
	ctx = core.WithAuthKeyID(ctx, "key-1")
	ctx = core.WithRequestLabels(ctx, []string{"prod"})

	call := llmclient.RequestInfo{Provider: "openai", Model: "gpt-5", Endpoint: "/chat/completions"}
	for _, status := range []int{http.StatusOK, http.StatusOK, http.StatusTooManyRequests} {
		info := llmclient.ResponseInfo{Provider: "openai", Model: "gpt-5", Endpoint: "/chat/completions", StatusCode: status}
		hooks.OnRequestEnd(hooks.OnRequestStart(ctx, call), info)
	}
	hooks.OnRequestEnd(hooks.OnRequestStart(context.Background(), call),
		llmclient.ResponseInfo{Provider: "openai", Model: "gpt-5", Endpoint: "/chat/completions", StatusCode: http.StatusOK})

	requests := func(statusType, userPath, authKey, labels string) float64 {
		return testutil.ToFloat64(CallerRequestsTotal.WithLabelValues("openai", "gpt-5", statusType, userPath, authKey, labels))
	}
	assert.InDelta(t, 2, requests("success", "/team", "ci-pipeline", "prod"), 0)
	assert.InDelta(t, 1, requests("error", "/team", "ci-pipeline", "prod"), 0)
	assert.InDelta(t, 1, requests("success", "/", "", ""), 0, "a call without a caller is the root user path")
	assert.Equal(t, 3, testutil.CollectAndCount(CallerRequestsTotal))
}

// Caller values come from raw request headers. A value that is not valid
// UTF-8 would make WithLabelValues panic on the request path.
func TestUsageRecorderSurvivesInvalidUTF8(t *testing.T) {
	resetUsageMetrics(t)
	callers := usagemetrics.NewCallers(config.MetricsCallerDimensionsConfig{
		UserPath: true, AuthKey: true, RequestLabels: true,
	}, func(string) string { return "ci\xff" })
	logger := usagemetrics.NewTap(&discardLogger{}, callers, UsageRecorder{})
	entry := &usage.UsageEntry{
		ProviderName: "openai\xff", Provider: "openai", Model: "gpt\xff", InputTokens: 10,
		UserPath: "/team/\xff", AuthKeyID: "key-1", Labels: []string{"prod", "team\xff"},
	}

	require.NotPanics(t, func() { logger.Write(entry) })

	other := usagemetrics.OtherValue
	assert.InDelta(t, 10, testutil.ToFloat64(TokensTotal.WithLabelValues(
		"openai\uFFFD", "gpt\uFFFD", TokenTypeInput, usagemetrics.OtherUserPath, other, other)), 1e-9)
	assert.Equal(t, 1, usageSeriesCount())

	SetRequestCallers(callers)
	ctx := core.WithRequestLabels(core.WithEffectiveUserPath(context.Background(), "/team/\xff"), []string{"team\xff"})
	require.NotPanics(t, func() { recordCallerRequest(ctx, llmclient.ResponseInfo{Provider: "openai", Model: "gpt"}, "success") })
	assert.InDelta(t, 1, testutil.ToFloat64(CallerRequestsTotal.WithLabelValues(
		"openai", "gpt", "success", usagemetrics.OtherUserPath, "", other)), 1e-9)
}
