package telemetry

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/labstack/echo-opentelemetry"
	"github.com/labstack/echo/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"

	"github.com/enterpilot/gomodel/config"
)

func TestNewBuildsMiddlewareAndHooksWithoutExporters(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")

	service, err := New(t.Context(), config.OpenTelemetryConfig{}, "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	if service.Middleware() == nil || service.Hooks().OnRequestStart == nil {
		t.Fatal("service must expose HTTP middleware and provider hooks")
	}
	if err := service.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
}

func TestNewRejectsUnknownExporter(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "zipkin")
	_, err := New(t.Context(), config.OpenTelemetryConfig{}, "/metrics")
	if err == nil || !strings.Contains(err.Error(), "otlp or none") {
		t.Fatalf("error = %v, want supported-exporter error", err)
	}
}

func TestOperationalEndpointSkipperUsesConfiguredMetricsPath(t *testing.T) {
	skip := operationalEndpointSkipper("/monitoring/metrics")
	tests := map[string]bool{
		"/health":              true,
		"/health/ready":        true,
		"/monitoring/metrics":  true,
		"/metrics":             true,
		"/debug/pprof/heap":    true,
		"/v1/models":           false,
		"/v1/chat/completions": false,
	}
	for requestPath, want := range tests {
		t.Run(requestPath, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, requestPath, nil)
			ctx := echo.New().NewContext(req, httptest.NewRecorder())
			if got := skip(ctx); got != want {
				t.Fatalf("skip(%q) = %v, want %v", requestPath, got, want)
			}
		})
	}
}

func TestPrivacySafeSpanAttributesRemovesIdentityAndHost(t *testing.T) {
	attrs := []attribute.KeyValue{
		semconv.ClientAddress("192.0.2.1"),
		semconv.NetworkPeerAddress("192.0.2.2"),
		semconv.NetworkPeerPort(1234),
		semconv.ServerAddress("attacker.example"),
		semconv.ServerPort(4321),
		semconv.UserAgentOriginal("identifying-agent"),
		semconv.HTTPRequestMethodGet,
	}
	got := privacySafeSpanAttributes(nil, nil, attrs)
	assertAttributeKeys(t, got, []attribute.Key{semconv.HTTPRequestMethodKey})
}

func TestBoundedMetricAttributesRemovesHostDimensions(t *testing.T) {
	values := &echootel.Values{
		HTTPMethod:    http.MethodGet,
		ServerAddress: "attacker.example",
		ServerPort:    4321,
		URLScheme:     "http",
	}
	got := boundedMetricAttributes(nil, values)
	assertAttributeKeys(t, got, []attribute.Key{semconv.HTTPRequestMethodKey, semconv.URLSchemeKey})
}

func TestExporterNameDefaultsToOTLP(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "")
	if got := exporterName("OTEL_TRACES_EXPORTER"); got != "otlp" {
		t.Fatalf("exporterName() = %q, want otlp", got)
	}
}

func TestSignalProtocol(t *testing.T) {
	tests := []struct {
		name     string
		generic  string
		specific string
		want     string
	}{
		{name: "default", want: "http/protobuf"},
		{name: "generic", generic: "grpc", want: "grpc"},
		{name: "signal-specific wins", generic: "grpc", specific: "HTTP/Protobuf", want: "http/protobuf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", tt.generic)
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", tt.specific)
			if got := signalProtocol("TRACES"); got != tt.want {
				t.Fatalf("signalProtocol() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTracerProviderRejectsUnknownProtocol(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "otlp")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "thrift")
	_, err := newTracerProvider(t.Context(), resource.Empty())
	if err == nil || !strings.Contains(err.Error(), "grpc or http/protobuf") {
		t.Fatalf("error = %v, want unsupported-protocol error", err)
	}
}

func TestPropagatorsFromEnv(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		wantFields []string
	}{
		{name: "default", wantFields: []string{"baggage", "traceparent", "tracestate"}},
		{name: "b3 single", configured: "b3", wantFields: []string{"b3"}},
		{name: "b3 multi", configured: "b3multi", wantFields: []string{"x-b3-flags", "x-b3-sampled", "x-b3-spanid", "x-b3-traceid"}},
		{name: "jaeger", configured: "jaeger", wantFields: []string{"uber-trace-id"}},
		{name: "deduplicated and case insensitive", configured: "BAGGAGE, baggage", wantFields: []string{"baggage"}},
		{name: "none overrides", configured: "tracecontext,none", wantFields: nil},
		{name: "unsupported ignored", configured: "unsupported,baggage", wantFields: []string{"baggage"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(propagatorsEnvVar, tt.configured)
			got := propagatorsFromEnv().Fields()
			slices.Sort(got)
			if !slices.Equal(got, tt.wantFields) {
				t.Fatalf("Fields() = %q, want %q", got, tt.wantFields)
			}
		})
	}
}

func assertAttributeKeys(t *testing.T, attrs []attribute.KeyValue, want []attribute.Key) {
	t.Helper()
	got := make([]attribute.Key, 0, len(attrs))
	for _, attr := range attrs {
		got = append(got, attr.Key)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("attribute keys = %v, want %v", got, want)
	}
}
