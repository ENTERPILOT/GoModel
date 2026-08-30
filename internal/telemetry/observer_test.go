package telemetry

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	apiTrace "go.opentelemetry.io/otel/trace"

	"github.com/enterpilot/gomodel/internal/llmclient"
)

func TestObserverRecordsBufferedGenAISpanAndDuration(t *testing.T) {
	hooks, recorder, reader := newTestHooks(t)
	call := llmclient.RequestInfo{Provider: "openai-eu", ProviderType: "openai", Model: "gpt-5", Operation: "chat", Endpoint: "/chat/completions"}
	ctx := hooks.OnRequestStart(t.Context(), call)
	hooks.OnRequestEnd(ctx, response(call, http.StatusOK, 250*time.Millisecond, nil))

	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "chat gpt-5" || spans[0].SpanKind() != apiTrace.SpanKindClient {
		t.Fatalf("spans = %+v, want one CLIENT span named chat gpt-5", spans)
	}
	attrs := attributeMap(spans[0].Attributes())
	if attrs["gen_ai.provider.name"] != "openai" || attrs["gomodel.provider.name"] != "openai-eu" || attrs["gen_ai.request.model"] != "gpt-5" {
		t.Fatalf("span attributes = %+v", attrs)
	}
	if !hasMetric(t, reader, "gen_ai.client.operation.duration") {
		t.Fatal("duration metric not recorded")
	}
}

func TestObserverRecordsStreamingTTFCWithoutPrematureSpan(t *testing.T) {
	hooks, recorder, reader := newTestHooks(t)
	call := llmclient.RequestInfo{Provider: "anthropic", Model: "claude", Operation: "chat", Endpoint: "/messages", Stream: true}
	ctx := hooks.OnRequestStart(t.Context(), call)
	hooks.OnRequestEnd(ctx, response(call, http.StatusOK, 80*time.Millisecond, nil))

	if got := len(recorder.Ended()); got != 0 {
		t.Fatalf("ended spans = %d, want 0 at stream establishment", got)
	}
	if hasMetric(t, reader, "gen_ai.client.operation.time_to_first_chunk") {
		t.Fatal("stream establishment incorrectly recorded time-to-first-chunk")
	}
	hooks.OnStreamFirstChunk(ctx, response(call, http.StatusOK, 180*time.Millisecond, nil))
	if !hasMetric(t, reader, "gen_ai.client.operation.time_to_first_chunk") {
		t.Fatal("time-to-first-chunk metric not recorded")
	}
}

func TestObserverRecordsFailedStreamingOperation(t *testing.T) {
	hooks, recorder, reader := newTestHooks(t)
	call := llmclient.RequestInfo{Provider: "openai", Model: "gpt-5", Operation: "chat", Stream: true}
	ctx := hooks.OnRequestStart(t.Context(), call)
	hooks.OnRequestEnd(ctx, response(call, 0, 250*time.Millisecond, context.DeadlineExceeded))

	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "chat gpt-5" || spans[0].SpanKind() != apiTrace.SpanKindClient {
		t.Fatalf("spans = %+v, want one CLIENT failure span named chat gpt-5", spans)
	}
	if spans[0].Status().Code != codes.Error {
		t.Fatalf("span status = %v, want Error", spans[0].Status().Code)
	}
	if got := spans[0].EndTime().Sub(spans[0].StartTime()); got < 250*time.Millisecond {
		t.Fatalf("retrospective span duration = %v, want at least 250ms", got)
	}
	if got := attributeMap(spans[0].Attributes())["error.type"]; got != "timeout" {
		t.Fatalf("span error.type = %q, want timeout", got)
	}
	if !metricHasAttribute(t, reader, "gen_ai.client.operation.duration", "error.type", "timeout") {
		t.Fatal("failed streaming duration metric missing error.type=timeout")
	}
}

func TestObserverRecordsHTTPErrorType(t *testing.T) {
	hooks, recorder, _ := newTestHooks(t)
	call := llmclient.RequestInfo{Provider: "openai", Model: "gpt-5", Operation: "chat"}
	ctx := hooks.OnRequestStart(t.Context(), call)
	hooks.OnRequestEnd(ctx, response(call, http.StatusTooManyRequests, time.Millisecond, nil))

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	attrs := attributeMap(spans[0].Attributes())
	if attrs["error.type"] != "429" || attrs["http.response.status_code"] != "429" {
		t.Fatalf("span attributes = %+v, want error.type and status 429", attrs)
	}
}

func TestObserverDefersTelemetryWhenStreamIntentIsUncertain(t *testing.T) {
	hooks, recorder, reader := newTestHooks(t)
	call := llmclient.RequestInfo{Provider: "openai", Operation: "chat", Endpoint: "/chat/completions", StreamUncertain: true}
	ctx := hooks.OnRequestStart(t.Context(), call)
	hooks.OnRequestEnd(ctx, response(call, http.StatusOK, 80*time.Millisecond, nil))

	if len(recorder.Ended()) != 0 || hasMetric(t, reader, "gen_ai.client.operation.duration") {
		t.Fatal("uncertain passthrough request produced buffered telemetry")
	}
	call.Stream = true
	hooks.OnStreamFirstChunk(ctx, response(call, http.StatusOK, 180*time.Millisecond, nil))
	if !hasMetric(t, reader, "gen_ai.client.operation.time_to_first_chunk") {
		t.Fatal("response-confirmed stream did not record time-to-first-chunk")
	}
}

func TestObserverRecordsUncertainCallResolvedAsBuffered(t *testing.T) {
	hooks, recorder, reader := newTestHooks(t)
	call := llmclient.RequestInfo{Provider: "openai", Model: "gpt-5", Operation: "chat", StreamUncertain: true}
	ctx := hooks.OnRequestStart(t.Context(), call)
	// The passthrough client resolves the intent from the response before End.
	call.StreamUncertain = false
	hooks.OnRequestEnd(ctx, response(call, http.StatusOK, 300*time.Millisecond, nil))

	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "chat gpt-5" || spans[0].Status().Code == codes.Error {
		t.Fatalf("spans = %+v, want one successful CLIENT span synthesized at completion", spans)
	}
	if got := spans[0].EndTime().Sub(spans[0].StartTime()); got < 300*time.Millisecond {
		t.Fatalf("synthesized span duration = %v, want at least 300ms", got)
	}
	if !hasMetric(t, reader, "gen_ai.client.operation.duration") {
		t.Fatal("duration metric not recorded for a buffered response")
	}
}

func TestObserverRecordsStreamThatEndsBeforeFirstChunkAsFailure(t *testing.T) {
	hooks, recorder, reader := newTestHooks(t)
	call := llmclient.RequestInfo{Provider: "openai", Model: "gpt-5", Operation: "chat", Stream: true}
	ctx := hooks.OnRequestStart(t.Context(), call)
	hooks.OnRequestEnd(ctx, response(call, http.StatusOK, 80*time.Millisecond, nil))
	hooks.OnStreamEmpty(ctx, response(call, http.StatusOK, 120*time.Millisecond, io.EOF))

	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Status().Code != codes.Error {
		t.Fatalf("spans = %+v, want one failed CLIENT span", spans)
	}
	if got := attributeMap(spans[0].Attributes())["error.type"]; got != "empty_stream" {
		t.Fatalf("span error.type = %q, want empty_stream", got)
	}
	if !metricHasAttribute(t, reader, "gen_ai.client.operation.duration", "error.type", "empty_stream") {
		t.Fatal("duration metric missing error.type=empty_stream")
	}
	if hasMetric(t, reader, "gen_ai.client.operation.time_to_first_chunk") {
		t.Fatal("empty stream must not record time-to-first-chunk")
	}
}

func TestObserverSkipsNonInferenceCalls(t *testing.T) {
	hooks, recorder, reader := newTestHooks(t)
	call := llmclient.RequestInfo{Provider: "openai", Endpoint: "/models"}
	ctx := hooks.OnRequestStart(t.Context(), call)
	hooks.OnRequestEnd(ctx, response(call, http.StatusOK, 0, nil))

	if len(recorder.Ended()) != 0 || hasMetric(t, reader, "gen_ai.client.operation.duration") {
		t.Fatal("non-inference call produced GenAI telemetry")
	}
}

func TestSemanticProviderName(t *testing.T) {
	tests := []struct {
		providerType, provider, want string
	}{
		{"azure-eu", "fallback", "azure.ai.openai"},
		{"bedrock", "fallback", "aws.bedrock"},
		{"vertex", "fallback", "gcp.vertex_ai"},
		{"gemini", "fallback", "gcp.gemini"},
		{"xai", "fallback", "x_ai"},
		{"openai", "fallback", "openai"},
		{"", "anthropic_eu", "anthropic"},
		{"", "openaiclone", "unknown"},
		{"custom", "fallback", "unknown"},
	}
	for _, tt := range tests {
		if got := semanticProviderName(tt.providerType, tt.provider); got != tt.want {
			t.Errorf("semanticProviderName(%q, %q) = %q, want %q", tt.providerType, tt.provider, got, tt.want)
		}
	}
}

func newTestHooks(t *testing.T) (llmclient.Hooks, *tracetest.SpanRecorder, *metric.ManualReader) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	observer, err := newObserver(tp, mp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = mp.Shutdown(context.Background())
		_ = tp.Shutdown(context.Background())
	})
	return observer.hooks(), recorder, reader
}

func response(call llmclient.RequestInfo, status int, duration time.Duration, err error) llmclient.ResponseInfo {
	return llmclient.ResponseInfo{
		Provider:        call.Provider,
		ProviderType:    call.ProviderType,
		Model:           call.Model,
		Operation:       call.Operation,
		Endpoint:        call.Endpoint,
		Stream:          call.Stream,
		StreamUncertain: call.StreamUncertain,
		StatusCode:      status,
		Duration:        duration,
		Error:           err,
	}
}

func collect(t *testing.T, reader *metric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var data metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func hasMetric(t *testing.T, reader *metric.ManualReader, name string) bool {
	t.Helper()
	for _, scope := range collect(t, reader).ScopeMetrics {
		for _, candidate := range scope.Metrics {
			if candidate.Name == name {
				return true
			}
		}
	}
	return false
}

func metricHasAttribute(t *testing.T, reader *metric.ManualReader, name, key, value string) bool {
	t.Helper()
	for _, scope := range collect(t, reader).ScopeMetrics {
		for _, candidate := range scope.Metrics {
			histogram, ok := candidate.Data.(metricdata.Histogram[float64])
			if candidate.Name != name || !ok {
				continue
			}
			for _, point := range histogram.DataPoints {
				if got, ok := point.Attributes.Value(attribute.Key(key)); ok && got.AsString() == value {
					return true
				}
			}
		}
	}
	return false
}

func attributeMap(attrs []attribute.KeyValue) map[string]string {
	values := make(map[string]string, len(attrs))
	for _, attr := range attrs {
		values[string(attr.Key)] = attr.Value.String()
	}
	return values
}
