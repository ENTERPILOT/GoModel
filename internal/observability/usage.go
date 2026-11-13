package observability

import (
	"context"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/usagemetrics"
)

// Values of the token_type label.
const (
	TokenTypeInput  = "input"
	TokenTypeOutput = "output"

	TokenTypeInputCached     = "input_cached"
	TokenTypeInputCacheWrite = "input_cache_write" //nolint:gosec // G101: metric label value, not a credential.
	TokenTypeOutputReasoning = "output_reasoning"
)

// Token and cost metrics, fed from recorded usage entries. The caller labels
// (user_path, auth_key, request_labels) are opt-in and empty when switched
// off; Prometheus treats an empty label as an absent one.
//
// Numbers that must not be added together live in separate metrics: the
// details are parts of gomodel_tokens_total, and the billed cost is the
// provider cost plus markup.
var (
	// TokensTotal counts every token sent to and returned by providers.
	TokensTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gomodel_tokens_total",
			Help: "Tokens processed by providers. token_type is input (uncached, cached, and cache-write prompt tokens) or output (reasoning included). Response-cache hits are not counted",
		},
		[]string{"provider", "model", "token_type", "user_path", "auth_key", "request_labels"},
	)

	// TokenDetailsTotal breaks gomodel_tokens_total down further.
	TokenDetailsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gomodel_token_details_total",
			Help: "Parts of gomodel_tokens_total: input_cached and input_cache_write are parts of the input tokens, output_reasoning is part of the output tokens. Do not add them to gomodel_tokens_total",
		},
		[]string{"provider", "model", "token_type", "user_path", "auth_key", "request_labels"},
	)

	// ProviderCostTotal sums what providers charge.
	ProviderCostTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gomodel_provider_cost_usd_total",
			Help: "Provider cost of requests in USD, for requests with known pricing. Response-cache hits are not counted",
		},
		[]string{"provider", "model", "user_path", "auth_key", "request_labels"},
	)

	// BilledCostTotal sums what callers are billed.
	BilledCostTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gomodel_billed_cost_usd_total",
			Help: "Cost billed to callers in USD: provider cost plus markup, including billed response-cache hits. Recorded only when pricing markups are enabled",
		},
		[]string{"provider", "model", "user_path", "auth_key", "request_labels"},
	)

	// CallerRequestsTotal counts provider calls per caller. It exists so the
	// caller labels never multiply gomodel_requests_total and its histogram:
	// it drops their endpoint, status_code, and stream labels, and is
	// recorded only while a caller dimension is enabled.
	CallerRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gomodel_caller_requests_total",
			Help: "LLM requests by caller. Recorded only while a caller dimension is enabled",
		},
		[]string{"provider", "model", "status_type", "user_path", "auth_key", "request_labels"},
	)
)

// UsageRecorder writes usage samples to the Prometheus token and cost
// counters.
type UsageRecorder struct{}

// RecordUsage adds one usage entry's tokens and cost. Zero values are
// skipped, so a series appears only once it has something to report.
func (UsageRecorder) RecordUsage(s usagemetrics.Sample) {
	c := s.Caller
	addTokens(TokensTotal, s, TokenTypeInput, s.InputTokens)
	addTokens(TokensTotal, s, TokenTypeOutput, s.OutputTokens)
	addTokens(TokenDetailsTotal, s, TokenTypeInputCached, s.CachedInputTokens)
	addTokens(TokenDetailsTotal, s, TokenTypeInputCacheWrite, s.CacheWriteInputTokens)
	addTokens(TokenDetailsTotal, s, TokenTypeOutputReasoning, s.ReasoningOutputTokens)
	if s.ProviderCost > 0 {
		ProviderCostTotal.WithLabelValues(s.Provider, s.Model, c.UserPath, c.AuthKey, c.RequestLabels).Add(s.ProviderCost)
	}
	if s.BilledCost > 0 {
		BilledCostTotal.WithLabelValues(s.Provider, s.Model, c.UserPath, c.AuthKey, c.RequestLabels).Add(s.BilledCost)
	}
}

func addTokens(counter *prometheus.CounterVec, s usagemetrics.Sample, tokenType string, tokens int64) {
	if tokens > 0 {
		c := s.Caller
		counter.WithLabelValues(s.Provider, s.Model, tokenType, c.UserPath, c.AuthKey, c.RequestLabels).Add(float64(tokens))
	}
}

// requestCallers resolves the caller of a provider call for
// gomodel_caller_requests_total; nil while every caller dimension is off.
// The hooks are built before the configuration that names the dimensions is
// wired, so the application sets it afterwards.
var requestCallers atomic.Pointer[usagemetrics.Callers]

// SetRequestCallers selects the caller dimensions of
// gomodel_caller_requests_total. nil stops recording the metric.
func SetRequestCallers(callers *usagemetrics.Callers) {
	requestCallers.Store(callers)
}

// RequestCallers returns the caller dimensions SetRequestCallers selected.
func RequestCallers() *usagemetrics.Callers {
	return requestCallers.Load()
}

// recordCallerRequest counts one finished provider call for its caller. With
// every caller dimension off it costs one atomic load.
func recordCallerRequest(ctx context.Context, info llmclient.ResponseInfo, statusType string) {
	callers := requestCallers.Load()
	if callers == nil {
		return
	}
	c := callers.Resolve(core.UserPathFromContext(ctx), core.GetAuthKeyID(ctx), core.RequestLabelsFromContext(ctx))
	CallerRequestsTotal.WithLabelValues(info.Provider, info.Model, statusType, c.UserPath, c.AuthKey, c.RequestLabels).Inc()
}
