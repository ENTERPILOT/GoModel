package config

import (
	"path"
	"strings"
)

// MetricsConfig holds observability configuration for Prometheus metrics
type MetricsConfig struct {
	// Enabled controls whether Prometheus metrics are collected and exposed
	// Default: false
	Enabled bool `yaml:"enabled" env:"METRICS_ENABLED"`

	// Endpoint is the HTTP path where metrics are exposed
	// Default: "/metrics"
	Endpoint string `yaml:"endpoint" env:"METRICS_ENDPOINT"`
}

// ResolveMetricsEndpoint returns the normalized, safe endpoint used by the
// HTTP server. Extensions should use the same value when excluding Prometheus
// scrapes from request instrumentation.
func ResolveMetricsEndpoint(endpoint string) string {
	metricsPath := "/metrics"
	if endpoint != "" {
		metricsPath = path.Clean("/" + endpoint)
	}
	if metricsPath == "/v1" || strings.HasPrefix(metricsPath, "/v1/") ||
		metricsPath == "/p" || strings.HasPrefix(metricsPath, "/p/") {
		return "/metrics"
	}
	return metricsPath
}
