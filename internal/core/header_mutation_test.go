package core

import (
	"context"
	"net/http"
	"testing"
)

func TestHeaderMutationApply(t *testing.T) {
	h := http.Header{
		"X-Debug":  []string{"1"},
		"X-Keep":   []string{"yes"},
		"X-Multi":  []string{"a", "b"},
		"X-Rewrit": []string{"old"},
	}
	mutation := &HeaderMutation{
		Set:    map[string]string{"X-Rewrit": "new", "X-Added": "v", "Authorization": "Bearer evil", "Content-Length": "0"},
		Remove: []string{"X-Debug", "Host"},
	}
	mutation.Apply(h)

	if h.Get("X-Rewrit") != "new" || h.Get("X-Added") != "v" {
		t.Fatalf("set not applied: %v", h)
	}
	if _, exists := h["X-Debug"]; exists {
		t.Fatal("remove not applied")
	}
	if h.Get("X-Keep") != "yes" || len(h["X-Multi"]) != 2 {
		t.Fatal("unrelated headers changed")
	}
	if h.Get("Authorization") != "" || h.Get("Content-Length") != "" {
		t.Fatal("protected headers must never be set by mutations")
	}
}

func TestHeaderMutationMerge(t *testing.T) {
	first := &HeaderMutation{Set: map[string]string{"X-A": "1", "X-B": "1"}, Remove: []string{"X-C"}}
	second := &HeaderMutation{Set: map[string]string{"X-B": "2", "X-C": "3"}, Remove: []string{"X-A"}}
	first.Merge(second)

	if _, ok := first.Set["X-A"]; ok {
		t.Fatal("later remove must clear earlier set")
	}
	if first.Set["X-B"] != "2" {
		t.Fatal("later set must win")
	}
	if first.Set["X-C"] != "3" {
		t.Fatal("later set must clear earlier remove")
	}
	for _, name := range first.Remove {
		if name == "X-C" {
			t.Fatal("X-C should no longer be removed")
		}
	}
}

func TestHeaderMutationContextRoundTrip(t *testing.T) {
	ctx := context.WithValue(t.Context(), headerMutationContextMarker{}, "kept")
	if HeaderMutationFromContext(ctx) != nil {
		t.Fatal("expected nil mutation on fresh context")
	}
	if got := WithHeaderMutation(ctx, nil); got != ctx {
		t.Fatal("empty mutation must not modify context")
	}
	mutation := &HeaderMutation{Set: map[string]string{"X-A": "1"}}
	got := HeaderMutationFromContext(WithHeaderMutation(ctx, mutation))
	if got == nil || got.Set["X-A"] != "1" {
		t.Fatalf("mutation not carried: %+v", got)
	}
	without := WithoutHeaderMutation(WithHeaderMutation(ctx, mutation))
	if HeaderMutationFromContext(without) != nil {
		t.Fatal("WithoutHeaderMutation must hide the request-scoped mutation")
	}
	if got := without.Value(headerMutationContextMarker{}); got != "kept" {
		t.Fatalf("WithoutHeaderMutation lost unrelated context value: %v", got)
	}
}

type headerMutationContextMarker struct{}

func TestIsProtectedHeader(t *testing.T) {
	for _, name := range []string{"Authorization", "cookie", "X-Api-Key", "Host", "content-length", "Content-Type", "Content-Encoding", "Accept-Encoding", "Transfer-Encoding", "Connection"} {
		if !IsProtectedHeader(name) {
			t.Fatalf("%s should be protected", name)
		}
	}
	for _, name := range []string{"Anthropic-Beta", "X-Debug", "User-Agent", "Accept"} {
		if IsProtectedHeader(name) {
			t.Fatalf("%s should not be protected", name)
		}
	}
}
