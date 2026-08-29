// Package telemetry provides GoModel's OpenTelemetry integration: OTLP trace
// and metric export for inbound HTTP requests and outbound provider calls.
//
// Enabling it is the only GoModel-specific switch (OTEL_ENABLED). Exporters,
// resource attributes, sampling, and propagation follow the standard OTEL_*
// environment variables read by the OpenTelemetry SDK.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/labstack/echo-opentelemetry"
	"github.com/labstack/echo/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdkMetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdkTrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

const (
	defaultServiceName  = "gomodel"
	instrumentationName = "github.com/enterpilot/gomodel/internal/telemetry"
	// closeTimeout bounds the final flush so an unreachable collector cannot
	// hold up gateway shutdown.
	closeTimeout = 10 * time.Second
)

// Span attributes that identify the caller or the host are never exported.
var privacyExcludedSpanAttributes = map[attribute.Key]struct{}{
	semconv.ClientAddressKey:      {},
	semconv.NetworkPeerAddressKey: {},
	semconv.NetworkPeerPortKey:    {},
	semconv.ServerAddressKey:      {},
	semconv.ServerPortKey:         {},
	semconv.UserAgentOriginalKey:  {},
}

// Host-derived metric dimensions are attacker-controlled (the Host header) and
// would let a client inflate metric cardinality.
var cardinalityExcludedMetricAttributes = map[attribute.Key]struct{}{
	semconv.ServerAddressKey: {},
	semconv.ServerPortKey:    {},
}

// Service owns one OpenTelemetry pipeline: providers, the HTTP middleware,
// and the provider-call observer. Core builds one per application generation
// so a reload picks up changed OTEL_* settings.
type Service struct {
	tracerProvider *sdkTrace.TracerProvider
	meterProvider  *sdkMetric.MeterProvider
	middleware     echo.MiddlewareFunc
	observer       *observer
}

// New configures OTLP traces and metrics from the standard OpenTelemetry
// environment variables, after exporting the YAML-configured settings into
// the environment for the SDK to read. Exporters are asynchronous, so the
// collector does not need to be reachable for this call to succeed.
// metricsEndpoint is the resolved Prometheus path, excluded from HTTP
// instrumentation together with the other operational endpoints.
func New(ctx context.Context, cfg config.OpenTelemetryConfig, metricsEndpoint string) (*Service, error) {
	exportEnvironment(cfg.Environment())

	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(defaultServiceName)),
		resource.WithTelemetrySDK(),
		resource.WithFromEnv(),
	)
	if err != nil {
		return nil, fmt.Errorf("detect OpenTelemetry resource: %w", err)
	}

	tp, err := newTracerProvider(ctx, res)
	if err != nil {
		return nil, fmt.Errorf("configure OpenTelemetry trace exporter: %w", err)
	}
	mp, err := newMeterProvider(ctx, res)
	if err != nil {
		_ = tp.Shutdown(ctx)
		return nil, fmt.Errorf("configure OpenTelemetry metric exporter: %w", err)
	}

	// Export failures surface through the SDK's global handler, which would
	// otherwise print at info level through the standard logger and be missed.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Warn("opentelemetry export failed", "error", err)
	}))

	service := &Service{tracerProvider: tp, meterProvider: mp}
	service.observer, err = newObserver(tp, mp)
	if err == nil {
		service.middleware, err = newMiddleware(tp, mp, propagatorsFromEnv(), metricsEndpoint)
	}
	if err != nil {
		_ = service.Close()
		return nil, err
	}

	warnPlaintextCredentials()
	slog.Info("opentelemetry enabled",
		"traces_exporter", exporterName("OTEL_TRACES_EXPORTER"),
		"metrics_exporter", exporterName("OTEL_METRICS_EXPORTER"),
	)
	return service, nil
}

// Middleware traces inbound HTTP requests and records the standard HTTP
// server metrics. It belongs at the outer edge of the middleware stack so a
// span covers the whole request.
func (s *Service) Middleware() echo.MiddlewareFunc { return s.middleware }

// Hooks instruments logical provider calls with GenAI client spans and
// metrics. Attach them to the provider factory before any provider exists.
func (s *Service) Hooks() llmclient.Hooks { return s.observer.hooks() }

// Close flushes pending telemetry and releases exporter resources. Both
// providers shut down concurrently so a stalled metric export cannot consume
// the deadline the final spans need.
func (s *Service) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	metricsDone := make(chan error, 1)
	go func() { metricsDone <- s.meterProvider.Shutdown(ctx) }()
	return errors.Join(s.tracerProvider.Shutdown(ctx), <-metricsDone)
}

func newMiddleware(tp *sdkTrace.TracerProvider, mp *sdkMetric.MeterProvider, propagators propagation.TextMapPropagator, metricsEndpoint string) (echo.MiddlewareFunc, error) {
	middleware, err := (echootel.Config{
		TracerProvider:      tp,
		MeterProvider:       mp,
		Propagators:         propagators,
		Skipper:             operationalEndpointSkipper(metricsEndpoint),
		SpanStartAttributes: privacySafeSpanAttributes,
		MetricAttributes:    boundedMetricAttributes,
	}).ToMiddleware()
	if err != nil {
		return nil, fmt.Errorf("configure OpenTelemetry HTTP middleware: %w", err)
	}
	return middleware, nil
}

// operationalEndpointSkipper leaves health probes, Prometheus scrapes, and
// pprof out of HTTP telemetry; they are polled, not served to users.
func operationalEndpointSkipper(metricsEndpoint string) func(*echo.Context) bool {
	return func(c *echo.Context) bool {
		requestPath := strings.TrimSuffix(c.Request().URL.Path, "/")
		return requestPath == "/health" || requestPath == "/health/ready" ||
			requestPath == "/metrics" || requestPath == metricsEndpoint ||
			requestPath == "/debug/pprof" || strings.HasPrefix(requestPath, "/debug/pprof/")
	}
}

func privacySafeSpanAttributes(_ *echo.Context, _ *echootel.Values, attrs []attribute.KeyValue) []attribute.KeyValue {
	return filterAttributes(attrs, privacyExcludedSpanAttributes)
}

func boundedMetricAttributes(_ *echo.Context, values *echootel.Values) []attribute.KeyValue {
	return filterAttributes(values.MetricAttributes(), cardinalityExcludedMetricAttributes)
}

func filterAttributes(attrs []attribute.KeyValue, excluded map[attribute.Key]struct{}) []attribute.KeyValue {
	filtered := make([]attribute.KeyValue, 0, len(attrs))
	for _, attr := range attrs {
		if _, skip := excluded[attr.Key]; !skip {
			filtered = append(filtered, attr)
		}
	}
	return filtered
}

// warnPlaintextCredentials flags export headers (typically an authorization
// token) configured together with an http:// collector endpoint: they would
// cross the network in clear text.
func warnPlaintextCredentials() {
	headersSet := false
	for _, key := range []string{"OTEL_EXPORTER_OTLP_HEADERS", "OTEL_EXPORTER_OTLP_TRACES_HEADERS", "OTEL_EXPORTER_OTLP_METRICS_HEADERS"} {
		headersSet = headersSet || strings.TrimSpace(os.Getenv(key)) != ""
	}
	if !headersSet {
		return
	}
	for _, key := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"} {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(os.Getenv(key))), "http://") {
			slog.Warn("opentelemetry export headers are sent over plaintext HTTP; use an https:// collector endpoint for credentials",
				"environment_variable", key)
			return
		}
	}
}
