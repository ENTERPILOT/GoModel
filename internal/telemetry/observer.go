package telemetry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	apiMetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

var durationBuckets = []float64{0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28, 2.56, 5.12, 10.24, 20.48, 40.96, 81.92}

// observer instruments logical provider calls with the OpenTelemetry GenAI
// semantic conventions. It never records credentials or error messages, and
// records prompts and responses only when content capture is enabled.
//
// Buffered calls get a CLIENT span opened at request start and closed at
// completion. When the gateway traces the call (core.GenerationTrace), a
// successful span is parked until the gateway decodes the response, which
// adds the response model and token usage; the span keeps its original end
// time. Streams traced by the gateway get a span that ends with the stream.
// Other streams get no span at establishment — the client only knows that
// response headers arrived — and record time to first chunk instead. A
// stream that fails to establish, or ends before its first byte, always gets
// a failure span so the error is traced.
type observer struct {
	tracer           trace.Tracer
	duration         apiMetric.Float64Histogram
	timeToFirstChunk apiMetric.Float64Histogram
	emptyResponses   apiMetric.Int64Counter
	captureContent   bool
}

type callState struct {
	spanName   string
	attrs      []attribute.KeyValue
	span       trace.Span
	generation *core.GenerationTrace
	ended      atomic.Bool
}

// endSpan ends the call's span once: a traced stream can fail after its span
// was parked, and the parked completion must then do nothing.
func (s *callState) endSpan(options ...trace.SpanEndOption) {
	if s.span != nil && s.ended.CompareAndSwap(false, true) {
		s.span.End(options...)
	}
}

type callStateKey struct{}

func newObserver(tp trace.TracerProvider, mp apiMetric.MeterProvider, captureContent bool) (*observer, error) {
	meter := mp.Meter(instrumentationName)
	duration, err := meter.Float64Histogram(
		"gen_ai.client.operation.duration",
		apiMetric.WithDescription("GenAI operation duration."),
		apiMetric.WithUnit("s"),
		apiMetric.WithExplicitBucketBoundaries(durationBuckets...),
	)
	if err != nil {
		return nil, err
	}
	ttfc, err := meter.Float64Histogram(
		"gen_ai.client.operation.time_to_first_chunk",
		apiMetric.WithDescription("Time to receive the first chunk from a streaming GenAI operation."),
		apiMetric.WithUnit("s"),
		apiMetric.WithExplicitBucketBoundaries(durationBuckets...),
	)
	if err != nil {
		return nil, err
	}
	emptyResponses, err := meter.Int64Counter(
		"gomodel.client.empty_responses",
		apiMetric.WithDescription("Provider responses that returned 200 without choices, output, or usage."),
		apiMetric.WithUnit("{response}"),
	)
	if err != nil {
		return nil, err
	}
	return &observer{
		tracer:           tp.Tracer(instrumentationName),
		duration:         duration,
		timeToFirstChunk: ttfc,
		emptyResponses:   emptyResponses,
		captureContent:   captureContent,
	}, nil
}

func (o *observer) hooks() llmclient.Hooks {
	return llmclient.Hooks{
		OnRequestStart:     o.start,
		OnRequestEnd:       o.end,
		OnStreamFirstChunk: o.firstChunk,
		OnStreamEmpty:      o.streamEmpty,
		OnEmptyResponse:    o.emptyResponse,
	}
}

// start records call attributes and opens the client span for buffered
// inference and for streams the gateway traces. Calls without an operation
// are not inference (model listings, for example) and produce no telemetry.
func (o *observer) start(ctx context.Context, info llmclient.RequestInfo) context.Context {
	operation := strings.TrimSpace(info.Operation)
	if operation == "" {
		return ctx
	}
	state := &callState{
		spanName:   operationSpanName(operation, info.Model),
		attrs:      callAttributes(info, operation),
		generation: core.GenerationTraceFromContext(ctx),
	}
	if !info.StreamUncertain && (!info.Stream || state.generation != nil) {
		ctx, state.span = o.tracer.Start(ctx, state.spanName,
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(state.attrs...),
		)
	}
	return context.WithValue(ctx, callStateKey{}, state)
}

// end completes the duration metric and the span. A successful call is
// parked for the gateway's outcome when it traces the call. For streaming
// calls completion here only means that response headers arrived, so a
// successful stream records nothing yet.
func (o *observer) end(ctx context.Context, info llmclient.ResponseInfo) {
	state, ok := ctx.Value(callStateKey{}).(*callState)
	if !ok {
		return
	}
	errorType := resultErrorType(info)
	if (info.Stream || info.StreamUncertain) && errorType == "" {
		if state.span != nil {
			o.park(state, info)
		}
		return
	}

	attrs := slices.Clone(state.attrs)
	if errorType != "" {
		attrs = append(attrs, attribute.String("error.type", errorType))
	}
	duration := max(info.Duration, 0)
	o.duration.Record(ctx, duration.Seconds(), apiMetric.WithAttributes(attrs...))

	if state.span == nil {
		// No span was opened at start (a stream that failed to establish, or
		// an uncertain passthrough call the response resolved as buffered);
		// synthesize one covering the measured duration.
		endTime := time.Now()
		_, span := o.tracer.Start(ctx, state.spanName,
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(state.attrs...),
			trace.WithTimestamp(endTime.Add(-duration)),
		)
		setResultAttributes(span, info, errorType)
		span.End(trace.WithTimestamp(endTime))
		return
	}
	if errorType == "" {
		o.park(state, info, trace.WithTimestamp(time.Now()))
		return
	}
	setResultAttributes(state.span, info, errorType)
	state.endSpan()
}

// park leaves a successful call's span open until the gateway finishes its
// generation trace with the decoded outcome; untraced calls end at once.
// Buffered calls pass their end time so the span keeps the provider latency.
func (o *observer) park(state *callState, info llmclient.ResponseInfo, endOptions ...trace.SpanEndOption) {
	setResultAttributes(state.span, info, "")
	if state.generation == nil {
		state.endSpan(endOptions...)
		return
	}
	state.generation.Park(func(outcome core.GenerationOutcome) {
		if attrs := o.outcomeAttributes(outcome); len(attrs) > 0 {
			state.span.SetAttributes(attrs...)
		}
		state.endSpan(endOptions...)
	})
}

func setResultAttributes(span trace.Span, info llmclient.ResponseInfo, errorType string) {
	if info.StatusCode > 0 {
		span.SetAttributes(attribute.Int("http.response.status_code", info.StatusCode))
	}
	if errorType != "" {
		span.SetAttributes(attribute.String("error.type", errorType))
		span.SetStatus(codes.Error, "")
	}
}

// firstChunk records streaming latency once the response body first returns
// bytes.
func (o *observer) firstChunk(ctx context.Context, info llmclient.ResponseInfo) {
	state, ok := ctx.Value(callStateKey{}).(*callState)
	if !ok || !info.Stream || info.Error != nil || info.StatusCode < 1 || info.StatusCode >= http.StatusBadRequest {
		return
	}
	o.timeToFirstChunk.Record(ctx, info.Duration.Seconds(), apiMetric.WithAttributes(state.attrs...))
}

// errEmptyStream classifies a stream that was established but ended before
// its first byte. It is a failure from the caller's point of view.
var errEmptyStream = errors.New("stream ended before its first chunk")

// streamEmpty completes a stream that never delivered as a failed call: the
// duration metric and a retrospective span carry error.type=empty_stream (or
// the transport error class when the read failed outright).
func (o *observer) streamEmpty(ctx context.Context, info llmclient.ResponseInfo) {
	if info.Error == nil || errors.Is(info.Error, io.EOF) {
		info.Error = errEmptyStream
	}
	o.end(ctx, info)
}

// emptyResponse counts a 200 response without choices, output, or usage. The
// call's span and duration already recorded it as a success, so the counter
// carries the reason as error.type for alerting.
func (o *observer) emptyResponse(ctx context.Context, info llmclient.EmptyResponseInfo) {
	attrs := []attribute.KeyValue{
		attribute.String("gen_ai.operation.name", info.Operation),
		attribute.String("gen_ai.provider.name", semanticProviderName(info.ProviderType, info.Provider)),
		attribute.String("gomodel.provider.name", info.Provider),
		attribute.String("error.type", info.Reason),
	}
	if model := modelName(info.Model); model != "" {
		attrs = append(attrs, attribute.String("gen_ai.request.model", model))
	}
	o.emptyResponses.Add(ctx, 1, apiMetric.WithAttributes(attrs...))
}

func callAttributes(info llmclient.RequestInfo, operation string) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("gen_ai.operation.name", operation),
		attribute.String("gen_ai.provider.name", semanticProviderName(info.ProviderType, info.Provider)),
		attribute.String("gomodel.provider.name", info.Provider),
	}
	if model := modelName(info.Model); model != "" {
		attrs = append(attrs, attribute.String("gen_ai.request.model", model))
	}
	if info.Stream {
		attrs = append(attrs, attribute.Bool("gen_ai.request.stream", true))
	}
	return attrs
}

func modelName(model string) string {
	model = strings.TrimSpace(model)
	if model == "unknown" {
		return ""
	}
	return model
}

func operationSpanName(operation, model string) string {
	if model = modelName(model); model != "" {
		return operation + " " + model
	}
	return operation
}

// semanticProviderName maps a provider type (or, failing that, the configured
// provider name) onto the gen_ai.provider.name well-known values. Anything
// else is "unknown"; the exact configured name is always exported separately
// as gomodel.provider.name.
func semanticProviderName(providerType, provider string) string {
	name := strings.ToLower(strings.TrimSpace(providerType))
	if name == "" {
		name = strings.ToLower(strings.TrimSpace(provider))
	}
	switch {
	case strings.HasPrefix(name, "azure"):
		return "azure.ai.openai"
	case strings.HasPrefix(name, "bedrock"):
		return "aws.bedrock"
	case strings.HasPrefix(name, "vertex"):
		return "gcp.vertex_ai"
	case strings.HasPrefix(name, "gemini"):
		return "gcp.gemini"
	case hasProviderPrefix(name, "xai"):
		return "x_ai"
	}
	for _, known := range []string{"openai", "anthropic", "cohere", "deepseek", "groq", "ollama"} {
		if hasProviderPrefix(name, known) {
			return known
		}
	}
	return "unknown"
}

// hasProviderPrefix matches "openai", "openai-eu", and "openai_eu" but not
// "openaiclone".
func hasProviderPrefix(name, known string) bool {
	return name == known || strings.HasPrefix(name, known+"-") || strings.HasPrefix(name, known+"_")
}

func resultErrorType(info llmclient.ResponseInfo) string {
	switch {
	case info.StatusCode >= http.StatusBadRequest:
		return strconv.Itoa(info.StatusCode)
	case info.Error == nil:
		return ""
	case errors.Is(info.Error, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(info.Error, errEmptyStream):
		return "empty_stream"
	default:
		return "network_error"
	}
}
