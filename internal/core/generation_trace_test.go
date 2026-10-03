package core

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartGenerationTraceRequiresTracing(t *testing.T) {
	ctx, trace := StartGenerationTrace(t.Context())
	assert.Nil(t, trace)
	assert.Nil(t, GenerationTraceFromContext(ctx))

	ctx, trace = StartGenerationTrace(WithGenerationTracing(t.Context(), true))
	require.NotNil(t, trace)
	assert.Same(t, trace, GenerationTraceFromContext(ctx))
	assert.True(t, trace.CapturesContent())
}

func TestGenerationTraceFinishCompletesParkedCall(t *testing.T) {
	_, trace := StartGenerationTrace(WithGenerationTracing(t.Context(), false))
	var got []GenerationOutcome
	trace.Park(func(outcome GenerationOutcome) { got = append(got, outcome) })
	require.Empty(t, got, "a parked call waits for the outcome")

	resp := &ChatResponse{ID: "chatcmpl-1"}
	trace.Finish(GenerationOutcome{Response: resp})
	trace.Finish(GenerationOutcome{})

	require.Len(t, got, 1)
	assert.Same(t, resp, got[0].Response)
}

func TestGenerationTraceParkCompletesSupersededCall(t *testing.T) {
	_, trace := StartGenerationTrace(WithGenerationTracing(t.Context(), false))
	var first, second []GenerationOutcome
	trace.Park(func(outcome GenerationOutcome) { first = append(first, outcome) })
	trace.Park(func(outcome GenerationOutcome) { second = append(second, outcome) })

	require.Len(t, first, 1, "an earlier call ends when a later one parks")
	assert.Nil(t, first[0].Response)

	trace.Finish(GenerationOutcome{Response: &ChatResponse{}})
	require.Len(t, second, 1)
	assert.NotNil(t, second[0].Response)
}

func TestGenerationTraceParkAfterFinishCompletesAtOnce(t *testing.T) {
	_, trace := StartGenerationTrace(WithGenerationTracing(t.Context(), false))
	trace.Finish(GenerationOutcome{Response: &ChatResponse{}})

	var got []GenerationOutcome
	trace.Park(func(outcome GenerationOutcome) { got = append(got, outcome) })
	require.Len(t, got, 1)
	assert.Nil(t, got[0].Response)
}

func TestNilGenerationTraceIsNoOp(t *testing.T) {
	var trace *GenerationTrace
	assert.False(t, trace.CapturesContent())
	trace.Park(func(GenerationOutcome) { assert.Fail(t, "nil trace must not run finishers") })
	trace.Finish(GenerationOutcome{})

	//nolint:staticcheck // A nil context is what this guard exists for.
	ctx, started := StartGenerationTrace(context.Context(nil))
	assert.Nil(t, ctx)
	assert.Nil(t, started)
}
