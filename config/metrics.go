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

	// CallerDimensions adds who made the request to the token, cost, and
	// per-caller request metrics. It applies to the Prometheus metrics and
	// to the OpenTelemetry metrics alike, whichever pipeline is enabled.
	CallerDimensions MetricsCallerDimensionsConfig `yaml:"caller_dimensions"`
}

// Defaults for MetricsCallerDimensionsConfig, also applied to values below 1.
const (
	DefaultMetricsUserPathDepth   = 2
	DefaultMetricsCallerMaxValues = 100
)

// MetricsCallerDimensionsConfig selects the caller dimensions on metrics.
// Every dimension is off by default: each one multiplies the number of
// exported series by its number of distinct values.
type MetricsCallerDimensionsConfig struct {
	// UserPath adds the request's user path, cut to UserPathDepth segments.
	// Default: false
	UserPath bool `yaml:"user_path" env:"METRICS_CALLER_USER_PATH"`

	// UserPathDepth is how many leading user path segments are kept:
	// with 2, "/team/alice/laptop" is reported as "/team/alice".
	// Default: 2
	UserPathDepth int `yaml:"user_path_depth" env:"METRICS_CALLER_USER_PATH_DEPTH"`

	// AuthKey adds the name of the managed API key that authenticated the
	// request. The key's secret and ID are never exported.
	// Default: false
	AuthKey bool `yaml:"auth_key" env:"METRICS_CALLER_AUTH_KEY"`

	// RequestLabels adds the request's labels (API key labels and tagging
	// headers) as one sorted, comma-separated value.
	// Default: false
	RequestLabels bool `yaml:"request_labels" env:"METRICS_CALLER_REQUEST_LABELS"`

	// MaxValues caps the distinct values of each dimension. Values seen after
	// the cap is reached are reported as "{other}".
	// Default: 100
	MaxValues int `yaml:"max_values" env:"METRICS_CALLER_MAX_VALUES"`
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
