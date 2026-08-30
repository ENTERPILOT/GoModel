package telemetry

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdkMetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdkTrace "go.opentelemetry.io/otel/sdk/trace"
)

// Exporter selection follows the OpenTelemetry SDK environment specification:
// OTEL_{TRACES,METRICS}_EXPORTER picks "otlp" (default) or "none", and
// OTEL_EXPORTER_OTLP_{TRACES,METRICS}_PROTOCOL overrides
// OTEL_EXPORTER_OTLP_PROTOCOL per signal ("http/protobuf" by default).

func newTracerProvider(ctx context.Context, res *resource.Resource) (*sdkTrace.TracerProvider, error) {
	options := []sdkTrace.TracerProviderOption{sdkTrace.WithResource(res)}
	switch exporterName("OTEL_TRACES_EXPORTER") {
	case "none":
	case "otlp":
		exporter, err := newOTLPSpanExporter(ctx, signalProtocol("TRACES"))
		if err != nil {
			return nil, err
		}
		options = append(options, sdkTrace.WithBatcher(exporter))
	default:
		return nil, fmt.Errorf("OTEL_TRACES_EXPORTER must be otlp or none")
	}
	return sdkTrace.NewTracerProvider(options...), nil
}

func newMeterProvider(ctx context.Context, res *resource.Resource) (*sdkMetric.MeterProvider, error) {
	options := []sdkMetric.Option{sdkMetric.WithResource(res)}
	switch exporterName("OTEL_METRICS_EXPORTER") {
	case "none":
	case "otlp":
		exporter, err := newOTLPMetricExporter(ctx, signalProtocol("METRICS"))
		if err != nil {
			return nil, err
		}
		options = append(options, sdkMetric.WithReader(sdkMetric.NewPeriodicReader(exporter)))
	default:
		return nil, fmt.Errorf("OTEL_METRICS_EXPORTER must be otlp or none")
	}
	return sdkMetric.NewMeterProvider(options...), nil
}

func newOTLPSpanExporter(ctx context.Context, protocol string) (sdkTrace.SpanExporter, error) {
	switch protocol {
	case "grpc":
		return otlptracegrpc.New(ctx)
	case "http/protobuf":
		return otlptracehttp.New(ctx)
	default:
		return nil, invalidProtocol(protocol)
	}
}

func newOTLPMetricExporter(ctx context.Context, protocol string) (sdkMetric.Exporter, error) {
	switch protocol {
	case "grpc":
		return otlpmetricgrpc.New(ctx)
	case "http/protobuf":
		return otlpmetrichttp.New(ctx)
	default:
		return nil, invalidProtocol(protocol)
	}
}

func exporterName(key string) string {
	if value := envValue(key); value != "" {
		return value
	}
	return "otlp"
}

func signalProtocol(signal string) string {
	if value := envValue("OTEL_EXPORTER_OTLP_" + signal + "_PROTOCOL"); value != "" {
		return value
	}
	if value := envValue("OTEL_EXPORTER_OTLP_PROTOCOL"); value != "" {
		return value
	}
	return "http/protobuf"
}

func envValue(key string) string {
	return strings.ToLower(strings.TrimSpace(os.Getenv(key)))
}

func invalidProtocol(protocol string) error {
	return fmt.Errorf("OTLP protocol %q is unsupported; use grpc or http/protobuf", protocol)
}

// plaintextCredentialSignals lists the exporting signals ("TRACES",
// "METRICS") whose effective configuration would send export headers —
// typically an authorization token — over an unencrypted connection to a
// non-loopback host. It resolves the same defaults the SDK applies: an unset
// http/protobuf endpoint is http://localhost:4318, and gRPC is plaintext only
// with an http:// endpoint or OTEL_EXPORTER_OTLP_INSECURE=true.
func plaintextCredentialSignals() []string {
	var signals []string
	for _, signal := range []string{"TRACES", "METRICS"} {
		if exporterName("OTEL_"+signal+"_EXPORTER") == "none" || signalEnv(signal, "HEADERS") == "" {
			continue
		}
		endpoint := signalEnv(signal, "ENDPOINT")
		plaintext := false
		switch signalProtocol(signal) {
		case "grpc":
			plaintext = strings.HasPrefix(endpoint, "http://") ||
				(!strings.Contains(endpoint, "://") && signalEnv(signal, "INSECURE") == "true")
		default:
			plaintext = endpoint == "" || strings.HasPrefix(endpoint, "http://")
		}
		if plaintext && !isLoopbackEndpoint(endpoint) {
			signals = append(signals, signal)
		}
	}
	return signals
}

// signalEnv reads OTEL_EXPORTER_OTLP_<SIGNAL>_<suffix>, falling back to the
// generic OTEL_EXPORTER_OTLP_<suffix>, the way the SDK resolves per-signal
// exporter settings.
func signalEnv(signal, suffix string) string {
	if value := envValue("OTEL_EXPORTER_OTLP_" + signal + "_" + suffix); value != "" {
		return value
	}
	return envValue("OTEL_EXPORTER_OTLP_" + suffix)
}

// isLoopbackEndpoint reports whether endpoint (possibly empty, meaning the
// SDK's localhost default) targets the local machine, where a plaintext
// connection never leaves the host.
func isLoopbackEndpoint(endpoint string) bool {
	if endpoint == "" {
		return true
	}
	host := strings.TrimPrefix(strings.TrimPrefix(endpoint, "http://"), "https://")
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
