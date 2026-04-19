// Package observability provides instrumentation for metrics, tracing, and logging.
package observability

import (
	"context"
	"fmt"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"gomodel/internal/llmclient"
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
)

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

// PrometheusMetrics provides access to all registered metrics for testing
type PrometheusMetrics struct {
	RequestsTotal                 *prometheus.CounterVec
	RequestDuration               *prometheus.HistogramVec
	InFlightRequests              *prometheus.GaugeVec
	ResponseSnapshotStoreFailures *prometheus.CounterVec
}

// GetMetrics returns the prometheus metrics for testing and introspection
func GetMetrics() *PrometheusMetrics {
	return &PrometheusMetrics{
		RequestsTotal:                 RequestsTotal,
		RequestDuration:               RequestDuration,
		InFlightRequests:              InFlightRequests,
		ResponseSnapshotStoreFailures: ResponseSnapshotStoreFailures,
	}
}

// ResetMetrics resets all metrics to zero (useful for testing)
func ResetMetrics() {
	RequestsTotal.Reset()
	RequestDuration.Reset()
	InFlightRequests.Reset()
	ResponseSnapshotStoreFailures.Reset()
}

// HealthCheck verifies that metrics are being collected
func HealthCheck() error {
	// Try to collect metrics
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		return fmt.Errorf("failed to gather metrics: %w", err)
	}

	// Check that we have some metrics
	if len(mfs) == 0 {
		return fmt.Errorf("no metrics registered")
	}

	return nil
}
