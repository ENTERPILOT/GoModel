package config

import (
	"net/url"
	"slices"
	"strings"
)

// OpenTelemetryConfig configures OTLP trace and metric export.
//
// Only Enabled is a GoModel setting. The other fields mirror the standard
// OTEL_* environment variables that the OpenTelemetry SDK reads by itself, so
// a YAML-first deployment does not have to reach for the environment for the
// common settings. An OTEL_* variable that is already set in the environment
// wins over the YAML value, like every other GoModel setting; anything not
// listed here (per-signal endpoints, timeouts, compression, batch sizes, …)
// is available through its OTEL_* variable.
type OpenTelemetryConfig struct {
	// Enabled turns on OpenTelemetry export for inbound HTTP requests and
	// outbound provider calls.
	// Default: false
	Enabled bool `yaml:"enabled" env:"OTEL_ENABLED"`

	// ServiceName is the service.name resource attribute (OTEL_SERVICE_NAME).
	// Default: "gomodel"
	ServiceName string `yaml:"service_name"`

	// ResourceAttributes adds resource attributes such as
	// deployment.environment (OTEL_RESOURCE_ATTRIBUTES).
	ResourceAttributes map[string]string `yaml:"resource_attributes"`

	// Endpoint is the OTLP collector endpoint (OTEL_EXPORTER_OTLP_ENDPOINT).
	// Default: "http://localhost:4318" for http/protobuf, "localhost:4317" for grpc
	Endpoint string `yaml:"endpoint"`

	// Protocol is "http/protobuf" or "grpc" (OTEL_EXPORTER_OTLP_PROTOCOL).
	// Default: "http/protobuf"
	Protocol string `yaml:"protocol"`

	// Headers are sent with every export request, for example an
	// authorization header for a hosted backend (OTEL_EXPORTER_OTLP_HEADERS).
	Headers map[string]string `yaml:"headers"`

	// TracesExporter is "otlp" or "none" (OTEL_TRACES_EXPORTER).
	// Default: "otlp"
	TracesExporter string `yaml:"traces_exporter"`

	// MetricsExporter is "otlp" or "none" (OTEL_METRICS_EXPORTER).
	// Default: "otlp"
	MetricsExporter string `yaml:"metrics_exporter"`

	// Sampler selects the trace sampler (OTEL_TRACES_SAMPLER), for example
	// "parentbased_traceidratio" with SamplerArg "0.1".
	// Default: "parentbased_always_on"
	Sampler string `yaml:"sampler"`

	// SamplerArg is the sampler argument (OTEL_TRACES_SAMPLER_ARG).
	SamplerArg string `yaml:"sampler_arg"`

	// Propagators is the comma-separated context propagator list
	// (OTEL_PROPAGATORS): tracecontext, baggage, b3, b3multi, jaeger,
	// ottrace, or none.
	// Default: "tracecontext,baggage"
	Propagators string `yaml:"propagators"`
}

// Environment returns the OTEL_* variables that the configured fields stand
// for. Unset fields are omitted so the SDK applies its own defaults.
func (c OpenTelemetryConfig) Environment() map[string]string {
	vars := make(map[string]string)
	set := func(key, value string) {
		if value = strings.TrimSpace(value); value != "" {
			vars[key] = value
		}
	}
	set("OTEL_SERVICE_NAME", c.ServiceName)
	set("OTEL_RESOURCE_ATTRIBUTES", encodeKeyValues(c.ResourceAttributes))
	set("OTEL_EXPORTER_OTLP_ENDPOINT", c.Endpoint)
	set("OTEL_EXPORTER_OTLP_PROTOCOL", c.Protocol)
	set("OTEL_EXPORTER_OTLP_HEADERS", encodeKeyValues(c.Headers))
	set("OTEL_TRACES_EXPORTER", c.TracesExporter)
	set("OTEL_METRICS_EXPORTER", c.MetricsExporter)
	set("OTEL_TRACES_SAMPLER", c.Sampler)
	set("OTEL_TRACES_SAMPLER_ARG", c.SamplerArg)
	set("OTEL_PROPAGATORS", c.Propagators)
	return vars
}

// encodeKeyValues renders a map in the W3C Baggage-style "k=v,k2=v2" form
// the SDK expects, percent-encoding values so commas, equals signs, and
// spaces survive the round trip. Keys are sorted for a stable result.
func encodeKeyValues(values map[string]string) string {
	pairs := make([]string, 0, len(values))
	for key, value := range values {
		if key = strings.TrimSpace(key); key != "" {
			pairs = append(pairs, key+"="+percentEncode(strings.TrimSpace(value)))
		}
	}
	slices.Sort(pairs)
	return strings.Join(pairs, ",")
}

// percentEncode is url.QueryEscape with spaces as %20: the SDK decodes values
// with url.PathUnescape, which would keep a "+" literally.
func percentEncode(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}
