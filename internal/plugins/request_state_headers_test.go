package plugins

import (
	"net/http"
	"slices"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	want := []string{"X-Drop", "X-New", "X-Trace"}
	require.True(t, slices.Equal(changed, want))
	got := live.Get("X-Trace")
	assert.Equal(t, "edited", got)
	got = live.Get("X-New")
	assert.Equal(t, "added", got)
	_, ok := live["X-Drop"]
	assert.False(t, ok)
	got = live.Get("Authorization")
	assert.Equal(t, "Bearer secret", got)
	got = live.Get("X-Keep")
	assert.Equal(t, "same", got)
}

func TestCoerceTextareaAcceptsList(t *testing.T) {
	got, err := coerceTextarea([]any{"a => b", " c => d "})
	require.NoError(t, err)
	require.Equal(t, "a => b\nc => d", got)
	_, err = coerceTextarea([]any{1, map[string]any{}})
	require.Error(t, err)
}

func TestApplyResponseHeadersRemovesEmptyValues(t *testing.T) {
	state := NewRequestState()
	state.AddResponseHeader("X-Extra", "1")
	state.AddResponseHeader("x-request-id", "")
	dst := http.Header{"X-Request-Id": {"req-1"}, "Content-Type": {"application/json"}}
	state.ApplyResponseHeaders(dst)
	_, still := dst["X-Request-Id"]
	require.False(t, still, "X-Request-Id not removed: %v", dst)
	require.Equal(t, "1", dst.Get("X-Extra"))
	require.Equal(t, "application/json", dst.Get("Content-Type"), "headers = %v", dst)
}
