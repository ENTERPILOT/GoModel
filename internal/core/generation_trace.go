package core

import (
	"context"
	"sync"
)

const (
	// generationTracingKey marks a request whose inference calls open
	// generation traces. Telemetry sets it when OpenTelemetry export is on.
	generationTracingKey contextKey = "generation-tracing"
	// generationTraceKey stores the trace of the inference call in flight.
	generationTraceKey contextKey = "generation-trace"
)

// GenerationContentLimit is the most bytes of one captured text part
// (a message text, tool arguments, or a tool result) that telemetry exports.
// Streams accumulate no more than this while content is captured.
const GenerationContentLimit = 64 << 10

// GenerationOutcome is what the gateway learned from one inference call once
// it decoded the result: the request it served and the response it returned.
// Response is nil when the call produced no usable response.
type GenerationOutcome struct {
	Request  any // *ChatRequest or *ResponsesRequest
	Response any // *ChatResponse or *ResponsesResponse
}

// GenerationTrace links one gateway inference call to the telemetry span of
// the provider call that served it. That span ends inside the provider client,
// before the gateway decodes usage, so telemetry parks the successful call
// here and the gateway finishes it with the outcome. A nil trace is a no-op.
type GenerationTrace struct {
	captureContent bool

	mu       sync.Mutex
	pending  func(GenerationOutcome)
	finished bool
}

type generationTracing struct {
	captureContent bool
}

// WithGenerationTracing enables generation traces for inference calls made
// under ctx. Without it, StartGenerationTrace does nothing, so requests pay
// for tracing only while telemetry is on.
func WithGenerationTracing(ctx context.Context, captureContent bool) context.Context {
	return context.WithValue(ctx, generationTracingKey, generationTracing{captureContent: captureContent})
}

// StartGenerationTrace opens the trace of one inference call. Provider calls
// made under the returned context park in it. It returns ctx unchanged and a
// nil trace when generation tracing is off for the request.
func StartGenerationTrace(ctx context.Context) (context.Context, *GenerationTrace) {
	if ctx == nil {
		return ctx, nil
	}
	tracing, ok := ctx.Value(generationTracingKey).(generationTracing)
	if !ok {
		return ctx, nil
	}
	trace := &GenerationTrace{captureContent: tracing.captureContent}
	return context.WithValue(ctx, generationTraceKey, trace), trace
}

// GenerationTraceFromContext returns the trace of the inference call in
// flight, or nil.
func GenerationTraceFromContext(ctx context.Context) *GenerationTrace {
	if ctx == nil {
		return nil
	}
	trace, _ := ctx.Value(generationTraceKey).(*GenerationTrace)
	return trace
}

// CapturesContent reports whether the outcome's message content is recorded,
// so streams know to accumulate it.
func (t *GenerationTrace) CapturesContent() bool {
	return t != nil && t.captureContent
}

// Park hands the trace the completion of a successful provider call. Only
// the last provider call served the outcome, so a call parked earlier is
// completed without one. After Finish, finish runs at once without one.
func (t *GenerationTrace) Park(finish func(GenerationOutcome)) {
	if t == nil || finish == nil {
		return
	}
	t.mu.Lock()
	previous := t.pending
	t.pending = nil
	if t.finished {
		previous = finish
	} else {
		t.pending = finish
	}
	t.mu.Unlock()
	if previous != nil {
		previous(GenerationOutcome{})
	}
}

// Finish completes the parked provider call with outcome. Only the first call
// has an effect.
func (t *GenerationTrace) Finish(outcome GenerationOutcome) {
	if t == nil {
		return
	}
	t.mu.Lock()
	pending := t.pending
	t.pending = nil
	t.finished = true
	t.mu.Unlock()
	if pending != nil {
		pending(outcome)
	}
}
