package telemetry

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
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
	require.Equal(t, "from-env", os.Getenv("OTEL_SERVICE_NAME"))
	require.Equal(t, "http://collector:4318", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))

	// A reload that changes one value and drops another.
	exportEnvironment(map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://other:4318",
	})
	require.Equal(t, "http://other:4318", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	require.Empty(t, os.Getenv("OTEL_TRACES_SAMPLER"), "want withdrawn after removal from YAML")
	exportEnvironment(nil)
}
