package telemetry

import (
	"context"
	"testing"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/usage"
	"github.com/enterpilot/gomodel/internal/usagemetrics"
)

func newTestUsageRecorder(t *testing.T) (*usageRecorder, *metric.ManualReader) {
	t.Helper()
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	recorder, err := newUsageRecorder(mp)
	require.NoError(t, err)
	return recorder, reader
}

// tokenUsage returns the gen_ai.client.token.usage points by token type.
func tokenUsage(t *testing.T, reader *metric.ManualReader) map[string]metricdata.HistogramDataPoint[int64] {
	t.Helper()
	found, ok := findMetric(t, reader, "gen_ai.client.token.usage")
	if !ok {
		return nil
	}
	histogram, ok := found.Data.(metricdata.Histogram[int64])
	require.True(t, ok, "gen_ai.client.token.usage is %T, want an int64 histogram", found.Data)
	assert.Equal(t, "{token}", found.Unit)
	points := make(map[string]metricdata.HistogramDataPoint[int64], len(histogram.DataPoints))
	for _, point := range histogram.DataPoints {
		tokenType, _ := point.Attributes.Value("gen_ai.token.type")
		points[tokenType.AsString()] = point
	}
	return points
}

// counterValues returns a counter's values keyed by the given attribute
// ("" for a counter that is not split by one).
func counterValues[N int64 | float64](t *testing.T, reader *metric.ManualReader, name, key string) map[string]N {
	t.Helper()
	found, ok := findMetric(t, reader, name)
	if !ok {
		return nil
	}
	sum, ok := found.Data.(metricdata.Sum[N])
	require.True(t, ok, "%s is %T", name, found.Data)
	require.True(t, sum.IsMonotonic)
	values := make(map[string]N, len(sum.DataPoints))
	for _, point := range sum.DataPoints {
		value, _ := point.Attributes.Value(attribute.Key(key))
		values[value.AsString()] += point.Value
	}
	return values
}

type discardLogger struct{}

func (discardLogger) Write(*usage.UsageEntry) {}
func (discardLogger) Config() usage.Config    { return usage.Config{Enabled: true} }
func (discardLogger) Close() error            { return nil }

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

// assertChatUsageExported checks one call of 1000 input tokens (400 cached)
// and 500 output tokens (100 reasoning) at testPricing, billed at 1.5x.
func assertChatUsageExported(t *testing.T, reader *metric.ManualReader) {
	t.Helper()
	tokens := tokenUsage(t, reader)
	require.Len(t, tokens, 2)
	assert.Equal(t, int64(1000), tokens["input"].Sum)
	assert.Equal(t, uint64(1), tokens["input"].Count, "one measurement per call and token type")
	assert.Equal(t, int64(500), tokens["output"].Sum)
	assert.Equal(t, uint64(1), tokens["output"].Count)

	assert.Equal(t, map[string]int64{"input_cached": 400, "output_reasoning": 100},
		counterValues[int64](t, reader, "gomodel.client.token.usage.details", "gomodel.token.type"))
	assert.InDelta(t, 0.007, counterValues[float64](t, reader, "gomodel.client.provider_cost", "")[""], 1e-12)
	assert.InDelta(t, 0.0105, counterValues[float64](t, reader, "gomodel.client.billed_cost", "")[""], 1e-12)
}

func TestUsageRecorderExportsNonStreamingUsage(t *testing.T) {
	recorder, reader := newTestUsageRecorder(t)
	logger := markupLogger{LoggerInterface: usagemetrics.NewTap(discardLogger{}, nil, recorder), factor: 1.5}

	entry := usage.ExtractFromChatResponse(&core.ChatResponse{ID: "chatcmpl-1", Model: "gpt-5-2026", Usage: core.Usage{
		PromptTokens: 1000, CompletionTokens: 500, TotalTokens: 1500,
		PromptTokensDetails:     &core.PromptTokensDetails{CachedTokens: 400},
		CompletionTokensDetails: &core.CompletionTokensDetails{ReasoningTokens: 100},
	}}, "req-1", "openai", "/v1/chat/completions", &testPricing)
	entry.ProviderName, entry.RoutedModel = "openai-eu", "gpt-5"
	entry.UserPath, entry.AuthKeyID, entry.Labels = "/team/alice", "key-1", []string{"prod"}
	logger.Write(entry)

	assertChatUsageExported(t, reader)
	input := tokenUsage(t, reader)["input"]
	attrs := attributeMap(input.Attributes.ToSlice())
	assert.Equal(t, map[string]string{
		"gen_ai.operation.name": "chat",
		"gen_ai.provider.name":  "openai",
		"gomodel.provider.name": "openai-eu",
		"gen_ai.request.model":  "gpt-5",
		"gen_ai.response.model": "gpt-5-2026",
		"gen_ai.token.type":     "input",
	}, attrs, "caller attributes are off by default")
}

func TestUsageRecorderExportsStreamingUsageWithCallerAttributes(t *testing.T) {
	usagemetrics.ResetCallerValues()
	t.Cleanup(usagemetrics.ResetCallerValues)
	recorder, reader := newTestUsageRecorder(t)
	callers := usagemetrics.NewCallers(
		config.MetricsCallerDimensionsConfig{UserPath: true, UserPathDepth: 2, AuthKey: true, RequestLabels: true},
		func(id string) string { return map[string]string{"key-1": "ci-pipeline"}[id] },
	)
	logger := markupLogger{LoggerInterface: usagemetrics.NewTap(discardLogger{}, callers, recorder), factor: 1.5}

	observer := usage.NewStreamUsageObserver(logger, "gpt-5", "openai", "req-1", "/v1/chat/completions", fixedPricing{testPricing}, "/team/alice/laptop")
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
	require.Empty(t, tokenUsage(t, reader), "a stream is exported once, when it closes")
	observer.OnStreamClose()

	assertChatUsageExported(t, reader)
	for name, values := range map[string]map[string]float64{
		"gomodel.client.provider_cost": counterValues[float64](t, reader, "gomodel.client.provider_cost", "gomodel.auth_key.name"),
		"gomodel.client.billed_cost":   counterValues[float64](t, reader, "gomodel.client.billed_cost", "gomodel.auth_key.name"),
	} {
		assert.Contains(t, values, "ci-pipeline", name)
	}
	output := tokenUsage(t, reader)["output"]
	attrs := attributeMap(output.Attributes.ToSlice())
	assert.Equal(t, "/team/alice", attrs["gomodel.user_path"])
	assert.Equal(t, "ci-pipeline", attrs["gomodel.auth_key.name"])
	assert.Equal(t, "prod,team-a", attrs["gomodel.request.labels"])
}

// OTLP refuses a string that is not valid UTF-8 and fails the whole export
// with it, so a caller value from a raw request header must never reach an
// attribute as it came.
func TestUsageRecorderExportsOnlyValidUTF8(t *testing.T) {
	usagemetrics.ResetCallerValues()
	t.Cleanup(usagemetrics.ResetCallerValues)
	recorder, reader := newTestUsageRecorder(t)
	callers := usagemetrics.NewCallers(
		config.MetricsCallerDimensionsConfig{UserPath: true, AuthKey: true, RequestLabels: true},
		func(string) string { return "ci\xff" },
	)
	usagemetrics.NewTap(discardLogger{}, callers, recorder).Write(&usage.UsageEntry{
		ProviderName: "openai\xff", Provider: "openai\xff", Model: "gpt\xff", RoutedModel: "alias\xff",
		Endpoint: "/v1/chat/completions", InputTokens: 10,
		UserPath: "/team/\xff", AuthKeyID: "key-1", Labels: []string{"prod", "team\xff"},
	})

	input, ok := tokenUsage(t, reader)["input"]
	require.True(t, ok)
	attrs := attributeMap(input.Attributes.ToSlice())
	for key, value := range attrs {
		assert.True(t, utf8.ValidString(value), "%s = %q", key, value)
	}
	assert.Equal(t, usagemetrics.OtherUserPath, attrs["gomodel.user_path"])
	assert.Equal(t, usagemetrics.OtherValue, attrs["gomodel.auth_key.name"])
	assert.Equal(t, usagemetrics.OtherValue, attrs["gomodel.request.labels"])
	assert.Equal(t, "gpt\uFFFD", attrs["gen_ai.response.model"])
}

func TestUsageRecorderKeepsProviderAndBilledCostApart(t *testing.T) {
	tests := []struct {
		name         string
		sample       usagemetrics.Sample
		wantProvider map[string]float64
		wantBilled   map[string]float64
	}{
		{name: "markups off exports no billed cost", sample: usagemetrics.Sample{ProviderCost: 0.2}, wantProvider: map[string]float64{"": 0.2}},
		{name: "markup", sample: usagemetrics.Sample{ProviderCost: 0.2, BilledCost: 0.25}, wantProvider: map[string]float64{"": 0.2}, wantBilled: map[string]float64{"": 0.25}},
		{name: "billed response-cache hit", sample: usagemetrics.Sample{BilledCost: 0.25}, wantBilled: map[string]float64{"": 0.25}},
		{name: "free or unknown cost", sample: usagemetrics.Sample{InputTokens: 5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder, reader := newTestUsageRecorder(t)

			recorder.RecordUsage(tt.sample)

			assert.Equal(t, tt.wantProvider, counterValues[float64](t, reader, "gomodel.client.provider_cost", ""))
			assert.Equal(t, tt.wantBilled, counterValues[float64](t, reader, "gomodel.client.billed_cost", ""))
		})
	}
}

func TestUsageRecorderExportsCacheWriteTokens(t *testing.T) {
	recorder, reader := newTestUsageRecorder(t)

	recorder.RecordUsage(usagemetrics.Sample{Provider: "anthropic", InputTokens: 250, CachedInputTokens: 200, CacheWriteInputTokens: 40})

	assert.Equal(t, map[string]int64{"input_cached": 200, "input_cache_write": 40},
		counterValues[int64](t, reader, "gomodel.client.token.usage.details", "gomodel.token.type"))
	assert.Len(t, tokenUsage(t, reader), 1, "no output measurement without output tokens")
}

func TestUsageRecorderExportsNothingWithoutUsage(t *testing.T) {
	recorder, reader := newTestUsageRecorder(t)
	logger := usagemetrics.NewTap(discardLogger{}, nil, recorder)

	logger.Write(&usage.UsageEntry{Provider: "mcp", ProviderName: "github", Model: "search"})
	logger.Write(&usage.UsageEntry{Provider: "openai", Model: "gpt-5", CacheType: usage.CacheTypeExact, InputTokens: 10, TotalCost: new(0.1)})
	recorder.RecordUsage(usagemetrics.Sample{Provider: "openai", ProviderCost: -1, BilledCost: -1})

	assert.Empty(t, collect(t, reader).ScopeMetrics)
}

func TestUsageOperation(t *testing.T) {
	tests := map[string]string{
		"/v1/chat/completions":   "chat",
		"/chat/completions":      "chat",
		"/v1/responses":          "chat",
		"responses":              "chat",
		"messages":               "chat",
		"/v1/messages?beta=true": "chat",
		"/p/cohere/v2/chat":      "chat",
		"/v1/embeddings":         "embeddings",
		"embeddings":             "embeddings",
		"/v2/embed":              "embeddings",
		"/v1/audio/speech":       "",
		"/v1/images/generations": "",
		"/v1/realtime":           "",
		"/v1/completions":        "text_completion",
		"/p/vllm/completions":    "text_completion",
		"":                       "",
	}
	for endpoint, want := range tests {
		t.Run(endpoint, func(t *testing.T) {
			assert.Equal(t, want, usageOperation(usagemetrics.Sample{Endpoint: endpoint}))
		})
	}
}

func TestUsageOperationPrefersTheRecordedOperation(t *testing.T) {
	assert.Equal(t, "generate_content", usageOperation(usagemetrics.Sample{Endpoint: "/v1/chat/completions", Operation: "generate_content"}))
}
