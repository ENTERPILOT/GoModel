package providers

import (
	"context"
	"net/http"
	"reflect"
	"sort"
	"testing"

	"gomodel/internal/core"
)

func TestApplyRequestHeaderOverrides_NoOverrides(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func() http.Header
		headers map[string]string
		pass    bool
	}{
		{
			name: "nil custom + passthrough false",
			setup: func() http.Header {
				return http.Header{"Authorization": {"Bearer original"}}
			},
			headers: nil,
			pass:    false,
		},
		{
			name: "empty custom + passthrough false",
			setup: func() http.Header {
				return http.Header{"Authorization": {"Bearer original"}}
			},
			headers: map[string]string{},
			pass:    false,
		},
		{
			name: "passthrough true but no snapshot on context",
			setup: func() http.Header {
				return http.Header{"Authorization": {"Bearer original"}}
			},
			headers: nil,
			pass:    true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := tt.setup()
			before := cloneHeader(h)

			ApplyRequestHeaderOverrides(context.Background(), h, tt.headers, tt.pass)

			if !reflect.DeepEqual(h, before) {
				t.Fatalf("expected no changes, got %v want %v", h, before)
			}
		})
	}
}

func TestApplyRequestHeaderOverrides_NilHeader(t *testing.T) {
	t.Parallel()

	// Must not panic with a nil header; nothing to apply onto.
	ApplyRequestHeaderOverrides(context.Background(), nil, map[string]string{"X-Test": "v"}, false)
	ApplyRequestHeaderOverrides(context.Background(), nil, nil, true)
}

func TestApplyRequestHeaderOverrides_CustomUpstreamHeadersCanonicalizeAndOverwrite(t *testing.T) {
	t.Parallel()

	h := http.Header{
		"Authorization": {"Bearer original"},
		"x-custom":      {"original-lower"},
		"Existing":      {"original-existing"},
	}

	custom := map[string]string{
		"authorization": "Bearer custom", // lower-case key, must canonicalize
		"X-Custom":      "new-upper",     // mixed case
		"New-Header":    "fresh",         // missing before
		"  Trim-Me  ":   "trimmed",       // whitespace key
	}

	ApplyRequestHeaderOverrides(context.Background(), h, custom, false)

	expectEqual(t, h.Get("Authorization"), "Bearer custom")
	expectEqual(t, h.Get("X-Custom"), "new-upper")
	expectEqual(t, h.Get("New-Header"), "fresh")
	expectEqual(t, h.Get("Existing"), "original-existing") // untouched
	expectEqual(t, h.Get("Trim-Me"), "trimmed")
}

func TestApplyRequestHeaderOverrides_CustomHeaderSkipsEmptyKey(t *testing.T) {
	t.Parallel()

	h := http.Header{"X-Existing": {"v"}}

	custom := map[string]string{
		"":       "ignored",
		"   ":    "ignored",
		"X-Real": "value",
	}

	ApplyRequestHeaderOverrides(context.Background(), h, custom, false)

	if _, ok := h["X-Ignored"]; ok {
		t.Fatalf("unexpected empty key written: %v", h)
	}
	expectEqual(t, h.Get("X-Real"), "value")
}

func TestApplyRequestHeaderOverrides_PassthroughAppliesSnapshotHeaders(t *testing.T) {
	t.Parallel()

	h := http.Header{"X-Other": {"original"}}

	snapshot := core.NewRequestSnapshot(
		"POST",
		"/v1/chat",
		nil,
		nil,
		map[string][]string{
			"X-Snapshot-Only": {"snap-1"},
			"x-trace-id":      {"trace-abc"},
			"X-Multi":         {"one", "two"},
		},
		"application/json",
		nil,
		false,
		"req-1",
		nil,
	)
	ctx := core.WithRequestSnapshot(context.Background(), snapshot)

	ApplyRequestHeaderOverrides(ctx, h, nil, true)

	expectEqual(t, h.Get("X-Snapshot-Only"), "snap-1")
	expectEqual(t, h.Get("X-Trace-Id"), "trace-abc")
	expectSlice(t, h.Values("X-Multi"), []string{"one", "two"})
	expectEqual(t, h.Get("X-Other"), "original")
}

func TestApplyRequestHeaderOverrides_PassthroughSkipsReservedHeaders(t *testing.T) {
	t.Parallel()

	h := http.Header{}

	snapshot := core.NewRequestSnapshot(
		"POST", "/v1/chat", nil, nil,
		map[string][]string{
			"Authorization":    {"Bearer snap"},
			"Cookie":           {"sid=abc"},
			"X-Forwarded-For":  {"1.2.3.4"},
			"X-Forwarded-Host": {"evil.example"},
			"Host":             {"upstream.example"},
			"X-Safe":           {"ok"},
		},
		"application/json", nil, false, "req-2", nil,
	)
	ctx := core.WithRequestSnapshot(context.Background(), snapshot)

	ApplyRequestHeaderOverrides(ctx, h, nil, true)

	if v := h.Get("Authorization"); v != "" {
		t.Fatalf("Authorization must be skipped, got %q", v)
	}
	if v := h.Get("Cookie"); v != "" {
		t.Fatalf("Cookie must be skipped, got %q", v)
	}
	if v := h.Get("X-Forwarded-For"); v != "" {
		t.Fatalf("X-Forwarded-* must be skipped, got %q", v)
	}
	if v := h.Get("X-Forwarded-Host"); v != "" {
		t.Fatalf("X-Forwarded-* must be skipped, got %q", v)
	}
	if v := h.Get("Host"); v != "" {
		t.Fatalf("Host must be skipped, got %q", v)
	}
	expectEqual(t, h.Get("X-Safe"), "ok")
}

func TestApplyRequestHeaderOverrides_PassthroughOverwritesExisting(t *testing.T) {
	t.Parallel()

	h := http.Header{
		"X-Trace-Id":   {"old"},
		"X-Only-Local": {"untouched"},
	}

	snapshot := core.NewRequestSnapshot(
		"POST", "/v1/chat", nil, nil,
		map[string][]string{"X-Trace-Id": {"fresh"}},
		"", nil, false, "req-3", nil,
	)
	ctx := core.WithRequestSnapshot(context.Background(), snapshot)

	ApplyRequestHeaderOverrides(ctx, h, nil, true)

	expectEqual(t, h.Get("X-Trace-Id"), "fresh")
	expectEqual(t, h.Get("X-Only-Local"), "untouched")
}

func TestApplyRequestHeaderOverrides_CustomIgnoresSnapshot(t *testing.T) {
	t.Parallel()

	h := http.Header{"X-Existing": {"preserve"}}

	snapshot := core.NewRequestSnapshot(
		"POST", "/v1/chat", nil, nil,
		map[string][]string{"X-Tenant-Id": {"acme"}, "X-Trace-Id": {"trace"}},
		"", nil, false, "req-4", nil,
	)
	ctx := core.WithRequestSnapshot(context.Background(), snapshot)

	custom := map[string]string{"X-My-Header": "value"}
	ApplyRequestHeaderOverrides(ctx, h, custom, false)

	expectEqual(t, h.Get("X-My-Header"), "value")
	if v := h.Get("X-Tenant-Id"); v != "" {
		t.Fatalf("custom path must not copy X-Tenant-Id from snapshot, got %q", v)
	}
	if v := h.Get("X-Trace-Id"); v != "" {
		t.Fatalf("custom path must not copy X-Trace-Id from snapshot, got %q", v)
	}
	expectEqual(t, h.Get("X-Existing"), "preserve")
}

func TestApplyRequestHeaderOverrides_BothPassthroughAndCustomPanics(t *testing.T) {
	t.Parallel()

	defer func() {
		if recover() == nil {
			t.Fatalf("expected panic when both passthrough and custom headers are set")
		}
	}()

	ApplyRequestHeaderOverrides(
		context.Background(),
		http.Header{},
		map[string]string{"X-Custom": "value"},
		true,
	)
}

func TestApplyRequestHeaderOverrides_PassthroughDeletesExistingValues(t *testing.T) {
	t.Parallel()

	h := http.Header{"X-Trace-Id": {"old-1", "old-2"}}

	snapshot := core.NewRequestSnapshot(
		"POST", "/v1/chat", nil, nil,
		map[string][]string{"X-Trace-Id": {"fresh"}},
		"", nil, false, "req-4", nil,
	)
	ctx := core.WithRequestSnapshot(context.Background(), snapshot)

	ApplyRequestHeaderOverrides(ctx, h, nil, true)

	expectSlice(t, h.Values("X-Trace-Id"), []string{"fresh"})
}

func TestApplyRequestHeaderOverrides_PassthroughEmptySnapshotMap(t *testing.T) {
	t.Parallel()

	h := http.Header{"X-Custom": {"existing"}}

	snapshot := core.NewRequestSnapshot(
		"POST", "/v1/chat", nil, nil, nil, "", nil, false, "req-6", nil,
	)
	ctx := core.WithRequestSnapshot(context.Background(), snapshot)

	ApplyRequestHeaderOverrides(ctx, h, nil, true)

	expectEqual(t, h.Get("X-Custom"), "existing")
}

func TestApplyRequestHeaderOverrides_PassthroughSnapshotWithEmptyValueSlice(t *testing.T) {
	t.Parallel()

	h := http.Header{"X-Empty": {"keep-or-not"}}

	snapshot := core.NewRequestSnapshot(
		"POST", "/v1/chat", nil, nil,
		map[string][]string{"X-Empty": nil},
		"", nil, false, "req-7", nil,
	)
	ctx := core.WithRequestSnapshot(context.Background(), snapshot)

	ApplyRequestHeaderOverrides(ctx, h, nil, true)

	if _, ok := h["X-Empty"]; ok {
		t.Fatalf("X-Empty must be deleted when snapshot slice empty, got %v", h.Values("X-Empty"))
	}
}

func TestApplyRequestHeaderOverrides_PassthroughUserExtrasSkipped(t *testing.T) {
	t.Parallel()

	h := http.Header{}

	snapshot := core.NewRequestSnapshot(
		"POST", "/v1/chat", nil, nil,
		map[string][]string{
			"X-Custom-Skip": {"upstream-rejects"},
			"X-Safe":        {"ok"},
		},
		"", nil, false, "req-8", nil,
	)
	ctx := core.WithRequestSnapshot(context.Background(), snapshot)

	ApplyRequestHeaderOverrides(ctx, h, nil, true, "X-Custom-Skip")

	if v := h.Get("X-Custom-Skip"); v != "" {
		t.Fatalf("X-Custom-Skip must be skipped, got %q", v)
	}
	expectEqual(t, h.Get("X-Safe"), "ok")
}

// --- helpers ---------------------------------------------------------------

func cloneHeader(h http.Header) http.Header {
	cloned := make(http.Header, len(h))
	for k, v := range h {
		vs := make([]string, len(v))
		copy(vs, v)
		cloned[k] = vs
	}
	return cloned
}

func expectEqual(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func expectSlice(t *testing.T, got, want []string) {
	t.Helper()
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
