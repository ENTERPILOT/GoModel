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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestObserverRecordsBufferedGenAISpanAndDuration(t *testing.T) {
	hooks, recorder, reader := newTestHooks(t)
	call := llmclient.RequestInfo{Provider: "openai-eu", ProviderType: "openai", Model: "gpt-5", Operation: "chat", Endpoint: "/chat/completions"}
	ctx := hooks.OnRequestStart(t.Context(), call)
	hooks.OnRequestEnd(ctx, response(call, http.StatusOK, 250*time.Millisecond, nil))

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	require.Equal(t, "chat gpt-5", spans[0].Name())
	require.Equal(t, apiTrace.SpanKindClient, spans[0].SpanKind())

	attrs := attributeMap(spans[0].Attributes())
	require.Equal(t, "openai", attrs["gen_ai.provider.name"])
	require.Equal(t, "openai-eu", attrs["gomodel.provider.name"])
	require.Equal(t, "gpt-5", attrs["gen_ai.request.model"], "span attributes = %+v", attrs)
	require.True(t, hasMetric(t, reader, "gen_ai.client.operation.duration"))
}

func TestObserverRecordsStreamingTTFCWithoutPrematureSpan(t *testing.T) {
	hooks, recorder, reader := newTestHooks(t)
	call := llmclient.RequestInfo{Provider: "anthropic", Model: "claude", Operation: "chat", Endpoint: "/messages", Stream: true}
	ctx := hooks.OnRequestStart(t.Context(), call)
	hooks.OnRequestEnd(ctx, response(call, http.StatusOK, 80*time.Millisecond, nil))
	require.Empty(t, recorder.Ended())
	require.False(t, hasMetric(t, reader, "gen_ai.client.operation.time_to_first_chunk"))

	hooks.OnStreamFirstChunk(ctx, response(call, http.StatusOK, 180*time.Millisecond, nil))
	require.True(t, hasMetric(t, reader, "gen_ai.client.operation.time_to_first_chunk"))
}

func TestObserverRecordsFailedStreamingOperation(t *testing.T) {
	hooks, recorder, reader := newTestHooks(t)
	call := llmclient.RequestInfo{Provider: "openai", Model: "gpt-5", Operation: "chat", Stream: true}
	ctx := hooks.OnRequestStart(t.Context(), call)
	hooks.OnRequestEnd(ctx, response(call, 0, 250*time.Millisecond, context.DeadlineExceeded))

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	require.Equal(t, "chat gpt-5", spans[0].Name())
	require.Equal(t, apiTrace.SpanKindClient, spans[0].SpanKind())
	require.Equal(t, codes.Error, spans[0].Status().Code)
	require.GreaterOrEqual(t, spans[0].EndTime().Sub(spans[0].StartTime()), 250*time.Millisecond)
	require.Equal(t, "timeout", attributeMap(spans[0].Attributes())["error.type"])
	require.True(t, metricHasAttribute(t, reader, "gen_ai.client.operation.duration", "error.type", "timeout"))
}

func TestObserverRecordsHTTPErrorType(t *testing.T) {
	hooks, recorder, _ := newTestHooks(t)
	call := llmclient.RequestInfo{Provider: "openai", Model: "gpt-5", Operation: "chat"}
	ctx := hooks.OnRequestStart(t.Context(), call)
	hooks.OnRequestEnd(ctx, response(call, http.StatusTooManyRequests, time.Millisecond, nil))

	spans := recorder.Ended()
	require.Len(t, spans, 1)

	attrs := attributeMap(spans[0].Attributes())
	require.Equal(t, "429", attrs["error.type"])
	require.Equal(t, "429", attrs["http.response.status_code"], "span attributes = %+v, want error.type and status 429", attrs)
}

func TestObserverCountsEmptyResponses(t *testing.T) {
	hooks, _, reader := newTestHooks(t)
	info := llmclient.EmptyResponseInfo{Provider: "openai-eu", ProviderType: "openai", Model: "gpt-5", Operation: "chat", Reason: llmclient.EmptyReasonNoChoices}
	hooks.OnEmptyResponse(t.Context(), info)
	hooks.OnEmptyResponse(t.Context(), info)

	counter, ok := findMetric(t, reader, "gomodel.client.empty_responses")
	require.True(t, ok, "gomodel.client.empty_responses was not recorded")
	sum, ok := counter.Data.(metricdata.Sum[int64])
	require.True(t, ok, "empty_responses data = %T, want int64 sum", counter.Data)
	require.Len(t, sum.DataPoints, 1)

	point := sum.DataPoints[0]
	attrs := attributeMap(point.Attributes.ToSlice())
	require.Equal(t, int64(2), point.Value)
	require.Equal(t, "no_choices", attrs["error.type"])
	require.Equal(t, "openai", attrs["gen_ai.provider.name"])
	require.Equal(t, "openai-eu", attrs["gomodel.provider.name"])
	require.Equal(t, "gpt-5", attrs["gen_ai.request.model"])
}

func TestObserverDefersTelemetryWhenStreamIntentIsUncertain(t *testing.T) {
	hooks, recorder, reader := newTestHooks(t)
	call := llmclient.RequestInfo{Provider: "openai", Operation: "chat", Endpoint: "/chat/completions", StreamUncertain: true}
	ctx := hooks.OnRequestStart(t.Context(), call)
	hooks.OnRequestEnd(ctx, response(call, http.StatusOK, 80*time.Millisecond, nil))

	require.Empty(t, recorder.Ended())
	require.False(t, hasMetric(t, reader, "gen_ai.client.operation.duration"))

	call.Stream = true
	hooks.OnStreamFirstChunk(ctx, response(call, http.StatusOK, 180*time.Millisecond, nil))
	require.True(t, hasMetric(t, reader, "gen_ai.client.operation.time_to_first_chunk"))
}

func TestObserverRecordsUncertainCallResolvedAsBuffered(t *testing.T) {
	hooks, recorder, reader := newTestHooks(t)
	call := llmclient.RequestInfo{Provider: "openai", Model: "gpt-5", Operation: "chat", StreamUncertain: true}
	ctx := hooks.OnRequestStart(t.Context(), call)
	// The passthrough client resolves the intent from the response before End.
	call.StreamUncertain = false
	hooks.OnRequestEnd(ctx, response(call, http.StatusOK, 300*time.Millisecond, nil))

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	require.Equal(t, "chat gpt-5", spans[0].Name())
	require.NotEqual(t, codes.Error, spans[0].Status().Code)
	require.GreaterOrEqual(t, spans[0].EndTime().Sub(spans[0].StartTime()), 300*time.Millisecond)
	require.True(t, hasMetric(t, reader, "gen_ai.client.operation.duration"))
}

func TestObserverRecordsStreamThatEndsBeforeFirstChunkAsFailure(t *testing.T) {
	hooks, recorder, reader := newTestHooks(t)
	call := llmclient.RequestInfo{Provider: "openai", Model: "gpt-5", Operation: "chat", Stream: true}
	ctx := hooks.OnRequestStart(t.Context(), call)
	hooks.OnRequestEnd(ctx, response(call, http.StatusOK, 80*time.Millisecond, nil))
	hooks.OnStreamEmpty(ctx, response(call, http.StatusOK, 120*time.Millisecond, io.EOF))

	spans := recorder.Ended()
	require.Len(t, spans, 1)
	require.Equal(t, codes.Error, spans[0].Status().Code)
	require.Equal(t, "empty_stream", attributeMap(spans[0].Attributes())["error.type"])
	require.True(t, metricHasAttribute(t, reader, "gen_ai.client.operation.duration", "error.type", "empty_stream"))
	require.False(t, hasMetric(t, reader, "gen_ai.client.operation.time_to_first_chunk"))
}

func TestObserverSkipsNonInferenceCalls(t *testing.T) {
	hooks, recorder, reader := newTestHooks(t)
	call := llmclient.RequestInfo{Provider: "openai", Endpoint: "/models"}
	ctx := hooks.OnRequestStart(t.Context(), call)
	hooks.OnRequestEnd(ctx, response(call, http.StatusOK, 0, nil))

	require.Empty(t, recorder.Ended())
	require.False(t, hasMetric(t, reader, "gen_ai.client.operation.duration"))
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
		assert.Equal(t, tt.want, semanticProviderName(tt.providerType, tt.provider), "semanticProviderName(%q, %q)", tt.providerType, tt.provider)
	}
}

func newTestHooks(t *testing.T) (llmclient.Hooks, *tracetest.SpanRecorder, *metric.ManualReader) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	observer, err := newObserver(tp, mp)
	require.NoError(t, err)

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
	err := reader.Collect(t.Context(), &data)
	require.NoError(t, err)

	return data
}

func findMetric(t *testing.T, reader *metric.ManualReader, name string) (metricdata.Metrics, bool) {
	t.Helper()
	for _, scope := range collect(t, reader).ScopeMetrics {
		for _, candidate := range scope.Metrics {
			if candidate.Name == name {
				return candidate, true
			}
		}
	}
	return metricdata.Metrics{}, false
}

func hasMetric(t *testing.T, reader *metric.ManualReader, name string) bool {
	t.Helper()
	_, ok := findMetric(t, reader, name)
	return ok
}

func metricHasAttribute(t *testing.T, reader *metric.ManualReader, name, key, value string) bool {
	t.Helper()
	found, ok := findMetric(t, reader, name)
	if !ok {
		return false
	}
	histogram, ok := found.Data.(metricdata.Histogram[float64])
	if !ok {
		return false
	}
	for _, point := range histogram.DataPoints {
		if got, ok := point.Attributes.Value(attribute.Key(key)); ok && got.AsString() == value {
			return true
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
