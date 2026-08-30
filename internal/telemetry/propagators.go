package telemetry

import (
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/propagators/b3"
	"go.opentelemetry.io/contrib/propagators/jaeger"
	"go.opentelemetry.io/contrib/propagators/ot"
	"go.opentelemetry.io/otel/propagation"
)

const propagatorsEnvVar = "OTEL_PROPAGATORS"

// propagatorsFromEnv builds the context propagator named by OTEL_PROPAGATORS:
// a comma-separated, case-insensitive list of tracecontext, baggage, b3,
// b3multi, jaeger, ottrace, or none. The default is "tracecontext,baggage".
func propagatorsFromEnv() propagation.TextMapPropagator {
	configured := strings.TrimSpace(os.Getenv(propagatorsEnvVar))
	if configured == "" {
		configured = "tracecontext,baggage"
	}

	seen := make(map[string]struct{})
	propagators := make([]propagation.TextMapPropagator, 0, 2)
	none := false
	for rawName := range strings.SplitSeq(configured, ",") {
		name := strings.ToLower(strings.TrimSpace(rawName))
		if _, duplicate := seen[name]; name == "" || duplicate {
			continue
		}
		seen[name] = struct{}{}

		switch name {
		case "tracecontext":
			propagators = append(propagators, propagation.TraceContext{})
		case "baggage":
			propagators = append(propagators, propagation.Baggage{})
		case "b3":
			propagators = append(propagators, b3.New(b3.WithInjectEncoding(b3.B3SingleHeader)))
		case "b3multi":
			propagators = append(propagators, b3.New(b3.WithInjectEncoding(b3.B3MultipleHeader)))
		case "jaeger":
			propagators = append(propagators, jaeger.Jaeger{})
		case "ottrace":
			propagators = append(propagators, ot.OT{})
		case "none":
			none = true
		default:
			slog.Warn("unsupported OpenTelemetry propagator ignored", "name", name, "environment_variable", propagatorsEnvVar)
		}
	}
	if none {
		if len(propagators) > 0 {
			slog.Warn("OpenTelemetry propagator 'none' overrides other configured values", "environment_variable", propagatorsEnvVar)
		}
		return propagation.NewCompositeTextMapPropagator()
	}
	return propagation.NewCompositeTextMapPropagator(propagators...)
}
