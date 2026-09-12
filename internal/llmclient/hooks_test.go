package llmclient

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJoinHooksChainsCallbacks(t *testing.T) {
	var order []string
	type ctxKey string

	first := Hooks{
		OnRequestStart: func(ctx context.Context, _ RequestInfo) context.Context {
			order = append(order, "start-1")
			return context.WithValue(ctx, ctxKey("first"), true)
		},
		OnRequestEnd: func(_ context.Context, _ ResponseInfo) {
			order = append(order, "end-1")
		},
		OnStreamFirstChunk: func(_ context.Context, _ ResponseInfo) {
			order = append(order, "chunk-1")
		},
		OnEmptyResponse: func(_ context.Context, _ EmptyResponseInfo) {
			order = append(order, "empty-1")
		},
	}
	second := Hooks{
		OnRequestStart: func(ctx context.Context, _ RequestInfo) context.Context {
			order = append(order, "start-2")
			assert.Equal(t, true, ctx.Value(ctxKey("first")))
			return ctx
		},
		OnRequestEnd: func(_ context.Context, _ ResponseInfo) {
			order = append(order, "end-2")
		},
		OnStreamFirstChunk: func(_ context.Context, _ ResponseInfo) {
			order = append(order, "chunk-2")
		},
		OnEmptyResponse: func(_ context.Context, _ EmptyResponseInfo) {
			order = append(order, "empty-2")
		},
	}

	joined := JoinHooks(first, Hooks{}, second)
	ctx := joined.OnRequestStart(t.Context(), RequestInfo{})
	joined.OnRequestEnd(ctx, ResponseInfo{})
	joined.OnStreamFirstChunk(ctx, ResponseInfo{})
	joined.OnEmptyResponse(ctx, EmptyResponseInfo{})

	want := []string{"start-1", "start-2", "end-1", "end-2", "chunk-1", "chunk-2", "empty-1", "empty-2"}
	require.Equal(t, want, order)
}

func TestJoinHooksEmpty(t *testing.T) {
	joined := JoinHooks(Hooks{}, Hooks{})
	require.Nil(t, joined.OnRequestStart)
	require.Nil(t, joined.OnRequestEnd)
	require.Nil(t, joined.OnStreamFirstChunk)
	require.Nil(t, joined.OnEmptyResponse)
}

func TestJoinHooksSingleReusesCallback(t *testing.T) {
	called := 0
	only := Hooks{OnRequestEnd: func(context.Context, ResponseInfo) { called++ }}
	joined := JoinHooks(Hooks{}, only)
	require.Nil(t, joined.OnRequestStart)

	joined.OnRequestEnd(t.Context(), ResponseInfo{})
	require.Equal(t, 1, called)
}
