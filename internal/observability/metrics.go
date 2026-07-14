// Package observability provides instrumentation for metrics, tracing, and logging.
package observability

import (
	"context"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/enterpilot/gomodel/internal/llmclient"
)

// Prometheus metrics for LLM gateway observability
var (
	// RequestsTotal counts total LLM requests by provider, model, endpoint, and status
	RequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gomodel_requests_total",
			Help: "Total number of LLM requests",
		},
		[]string{"provider", "model", "endpoint", "status_code", "status_type", "stream"},
	)

	// RequestDuration measures request latency distribution
	// For streaming requests, this measures time to stream establishment, not total stream duration
	RequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "gomodel_request_duration_seconds",
			Help:    "LLM request duration in seconds",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60},
		},
		[]string{"provider", "model", "endpoint", "stream"},
	)

	// InFlightRequests tracks concurrent requests per provider
	InFlightRequests = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gomodel_requests_in_flight",
			Help: "Number of LLM requests currently in flight",
		},
		[]string{"provider", "endpoint", "stream"},
	)

	// ResponseSnapshotStoreFailures counts failures while storing response snapshots.
	ResponseSnapshotStoreFailures = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gomodel_response_snapshot_store_failures_total",
			Help: "Total number of response snapshot store failures",
		},
		[]string{"provider", "provider_name", "operation"},
	)

	// CircuitBreakerState reports each provider's circuit breaker state as of
	// its most recent request (0=closed, 1=half-open, 2=open). The value is
	// updated per request, so an idle provider keeps its last observed state.
	CircuitBreakerState = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "gomodel_circuit_breaker_state",
			Help: "Circuit breaker state per provider (0=closed, 1=half-open, 2=open)",
		},
		[]string{"provider"},
	)
)

// circuitStateValue maps llmclient circuit state names to gauge values.
func circuitStateValue(state string) (float64, bool) {
	switch state {
	case "closed":
		return 0, true
	case "half-open":
		return 1, true
	case "open":
		return 2, true
	default:
		return 0, false
	}
}

// NewPrometheusHooks returns hooks that instrument LLM requests with Prometheus metrics.
// These hooks can be injected into llmclient.Config to enable observability without
// polluting business logic.
func NewPrometheusHooks() llmclient.Hooks {
	return llmclient.Hooks{
		OnRequestStart: func(ctx context.Context, info llmclient.RequestInfo) context.Context {
			// Increment in-flight gauge
			streamLabel := strconv.FormatBool(info.Stream)
			InFlightRequests.WithLabelValues(
				info.Provider,
				info.Endpoint,
				streamLabel,
			).Inc()

			return ctx
		},
		OnRequestEnd: func(ctx context.Context, info llmclient.ResponseInfo) {
			// Decrement in-flight gauge
			streamLabel := strconv.FormatBool(info.Stream)
			InFlightRequests.WithLabelValues(
				info.Provider,
				info.Endpoint,
				streamLabel,
			).Dec()

			// Determine status type and code
			statusType := "success"
			statusCode := strconv.Itoa(info.StatusCode)

			if info.Error != nil {
				statusType = "error"
				if info.StatusCode == 0 {
					// Network error or circuit breaker
					statusCode = "network_error"
				}
			} else if info.StatusCode >= 400 {
				// HTTP error (shouldn't happen if Error is nil, but be defensive)
				statusType = "error"
			}

			// Increment request counter
			RequestsTotal.WithLabelValues(
				info.Provider,
				info.Model,
				info.Endpoint,
				statusCode,
				statusType,
				streamLabel,
			).Inc()

			// Record request duration
			RequestDuration.WithLabelValues(
				info.Provider,
				info.Model,
				info.Endpoint,
				streamLabel,
			).Observe(info.Duration.Seconds())

			// Record circuit breaker state (empty when the breaker is disabled)
			if value, ok := circuitStateValue(info.CircuitState); ok {
				CircuitBreakerState.WithLabelValues(info.Provider).Set(value)
			}
		},
	}
}

// Example query patterns for Prometheus:
//
// Request rate by provider:
//   rate(gomodel_requests_total[5m])
//
// Error rate by provider:
//   rate(gomodel_requests_total{status_type="error"}[5m])
//
// P95 latency by model:
//   histogram_quantile(0.95, rate(gomodel_request_duration_seconds_bucket[5m]))
//
// Concurrent requests:
//   gomodel_requests_in_flight

// Example Grafana dashboard queries:
//
// Panel 1: Request Rate
// Query: sum(rate(gomodel_requests_total[5m])) by (provider)
//
// Panel 2: Error Rate %
// Query: sum(rate(gomodel_requests_total{status_type="error"}[5m])) / sum(rate(gomodel_requests_total[5m])) * 100
//
// Panel 3: Latency Percentiles
// Query: histogram_quantile(0.95, sum(rate(gomodel_request_duration_seconds_bucket[5m])) by (le, provider))
//
// Panel 4: In-Flight Requests
// Query: sum(gomodel_requests_in_flight) by (provider)
//
// Panel 5: Requests by Model
// Query: sum(rate(gomodel_requests_total[5m])) by (model)

// ResetMetrics resets all metrics to zero (useful for testing)
func ResetMetrics() {
	RequestsTotal.Reset()
	RequestDuration.Reset()
	InFlightRequests.Reset()
	ResponseSnapshotStoreFailures.Reset()
	CircuitBreakerState.Reset()
}
