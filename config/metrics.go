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

// reservedMetricsPrefixes are the authenticated route trees the metrics
// endpoint must never overlap: the metrics path skips authentication, so a
// metrics path inside one of them would expose that route without a credential.
var reservedMetricsPrefixes = []string{"/v1", "/p", "/admin", "/mcp"}

// ResolveMetricsEndpoint returns the normalized, safe endpoint used by the
// HTTP server. Extensions should use the same value when excluding Prometheus
// scrapes from request instrumentation.
func ResolveMetricsEndpoint(endpoint string) string {
	metricsPath := "/metrics"
	if endpoint != "" {
		metricsPath = path.Clean("/" + endpoint)
	}
	for _, prefix := range reservedMetricsPrefixes {
		if metricsPath == prefix || strings.HasPrefix(metricsPath, prefix+"/") {
			return "/metrics"
		}
	}
	return metricsPath
}

// ResolveMetricsEndpointWithPprof also prevents the metrics route from
// shadowing an enabled pprof route.
func ResolveMetricsEndpointWithPprof(endpoint string, pprofEnabled bool) string {
	metricsPath := ResolveMetricsEndpoint(endpoint)
	if pprofEnabled && (metricsPath == "/debug/pprof" || strings.HasPrefix(metricsPath, "/debug/pprof/")) {
		return "/metrics"
	}
	return metricsPath
}
