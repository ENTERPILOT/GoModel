package telemetry

import (
	"context"
	"slices"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	apiMetric "go.opentelemetry.io/otel/metric"

	"github.com/enterpilot/gomodel/internal/usagemetrics"
)

// tokenBuckets are the boundaries the GenAI semantic conventions specify for
// gen_ai.client.token.usage.
var tokenBuckets = []float64{1, 4, 16, 64, 256, 1024, 4096, 16384, 65536, 262144, 1048576, 4194304, 16777216, 67108864}

// usageRecorder exports token usage and cost from recorded usage entries.
//
// gen_ai.client.token.usage follows the semantic conventions: one input and
// one output measurement per call, each complete (cached and cache-write
// tokens are input, reasoning is output). The conventions have no metric for
// the parts of those totals or for cost, so those are gomodel.* counters;
// keeping them apart means no sum over one metric counts a token or a dollar
// twice.
type usageRecorder struct {
	tokens       apiMetric.Int64Histogram
	tokenDetails apiMetric.Int64Counter
	providerCost apiMetric.Float64Counter
	billedCost   apiMetric.Float64Counter
}

func newUsageRecorder(mp apiMetric.MeterProvider) (*usageRecorder, error) {
	meter := mp.Meter(instrumentationName)
	tokens, err := meter.Int64Histogram(
		"gen_ai.client.token.usage",
		apiMetric.WithDescription("Number of input and output tokens used."),
		apiMetric.WithUnit("{token}"),
		apiMetric.WithExplicitBucketBoundaries(tokenBuckets...),
	)
	if err != nil {
		return nil, err
	}
	tokenDetails, err := meter.Int64Counter(
		"gomodel.client.token.usage.details",
		apiMetric.WithDescription("Parts of gen_ai.client.token.usage: cached and cache-write input tokens, reasoning output tokens."),
		apiMetric.WithUnit("{token}"),
	)
	if err != nil {
		return nil, err
	}
	providerCost, err := meter.Float64Counter(
		"gomodel.client.provider_cost",
		apiMetric.WithDescription("Provider cost of requests with known pricing."),
		apiMetric.WithUnit("{USD}"),
	)
	if err != nil {
		return nil, err
	}
	billedCost, err := meter.Float64Counter(
		"gomodel.client.billed_cost",
		apiMetric.WithDescription("Cost billed to callers: provider cost plus markup, including billed response-cache hits."),
		apiMetric.WithUnit("{USD}"),
	)
	if err != nil {
		return nil, err
	}
	return &usageRecorder{tokens: tokens, tokenDetails: tokenDetails, providerCost: providerCost, billedCost: billedCost}, nil
}

// RecordUsage exports one usage entry. Zero values are skipped, so a
// response without usage is not shown as a free call.
func (r *usageRecorder) RecordUsage(s usagemetrics.Sample) {
	ctx := context.Background()
	attrs := usageAttributes(s)
	r.recordTokens(ctx, attrs, "input", s.InputTokens)
	r.recordTokens(ctx, attrs, "output", s.OutputTokens)
	r.addTokenDetail(ctx, attrs, "input_cached", s.CachedInputTokens)
	r.addTokenDetail(ctx, attrs, "input_cache_write", s.CacheWriteInputTokens)
	r.addTokenDetail(ctx, attrs, "output_reasoning", s.ReasoningOutputTokens)
	if s.ProviderCost > 0 {
		r.providerCost.Add(ctx, s.ProviderCost, apiMetric.WithAttributes(attrs...))
	}
	if s.BilledCost > 0 {
		r.billedCost.Add(ctx, s.BilledCost, apiMetric.WithAttributes(attrs...))
	}
}

func (r *usageRecorder) recordTokens(ctx context.Context, attrs []attribute.KeyValue, tokenType string, tokens int64) {
	if tokens > 0 {
		r.tokens.Record(ctx, tokens, apiMetric.WithAttributes(
			append(slices.Clip(attrs), attribute.String("gen_ai.token.type", tokenType))...))
	}
}

func (r *usageRecorder) addTokenDetail(ctx context.Context, attrs []attribute.KeyValue, tokenType string, tokens int64) {
	if tokens > 0 {
		r.tokenDetails.Add(ctx, tokens, apiMetric.WithAttributes(
			append(slices.Clip(attrs), attribute.String("gomodel.token.type", tokenType))...))
	}
}

// usageAttributes describes the call a sample came from, plus the caller
// dimensions the operator opted into.
func usageAttributes(s usagemetrics.Sample) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 8)
	if operation := usageOperation(s.Endpoint); operation != "" {
		attrs = append(attrs, attribute.String("gen_ai.operation.name", operation))
	}
	attrs = append(attrs,
		attribute.String("gen_ai.provider.name", semanticProviderName(s.ProviderType, s.Provider)),
		attribute.String("gomodel.provider.name", s.Provider),
	)
	if model := modelName(s.RequestModel); model != "" {
		attrs = append(attrs, attribute.String("gen_ai.request.model", model))
	}
	if model := modelName(s.Model); model != "" {
		attrs = append(attrs, attribute.String("gen_ai.response.model", model))
	}
	if s.Caller.UserPath != "" {
		attrs = append(attrs, attribute.String("gomodel.user_path", s.Caller.UserPath))
	}
	if s.Caller.AuthKey != "" {
		attrs = append(attrs, attribute.String("gomodel.auth_key.name", s.Caller.AuthKey))
	}
	if s.Caller.RequestLabels != "" {
		attrs = append(attrs, attribute.String("gomodel.request.labels", s.Caller.RequestLabels))
	}
	return attrs
}

// usageOperation maps a usage entry's endpoint onto the GenAI operation name.
// Endpoints without a well-known operation (audio, images, realtime, …) get
// none rather than a wrong one.
func usageOperation(endpoint string) string {
	endpoint, _, _ = strings.Cut(strings.TrimRight(strings.TrimSpace(endpoint), "/"), "?")
	switch {
	case strings.HasSuffix(endpoint, "embeddings"), strings.HasSuffix(endpoint, "/embed"):
		return "embeddings"
	case strings.HasSuffix(endpoint, "chat/completions"), strings.HasSuffix(endpoint, "responses"),
		strings.HasSuffix(endpoint, "messages"), strings.HasSuffix(endpoint, "/chat"):
		return "chat"
	default:
		return ""
	}
}
