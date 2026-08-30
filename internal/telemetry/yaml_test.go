package telemetry

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// YAML settings must reach the SDK: the exporter uses the configured endpoint
// and sends the configured (percent-encoded) headers decoded.
func TestNewAppliesYAMLSettingsToExporter(t *testing.T) {
	for _, key := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_HEADERS", "OTEL_EXPORTER_OTLP_PROTOCOL", "OTEL_TRACES_EXPORTER", "OTEL_METRICS_EXPORTER"} {
		t.Setenv(key, "")
	}
	var (
		mu       sync.Mutex
		received http.Header
	)
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/traces" {
			mu.Lock()
			received = r.Header.Clone()
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	service, err := New(t.Context(), config.OpenTelemetryConfig{
		Endpoint:        collector.URL,
		Protocol:        "http/protobuf",
		Headers:         map[string]string{"authorization": "Bearer top secret"},
		MetricsExporter: "none",
	}, "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	hooks := service.Hooks()
	call := llmclient.RequestInfo{Provider: "openai", Model: "gpt-5", Operation: "chat"}
	hooks.OnRequestEnd(hooks.OnRequestStart(t.Context(), call), llmclient.ResponseInfo{Provider: "openai", Model: "gpt-5", Operation: "chat", StatusCode: http.StatusOK})
	if err := service.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if received == nil {
		t.Fatal("no spans reached the YAML-configured endpoint")
	}
	if got := received.Get("Authorization"); got != "Bearer top secret" {
		t.Fatalf("Authorization header = %q, want the YAML value with the space decoded", got)
	}
}
