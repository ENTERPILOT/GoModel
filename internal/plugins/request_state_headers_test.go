package plugins

import (
	"net/http"
	"slices"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestApplyRequestHeadersReplaysOnlyEdits(t *testing.T) {
	inbound := http.Header{
		"Authorization": {"Bearer secret"},
		"X-Trace":       {"abc"},
		"X-Drop":        {"1"},
		"X-Keep":        {"same"},
	}
	snapshot := core.NewRequestSnapshot(http.MethodPost, "/v1/chat/completions", nil, nil, inbound, "application/json", nil, false, "req-1", nil)
	ctx := core.WithRequestSnapshot(t.Context(), snapshot)
	ctx, state := WithRequestState(ctx)

	x := state.NewExchange(ctx, MetaFromContext(ctx, nil))
	x.Headers.Request.Set("X-Trace", "edited")
	x.Headers.Request.Set("X-New", "added")
	x.Headers.Request.Del("X-Drop")
	x.Headers.Request.Set("Authorization", "Bearer injected")

	live := inbound.Clone()
	changed := state.ApplyRequestHeaders(live)
	if want := []string{"X-Drop", "X-New", "X-Trace"}; !slices.Equal(changed, want) {
		t.Fatalf("changed = %v, want %v", changed, want)
	}
	if got := live.Get("X-Trace"); got != "edited" {
		t.Errorf("X-Trace = %q", got)
	}
	if got := live.Get("X-New"); got != "added" {
		t.Errorf("X-New = %q", got)
	}
	if _, ok := live["X-Drop"]; ok {
		t.Errorf("X-Drop still present")
	}
	if got := live.Get("Authorization"); got != "Bearer secret" {
		t.Errorf("credential header changed: %q", got)
	}
	if got := live.Get("X-Keep"); got != "same" {
		t.Errorf("X-Keep = %q", got)
	}
}

func TestCoerceTextareaAcceptsList(t *testing.T) {
	got, err := coerceTextarea([]any{"a => b", " c => d "})
	if err != nil {
		t.Fatal(err)
	}
	if got != "a => b\nc => d" {
		t.Fatalf("got %q", got)
	}
	if _, err := coerceTextarea([]any{1, map[string]any{}}); err == nil {
		t.Fatal("expected an error for a non-string item")
	}
}

func TestApplyResponseHeadersRemovesEmptyValues(t *testing.T) {
	state := NewRequestState()
	state.AddResponseHeader("X-Extra", "1")
	state.AddResponseHeader("x-request-id", "")
	dst := http.Header{"X-Request-Id": {"req-1"}, "Content-Type": {"application/json"}}
	state.ApplyResponseHeaders(dst)
	if _, still := dst["X-Request-Id"]; still {
		t.Fatalf("X-Request-Id not removed: %v", dst)
	}
	if dst.Get("X-Extra") != "1" || dst.Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %v", dst)
	}
}
