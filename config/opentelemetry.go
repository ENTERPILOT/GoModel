package config

// OpenTelemetryConfig gates OTLP trace and metric export. Everything else —
// endpoint, protocol, headers, resource attributes, sampling, propagation —
// is read from the standard OTEL_* environment variables by the OpenTelemetry
// SDK, so an operator configures GoModel the same way as any other service.
type OpenTelemetryConfig struct {
	// Enabled turns on OpenTelemetry export for inbound HTTP requests and
	// outbound provider calls.
	// Default: false
	Enabled bool `yaml:"enabled" env:"OTEL_ENABLED"`
}
