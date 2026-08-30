package telemetry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	sdkMetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdkTrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/enterpilot/gomodel/internal/llmclient"
)

// The synchronous cost a request pays on the hot path with export enabled.
// Exporting itself happens on SDK background goroutines (batch span
// processor, periodic metric reader), so a no-op exporter isolates what the
// request goroutine actually spends.
func newBenchService(b *testing.B) *Service {
	b.Helper()
	tp := sdkTrace.NewTracerProvider(sdkTrace.WithBatcher(tracetest.NewNoopExporter()))
	mp := sdkMetric.NewMeterProvider(sdkMetric.WithReader(sdkMetric.NewPeriodicReader(&dropMetricExporter{})))
	observer, err := newObserver(tp, mp)
	if err != nil {
		b.Fatal(err)
	}
	middleware, err := newMiddleware(tp, mp, propagatorsFromEnv(), "/metrics")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		_ = mp.Shutdown(context.Background())
		_ = tp.Shutdown(context.Background())
	})
	return &Service{tracerProvider: tp, meterProvider: mp, middleware: middleware, observer: observer}
}

func BenchmarkProviderCallBuffered(b *testing.B) {
	hooks := newBenchService(b).Hooks()
	call := llmclient.RequestInfo{Provider: "openai-eu", ProviderType: "openai", Model: "gpt-5", Operation: "chat", Endpoint: "/chat/completions"}
	result := llmclient.ResponseInfo{Provider: "openai-eu", ProviderType: "openai", Model: "gpt-5", Operation: "chat", StatusCode: http.StatusOK, Duration: 250 * time.Millisecond}
	b.ReportAllocs()
	for b.Loop() {
		ctx := hooks.OnRequestStart(context.Background(), call)
		hooks.OnRequestEnd(ctx, result)
	}
}

func BenchmarkProviderCallStreaming(b *testing.B) {
	hooks := newBenchService(b).Hooks()
	call := llmclient.RequestInfo{Provider: "openai-eu", ProviderType: "openai", Model: "gpt-5", Operation: "chat", Endpoint: "/chat/completions", Stream: true}
	result := llmclient.ResponseInfo{Provider: "openai-eu", ProviderType: "openai", Model: "gpt-5", Operation: "chat", Stream: true, StatusCode: http.StatusOK, Duration: 250 * time.Millisecond}
	b.ReportAllocs()
	for b.Loop() {
		ctx := hooks.OnRequestStart(context.Background(), call)
		hooks.OnRequestEnd(ctx, result)
		hooks.OnStreamFirstChunk(ctx, result)
	}
}

func BenchmarkHTTPMiddleware(b *testing.B) {
	service := newBenchService(b)
	e := echo.New()
	e.Use(service.Middleware())
	e.POST("/v1/chat/completions", func(c *echo.Context) error { return c.String(http.StatusOK, "ok") })
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	b.ReportAllocs()
	for b.Loop() {
		e.ServeHTTP(httptest.NewRecorder(), req)
	}
}

func BenchmarkHTTPNoMiddleware(b *testing.B) {
	e := echo.New()
	e.POST("/v1/chat/completions", func(c *echo.Context) error { return c.String(http.StatusOK, "ok") })
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	b.ReportAllocs()
	for b.Loop() {
		e.ServeHTTP(httptest.NewRecorder(), req)
	}
}

type dropMetricExporter struct{}

func (*dropMetricExporter) Temporality(kind sdkMetric.InstrumentKind) metricdata.Temporality {
	return sdkMetric.DefaultTemporalitySelector(kind)
}
func (*dropMetricExporter) Aggregation(kind sdkMetric.InstrumentKind) sdkMetric.Aggregation {
	return sdkMetric.DefaultAggregationSelector(kind)
}
func (*dropMetricExporter) Export(context.Context, *metricdata.ResourceMetrics) error { return nil }
func (*dropMetricExporter) ForceFlush(context.Context) error                          { return nil }
func (*dropMetricExporter) Shutdown(context.Context) error                            { return nil }
