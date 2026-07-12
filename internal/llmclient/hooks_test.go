package llmclient

import (
	"context"
	"testing"
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
	}
	second := Hooks{
		OnRequestStart: func(ctx context.Context, _ RequestInfo) context.Context {
			order = append(order, "start-2")
			if ctx.Value(ctxKey("first")) != true {
				t.Errorf("second OnRequestStart did not receive first hook's context")
			}
			return ctx
		},
		OnRequestEnd: func(_ context.Context, _ ResponseInfo) {
			order = append(order, "end-2")
		},
	}

	joined := JoinHooks(first, Hooks{}, second)
	ctx := joined.OnRequestStart(t.Context(), RequestInfo{})
	joined.OnRequestEnd(ctx, ResponseInfo{})

	want := []string{"start-1", "start-2", "end-1", "end-2"}
	if len(order) != len(want) {
		t.Fatalf("callback order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("callback order = %v, want %v", order, want)
		}
	}
}

func TestJoinHooksEmpty(t *testing.T) {
	joined := JoinHooks(Hooks{}, Hooks{})
	if joined.OnRequestStart != nil || joined.OnRequestEnd != nil {
		t.Fatalf("JoinHooks of empty hooks should have nil callbacks")
	}
}

func TestJoinHooksSingleReusesCallback(t *testing.T) {
	called := 0
	only := Hooks{OnRequestEnd: func(context.Context, ResponseInfo) { called++ }}
	joined := JoinHooks(Hooks{}, only)
	if joined.OnRequestStart != nil {
		t.Fatalf("expected nil OnRequestStart")
	}
	joined.OnRequestEnd(t.Context(), ResponseInfo{})
	if called != 1 {
		t.Fatalf("OnRequestEnd called %d times, want 1", called)
	}
}
