package core

import (
	"context"
	"net/http"
	"slices"
	"testing"
)

func TestHeaderPlanApply(t *testing.T) {
	h := http.Header{
		"X-Debug":  []string{"1"},
		"X-Keep":   []string{"yes"},
		"X-Multi":  []string{"a", "b"},
		"X-Rewrit": []string{"old"},
	}
	mutation := &HeaderPlan{
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

func TestHeaderPlanMerge(t *testing.T) {
	first := &HeaderPlan{
		Set:    map[string]string{"X-A": "1", "X-B": "1", "X-Secret": "copied"},
		Remove: []string{"X-C"}, SensitiveSet: []string{"X-Secret"},
	}
	second := &HeaderPlan{
		Set:    map[string]string{"X-B": "2", "X-C": "3", "X-Secret": "literal"},
		Remove: []string{"X-A"},
	}
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
	if slices.Contains(first.SensitiveSet, "X-Secret") {
		t.Fatal("later non-sensitive set must clear earlier source sensitivity")
	}
	for _, name := range first.Remove {
		if name == "X-C" {
			t.Fatal("X-C should no longer be removed")
		}
	}
}

func TestHeaderPlanMergeRemovesSensitivityWithHeader(t *testing.T) {
	plan := &HeaderPlan{Set: map[string]string{"X-Team": "secret"}, SensitiveSet: []string{"X-Team"}}
	plan.Merge(&HeaderPlan{Remove: []string{"X-Team"}})

	if len(plan.SensitiveSet) != 0 {
		t.Fatalf("removed header retained audit sensitivity metadata: %v", plan.SensitiveSet)
	}
}

func TestHeaderPlanContextRoundTrip(t *testing.T) {
	ctx := context.WithValue(t.Context(), headerMutationContextMarker{}, "kept")
	if HeaderPlanFromContext(ctx) != nil {
		t.Fatal("expected nil mutation on fresh context")
	}
	if got := WithHeaderPlan(ctx, nil); got != ctx {
		t.Fatal("empty mutation must not modify context")
	}
	mutation := &HeaderPlan{Set: map[string]string{"X-A": "1"}}
	got := HeaderPlanFromContext(WithHeaderPlan(ctx, mutation))
	if got == nil || got.Set["X-A"] != "1" {
		t.Fatalf("mutation not carried: %+v", got)
	}
	without := WithoutHeaderPlan(WithHeaderPlan(ctx, mutation))
	if HeaderPlanFromContext(without) != nil {
		t.Fatal("WithoutHeaderPlan must hide the request-scoped mutation")
	}
	if got := without.Value(headerMutationContextMarker{}); got != "kept" {
		t.Fatalf("WithoutHeaderPlan lost unrelated context value: %v", got)
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

func TestHeaderPlanCacheFingerprintIsStableAndValueSensitive(t *testing.T) {
	first := &HeaderPlan{Set: map[string]string{"X-B": "2", "X-A": "1"}, Remove: []string{"X-Z"}}
	reordered := &HeaderPlan{Set: map[string]string{"X-A": "1", "X-B": "2"}, Remove: []string{"X-Z"}}
	changed := &HeaderPlan{Set: map[string]string{"X-A": "different", "X-B": "2"}, Remove: []string{"X-Z"}}

	if first.CacheFingerprint() != reordered.CacheFingerprint() {
		t.Fatal("equivalent plans produced different cache fingerprints")
	}
	if first.CacheFingerprint() == changed.CacheFingerprint() {
		t.Fatal("different resolved values produced the same cache fingerprint")
	}
}

func TestShouldRedactHeaderRecognizesCustomCredentialNames(t *testing.T) {
	for _, name := range []string{"X-Session-Token", "X-Api-Token", "X-Custom-Auth", "X-Upstream-Secret"} {
		if !ShouldRedactHeader(name) {
			t.Fatalf("%s should be redacted from audit logs", name)
		}
		if IsProtectedHeader(name) {
			t.Fatalf("%s should remain authorable as an outbound policy target", name)
		}
	}
	if ShouldRedactHeader("Anthropic-Beta") {
		t.Fatal("Anthropic-Beta should remain visible when header logging is enabled")
	}
}
