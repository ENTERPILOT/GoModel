package config

import (
	"reflect"
	"testing"
)

func TestOpenTelemetryConfigEnvironment(t *testing.T) {
	tests := []struct {
		name string
		cfg  OpenTelemetryConfig
		want map[string]string
	}{
		{name: "empty config exports nothing", cfg: OpenTelemetryConfig{Enabled: true}, want: map[string]string{}},
		{
			name: "fields map to their OTEL variables",
			cfg: OpenTelemetryConfig{
				ServiceName:     " gomodel-prod ",
				Endpoint:        "http://collector:4317",
				Protocol:        "grpc",
				TracesExporter:  "otlp",
				MetricsExporter: "none",
				Sampler:         "parentbased_traceidratio",
				SamplerArg:      "0.1",
				Propagators:     "b3,baggage",
			},
			want: map[string]string{
				"OTEL_SERVICE_NAME":           "gomodel-prod",
				"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4317",
				"OTEL_EXPORTER_OTLP_PROTOCOL": "grpc",
				"OTEL_TRACES_EXPORTER":        "otlp",
				"OTEL_METRICS_EXPORTER":       "none",
				"OTEL_TRACES_SAMPLER":         "parentbased_traceidratio",
				"OTEL_TRACES_SAMPLER_ARG":     "0.1",
				"OTEL_PROPAGATORS":            "b3,baggage",
			},
		},
		{
			name: "maps are sorted and percent-encoded",
			cfg: OpenTelemetryConfig{
				Headers:            map[string]string{"x-tenant": "a,b", "authorization": "Bearer tok=en"},
				ResourceAttributes: map[string]string{"deployment.environment": "prod eu", "": "ignored"},
			},
			want: map[string]string{
				"OTEL_EXPORTER_OTLP_HEADERS": "authorization=Bearer%20tok%3Den,x-tenant=a%2Cb",
				"OTEL_RESOURCE_ATTRIBUTES":   "deployment.environment=prod%20eu",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.Environment(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Environment() = %v, want %v", got, tt.want)
			}
		})
	}
}
