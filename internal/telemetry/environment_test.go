package telemetry

import (
	"os"
	"testing"
)

func TestExportEnvironmentPrecedenceAndWithdrawal(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "from-env")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_TRACES_SAMPLER", "")

	exportEnvironment(map[string]string{
		"OTEL_SERVICE_NAME":           "from-yaml",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4318",
		"OTEL_TRACES_SAMPLER":         "always_off",
	})
	if got := os.Getenv("OTEL_SERVICE_NAME"); got != "from-env" {
		t.Fatalf("OTEL_SERVICE_NAME = %q, want the operator's environment value to win", got)
	}
	if got := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); got != "http://collector:4318" {
		t.Fatalf("OTEL_EXPORTER_OTLP_ENDPOINT = %q, want YAML value exported", got)
	}

	// A reload that changes one value and drops another.
	exportEnvironment(map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://other:4318",
	})
	if got := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); got != "http://other:4318" {
		t.Fatalf("OTEL_EXPORTER_OTLP_ENDPOINT = %q, want the reloaded YAML value", got)
	}
	if got, set := os.LookupEnv("OTEL_TRACES_SAMPLER"); set && got != "" {
		t.Fatalf("OTEL_TRACES_SAMPLER = %q, want withdrawn after removal from YAML", got)
	}
	exportEnvironment(nil)
}
