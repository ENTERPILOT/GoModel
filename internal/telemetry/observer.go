package telemetry

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	apiMetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/enterpilot/gomodel/internal/llmclient"
)

var durationBuckets = []float64{0.01, 0.02, 0.04, 0.08, 0.16, 0.32, 0.64, 1.28, 2.56, 5.12, 10.24, 20.48, 40.96, 81.92}

// observer instruments logical provider calls with the OpenTelemetry GenAI
// semantic conventions. It never records prompts, responses, credentials, or
// error messages.
//
// Buffered calls get a CLIENT span opened at request start and closed at
// completion. Streaming calls intentionally get no span at stream
// establishment — the client only knows that response headers arrived — and
// record time to first chunk instead; a stream that fails to establish still
// gets a retrospective failure span so the error is traced.
type observer struct {
	tracer           trace.Tracer
	duration         apiMetric.Float64Histogram
	timeToFirstChunk apiMetric.Float64Histogram
}

type callState struct {
	spanName string
	attrs    []attribute.KeyValue
	span     trace.Span
}

type callStateKey struct{}

func newObserver(tp trace.TracerProvider, mp apiMetric.MeterProvider) (*observer, error) {
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
	return &observer{
		tracer:           tp.Tracer(instrumentationName),
		duration:         duration,
		timeToFirstChunk: ttfc,
	}, nil
}

func (o *observer) hooks() llmclient.Hooks {
	return llmclient.Hooks{
		OnRequestStart:     o.start,
		OnRequestEnd:       o.end,
		OnStreamFirstChunk: o.firstChunk,
	}
}

// start records call attributes and, for buffered inference, opens the
// client span. Calls without an operation are not inference (model listings,
// for example) and produce no telemetry.
func (o *observer) start(ctx context.Context, info llmclient.RequestInfo) context.Context {
	operation := strings.TrimSpace(info.Operation)
	if operation == "" {
		return ctx
	}
	state := callState{
		spanName: operationSpanName(operation, info.Model),
		attrs:    callAttributes(info, operation),
	}
	if !info.Stream && !info.StreamUncertain {
		ctx, state.span = o.tracer.Start(ctx, state.spanName,
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(state.attrs...),
		)
	}
	return context.WithValue(ctx, callStateKey{}, state)
}

// end completes buffered spans and the duration metric. For streaming calls
// completion here only means that response headers arrived, so a successful
// stream records nothing yet.
func (o *observer) end(ctx context.Context, info llmclient.ResponseInfo) {
	state, ok := ctx.Value(callStateKey{}).(callState)
	if !ok {
		return
	}
	errorType := resultErrorType(info)
	if (info.Stream || info.StreamUncertain) && errorType == "" {
		return
	}

	attrs := slices.Clone(state.attrs)
	if errorType != "" {
		attrs = append(attrs, attribute.String("error.type", errorType))
	}
	duration := max(info.Duration, 0)
	o.duration.Record(ctx, duration.Seconds(), apiMetric.WithAttributes(attrs...))

	span := state.span
	endOptions := []trace.SpanEndOption(nil)
	if span == nil {
		// A stream that failed to establish never had a span; synthesize one
		// covering the measured duration so the failure is still traced.
		endTime := time.Now()
		_, span = o.tracer.Start(ctx, state.spanName,
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(state.attrs...),
			trace.WithTimestamp(endTime.Add(-duration)),
		)
		endOptions = append(endOptions, trace.WithTimestamp(endTime))
	}
	if info.StatusCode > 0 {
		span.SetAttributes(attribute.Int("http.response.status_code", info.StatusCode))
	}
	if errorType != "" {
		span.SetAttributes(attribute.String("error.type", errorType))
		span.SetStatus(codes.Error, "")
	}
	span.End(endOptions...)
}

// firstChunk records streaming latency once the response body first returns
// bytes.
func (o *observer) firstChunk(ctx context.Context, info llmclient.ResponseInfo) {
	state, ok := ctx.Value(callStateKey{}).(callState)
	if !ok || !info.Stream || info.Error != nil || info.StatusCode < 1 || info.StatusCode >= http.StatusBadRequest {
		return
	}
	o.timeToFirstChunk.Record(ctx, info.Duration.Seconds(), apiMetric.WithAttributes(state.attrs...))
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
	default:
		return "network_error"
	}
}
