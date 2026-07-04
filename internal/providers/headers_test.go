package providers

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// captureLogger swaps slog.Default for a buffer-backed logger during the test
// and restores it when the test finishes. Returns the buffer so the test can
// inspect emitted log records.
func captureLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		slog.SetDefault(prev)
	})
	return buf
}

func TestIsHeaderBlocked(t *testing.T) {
	tests := []struct {
		name          string
		header        string
		userPathAlias string
		want          bool
	}{
		{name: "authorization credential", header: "Authorization", want: true},
		{name: "lowercase authorization", header: "authorization", want: true},
		{name: "x-api-key credential", header: "X-Api-Key", want: true},
		{name: "cookie credential", header: "Cookie", want: true},
		{name: "set-cookie credential", header: "Set-Cookie", want: true},
		{name: "x-gomodel-key credential", header: "X-GoModel-Key", want: true},
		{name: "x-gomodel-user-path internal", header: "X-GoModel-User-Path", want: true},
		{name: "user path alias blocks header", header: "X-My-User-Path", userPathAlias: "X-My-User-Path", want: true},
		{name: "user path alias case-insensitive", header: "x-my-user-path", userPathAlias: "X-My-User-Path", want: true},
		{name: "user path alias whitespace trimmed", header: "  X-My-User-Path  ", userPathAlias: "X-My-User-Path", want: true},
		{name: "alias configured but header different", header: "X-Some-Other", userPathAlias: "X-My-User-Path", want: false},
		{name: "empty alias does not block extras", header: "X-Custom", userPathAlias: "", want: false},
		{name: "non-credential header allowed", header: "X-Custom-Header", want: false},
		{name: "content-type allowed", header: "Content-Type", want: false},
		{name: "accept allowed", header: "Accept", want: false},
		{name: "user-agent allowed", header: "User-Agent", want: false},
		{name: "empty header name", header: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsHeaderBlocked(tt.header, tt.userPathAlias)
			if got != tt.want {
				t.Errorf("IsHeaderBlocked(%q, %q) = %v, want %v", tt.header, tt.userPathAlias, got, tt.want)
			}
		})
	}
}

func TestFilterIncomingHeaders_CopySemantics(t *testing.T) {
	userPathHeader := http.CanonicalHeaderKey("X-GoModel-User-Path")
	src := http.Header{
		"Authorization": {"Bearer secret"},
		"X-Api-Key":     {"k"},
		userPathHeader:  {"/v1/x"},
		"Content-Type":  {"application/json"},
		"X-Custom":      {"value"},
	}

	filtered := FilterIncomingHeaders(src, "")

	// Original must not be mutated.
	if got := src.Get("Authorization"); got != "Bearer secret" {
		t.Errorf("original Authorization mutated: %q", got)
	}
	if _, ok := src["X-Api-Key"]; !ok {
		t.Error("original X-Api-Key removed unexpectedly")
	}

	// Filtered must drop credentials and internal headers.
	if got := filtered.Get("Authorization"); got != "" {
		t.Errorf("filtered Authorization should be empty, got %q", got)
	}
	if _, ok := filtered["X-Api-Key"]; ok {
		t.Error("filtered X-Api-Key should be dropped")
	}
	if _, ok := filtered[userPathHeader]; ok {
		t.Error("filtered X-GoModel-User-Path should be dropped")
	}

	// Filtered must keep non-credential headers.
	if got := filtered.Get("Content-Type"); got != "application/json" {
		t.Errorf("filtered Content-Type = %q, want application/json", got)
	}
	if got := filtered.Get("X-Custom"); got != "value" {
		t.Errorf("filtered X-Custom = %q, want value", got)
	}

	// Filtered must be a new map (not the same underlying reference).
	if len(filtered) == 0 {
		t.Error("expected non-empty filtered result")
	}
	filtered.Set("Content-Type", "mutated")
	if src.Get("Content-Type") == "mutated" {
		t.Error("mutation of filtered leaked back to source")
	}
}

func TestFilterIncomingHeaders_UserPathAlias(t *testing.T) {
	src := http.Header{
		"X-My-Alias": {"v"},
		"X-Custom":   {"keep"},
	}
	filtered := FilterIncomingHeaders(src, "X-My-Alias")
	if _, ok := filtered["X-My-Alias"]; ok {
		t.Error("alias header should be filtered out")
	}
	if filtered.Get("X-Custom") != "keep" {
		t.Error("non-alias header should be kept")
	}
}

func TestApplyHeaderOverrides_StaticMode(t *testing.T) {
	tests := []struct {
		name          string
		cfg           HeaderOverridesConfig
		userPathAlias string
		wantHeaders   map[string]string
		wantMissing   []string
	}{
		{
			name: "applies static custom headers",
			cfg: HeaderOverridesConfig{
				CustomUpstreamHeaders: map[string]string{
					"X-Provider-Region": "us-east-1",
					"X-Trace-Id":        "abc-123",
				},
			},
			wantHeaders: map[string]string{
				"X-Provider-Region": "us-east-1",
				"X-Trace-Id":        "abc-123",
			},
		},
		{
			name: "skips blocked credential header in static mode",
			cfg: HeaderOverridesConfig{
				CustomUpstreamHeaders: map[string]string{
					"Authorization":       "Bearer leaked",
					"X-Api-Key":           "leaked",
					"X-GoModel-User-Path": "/internal",
					"X-Safe":              "ok",
				},
			},
			wantHeaders: map[string]string{
				"X-Safe": "ok",
			},
			wantMissing: []string{"Authorization", "X-Api-Key", "X-GoModel-User-Path"},
		},
		{
			name: "user path alias blocks configured header",
			cfg: HeaderOverridesConfig{
				CustomUpstreamHeaders: map[string]string{
					"X-My-Alias": "secret",
					"X-Other":    "ok",
				},
			},
			userPathAlias: "X-My-Alias",
			wantHeaders: map[string]string{
				"X-Other": "ok",
			},
			wantMissing: []string{"X-My-Alias"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, "http://example.com/v1/chat", nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			ApplyHeaderOverrides(req, tt.cfg, tt.userPathAlias)
			for k, want := range tt.wantHeaders {
				if got := req.Header.Get(k); got != want {
					t.Errorf("header %q = %q, want %q", k, got, want)
				}
			}
			for _, k := range tt.wantMissing {
				if got := req.Header.Get(k); got != "" {
					t.Errorf("blocked header %q unexpectedly present: %q", k, got)
				}
			}
		})
	}
}

func TestApplyHeaderOverrides_NilAndZeroValue_NoOp(t *testing.T) {
	t.Run("zero value config", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, "http://example.com", nil)
		req.Header.Set("X-Existing", "v")
		ApplyHeaderOverrides(req, HeaderOverridesConfig{}, "")
		if got := req.Header.Get("X-Existing"); got != "v" {
			t.Errorf("zero-value config should be no-op, got %q", got)
		}
		if len(req.Header) != 1 {
			t.Errorf("expected exactly 1 header, got %d (%v)", len(req.Header), req.Header)
		}
	})

	t.Run("passthrough disabled no custom headers", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, "http://example.com", nil)
		ApplyHeaderOverrides(req, HeaderOverridesConfig{PassthroughUserHeaders: false, CustomUpstreamHeaders: nil}, "")
		if len(req.Header) != 0 {
			t.Errorf("expected empty headers, got %v", req.Header)
		}
	})

	t.Run("passthrough enabled but no context headers", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, "http://example.com", nil)
		ApplyHeaderOverrides(req, HeaderOverridesConfig{PassthroughUserHeaders: true}, "")
		if len(req.Header) != 0 {
			t.Errorf("expected empty headers when no context, got %v", req.Header)
		}
	})
}

func TestApplyHeaderOverrides_PassthroughMode(t *testing.T) {
	tests := []struct {
		name          string
		ctxHeaders    http.Header
		cfg           HeaderOverridesConfig
		userPathAlias string
		wantHeaders   map[string]string
		wantMissing   []string
	}{
		{
			name: "skip mode (default) drops listed headers",
			ctxHeaders: http.Header{
				"X-Tenant":     {"acme"},
				"X-Skip-Me":    {"nope"},
				"Content-Type": {"application/json"},
			},
			cfg: HeaderOverridesConfig{
				PassthroughUserHeaders: true,
				SkipHeaders:            []string{"X-Skip-Me"},
				SkipMode:               "skip",
			},
			wantHeaders: map[string]string{
				"X-Tenant":     "acme",
				"Content-Type": "application/json",
			},
			wantMissing: []string{"X-Skip-Me"},
		},
		{
			name: "skip mode empty mode string treated as skip",
			ctxHeaders: http.Header{
				"X-Tenant":  {"acme"},
				"X-Skip-Me": {"nope"},
			},
			cfg: HeaderOverridesConfig{
				PassthroughUserHeaders: true,
				SkipHeaders:            []string{"X-Skip-Me"},
				SkipMode:               "",
			},
			wantHeaders: map[string]string{
				"X-Tenant": "acme",
			},
			wantMissing: []string{"X-Skip-Me"},
		},
		{
			name: "allow mode forwards only listed headers",
			ctxHeaders: http.Header{
				"X-Tenant":     {"acme"},
				"X-Allowed":    {"yes"},
				"X-Not-Listed": {"no"},
			},
			cfg: HeaderOverridesConfig{
				PassthroughUserHeaders: true,
				SkipHeaders:            []string{"X-Tenant", "X-Allowed"},
				SkipMode:               "allow",
			},
			wantHeaders: map[string]string{
				"X-Tenant":  "acme",
				"X-Allowed": "yes",
			},
			wantMissing: []string{"X-Not-Listed"},
		},
		{
			name: "only mode alias forwards only listed headers",
			ctxHeaders: http.Header{
				"X-Tenant":     {"acme"},
				"X-Allowed":    {"yes"},
				"X-Not-Listed": {"no"},
			},
			cfg: HeaderOverridesConfig{
				PassthroughUserHeaders: true,
				SkipHeaders:            []string{"X-Tenant", "X-Allowed"},
				SkipMode:               "only",
			},
			wantHeaders: map[string]string{
				"X-Tenant":  "acme",
				"X-Allowed": "yes",
			},
			wantMissing: []string{"X-Not-Listed"},
		},
		{
			name: "only mode credential floor blocks even when allowlist includes them",
			ctxHeaders: http.Header{
				"Authorization": {"Bearer user"},
				"X-Api-Key":     {"k"},
				"X-Tenant":      {"acme"},
			},
			cfg: HeaderOverridesConfig{
				PassthroughUserHeaders: true,
				SkipHeaders:            []string{"Authorization", "X-Api-Key", "X-Tenant"},
				SkipMode:               "only",
			},
			wantHeaders: map[string]string{
				"X-Tenant": "acme",
			},
			wantMissing: []string{"Authorization", "X-Api-Key"},
		},
		{
			name: "credential floor blocks even when allowed list includes them",
			ctxHeaders: http.Header{
				"Authorization": {"Bearer user"},
				"X-Api-Key":     {"k"},
				"X-Tenant":      {"acme"},
			},
			cfg: HeaderOverridesConfig{
				PassthroughUserHeaders: true,
				SkipHeaders:            []string{"Authorization", "X-Api-Key", "X-Tenant"},
				SkipMode:               "allow",
			},
			wantHeaders: map[string]string{
				"X-Tenant": "acme",
			},
			wantMissing: []string{"Authorization", "X-Api-Key"},
		},
		{
			name: "internal header blocked in passthrough",
			ctxHeaders: http.Header{
				"X-GoModel-User-Path": {"/v1/internal"},
				"X-Tenant":            {"acme"},
			},
			cfg: HeaderOverridesConfig{
				PassthroughUserHeaders: true,
				SkipMode:               "skip",
			},
			wantHeaders: map[string]string{
				"X-Tenant": "acme",
			},
			wantMissing: []string{"X-GoModel-User-Path"},
		},
		{
			name: "user path alias blocks in passthrough",
			ctxHeaders: http.Header{
				"X-My-Alias": {"value"},
				"X-Tenant":   {"acme"},
			},
			cfg: HeaderOverridesConfig{
				PassthroughUserHeaders: true,
				SkipMode:               "skip",
			},
			userPathAlias: "X-My-Alias",
			wantHeaders: map[string]string{
				"X-Tenant": "acme",
			},
			wantMissing: []string{"X-My-Alias"},
		},
		{
			name: "skip mode case-insensitive",
			ctxHeaders: http.Header{
				"X-Tenant":  {"acme"},
				"X-Skip-Me": {"nope"},
			},
			cfg: HeaderOverridesConfig{
				PassthroughUserHeaders: true,
				SkipHeaders:            []string{"x-skip-me"},
				SkipMode:               "skip",
			},
			wantHeaders: map[string]string{
				"X-Tenant": "acme",
			},
			wantMissing: []string{"X-Skip-Me"},
		},
		{
			name: "skip mode with whitespace in skip list",
			ctxHeaders: http.Header{
				"X-Tenant":  {"acme"},
				"X-Skip-Me": {"nope"},
			},
			cfg: HeaderOverridesConfig{
				PassthroughUserHeaders: true,
				SkipHeaders:            []string{"  X-Skip-Me  "},
				SkipMode:               "skip",
			},
			wantHeaders: map[string]string{
				"X-Tenant": "acme",
			},
			wantMissing: []string{"X-Skip-Me"},
		},
		{
			name: "unknown mode defaults to skip",
			ctxHeaders: http.Header{
				"X-Tenant":  {"acme"},
				"X-Skip-Me": {"nope"},
			},
			cfg: HeaderOverridesConfig{
				PassthroughUserHeaders: true,
				SkipHeaders:            []string{"X-Skip-Me"},
				SkipMode:               "bogus",
			},
			wantHeaders: map[string]string{
				"X-Tenant": "acme",
			},
			wantMissing: []string{"X-Skip-Me"},
		},
		{
			name: "multi-value header preserved (first retrieved by Get)",
			ctxHeaders: http.Header{
				"X-Tenant": {"a", "b", "c"},
			},
			cfg: HeaderOverridesConfig{
				PassthroughUserHeaders: true,
				SkipMode:               "skip",
			},
			wantHeaders: map[string]string{
				"X-Tenant": "a",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, "http://example.com", nil)
			ctx := WithPassthroughHeaders(req.Context(), tt.ctxHeaders)
			req = req.WithContext(ctx)

			ApplyHeaderOverrides(req, tt.cfg, tt.userPathAlias)

			for k, want := range tt.wantHeaders {
				if got := req.Header.Get(k); got != want {
					t.Errorf("header %q = %q, want %q", k, got, want)
				}
			}
			for _, k := range tt.wantMissing {
				if got := req.Header.Get(k); got != "" {
					t.Errorf("blocked header %q unexpectedly present: %q", k, got)
				}
			}
		})
	}
}

func TestApplyHeaderOverrides_PassthroughWithCustomHeadersEmitsDebug(t *testing.T) {
	buf := captureLogger(t)

	req, _ := http.NewRequest(http.MethodPost, "http://example.com", nil)
	ctx := WithPassthroughHeaders(req.Context(), http.Header{"X-Tenant": {"acme"}})
	req = req.WithContext(ctx)

	cfg := HeaderOverridesConfig{
		PassthroughUserHeaders: true,
		CustomUpstreamHeaders: map[string]string{
			"X-Ignored": "value",
		},
	}
	ApplyHeaderOverrides(req, cfg, "")

	if !strings.Contains(buf.String(), "custom_upstream_headers ignored") {
		t.Errorf("expected debug log about ignored custom headers, got: %q", buf.String())
	}
	if req.Header.Get("X-Tenant") != "acme" {
		t.Errorf("passthrough header X-Tenant not applied: %q", req.Header.Get("X-Tenant"))
	}
	if req.Header.Get("X-Ignored") != "" {
		t.Errorf("custom header should be ignored when passthrough active, got %q", req.Header.Get("X-Ignored"))
	}
}

func TestApplyHeaderOverrides_PassthroughAlwaysWarnsWithCustomHeaders(t *testing.T) {
	// When both passthrough and custom headers are configured, the warn fires
	// regardless of whether the request context carries user headers — the
	// warning is about config-shape, not per-request data.
	buf := captureLogger(t)
	req, _ := http.NewRequest(http.MethodPost, "http://example.com", nil)
	cfg := HeaderOverridesConfig{
		PassthroughUserHeaders: true,
		CustomUpstreamHeaders: map[string]string{
			"X-Ignored": "value",
		},
	}
	ApplyHeaderOverrides(req, cfg, "")
	if !strings.Contains(buf.String(), "custom_upstream_headers ignored") {
		t.Errorf("expected debug warning when both passthrough and custom headers set, got: %q", buf.String())
	}
}

func TestPassthroughContextRoundTrip(t *testing.T) {
	t.Run("round-trip preserves headers", func(t *testing.T) {
		want := http.Header{
			"X-Tenant":     {"acme"},
			"Content-Type": {"application/json"},
		}
		ctx := WithPassthroughHeaders(context.Background(), want)
		got := PassthroughHeadersFromContext(ctx)
		if got == nil {
			t.Fatal("expected headers from context, got nil")
		}
		if got.Get("X-Tenant") != "acme" {
			t.Errorf("X-Tenant = %q, want acme", got.Get("X-Tenant"))
		}
		if got.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got.Get("Content-Type"))
		}
	})

	t.Run("missing context key returns nil", func(t *testing.T) {
		got := PassthroughHeadersFromContext(context.Background())
		if got != nil {
			t.Errorf("expected nil for empty context, got %v", got)
		}
	})

	t.Run("wrong type in context returns nil", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), passthroughCtxKey{}, "not a header")
		got := PassthroughHeadersFromContext(ctx)
		if got != nil {
			t.Errorf("expected nil for wrong type, got %v", got)
		}
	})

	t.Run("storing nil headers returns nil on retrieval", func(t *testing.T) {
		ctx := WithPassthroughHeaders(context.Background(), nil)
		got := PassthroughHeadersFromContext(ctx)
		if got != nil {
			t.Errorf("expected nil for stored nil, got %v", got)
		}
	})
}

func TestShouldForward(t *testing.T) {
	skipSet := normalizeHeaderSet([]string{"X-Skip-Me", "X-Other"})

	tests := []struct {
		name          string
		header        string
		mode          string
		userPathAlias string
		want          bool
	}{
		{name: "skip mode: not in skip list", header: "X-Tenant", mode: "skip", want: true},
		{name: "skip mode: in skip list", header: "X-Skip-Me", mode: "skip", want: false},
		{name: "skip mode: case-insensitive", header: "x-skip-me", mode: "skip", want: false},
		{name: "skip mode: empty mode defaults to skip", header: "X-Skip-Me", mode: "", want: false},
		{name: "allow mode: in allow list", header: "X-Other", mode: "allow", want: true},
		{name: "allow mode: not in allow list", header: "X-Tenant", mode: "allow", want: false},
		{name: "allow mode: case-insensitive", header: "x-skip-me", mode: "allow", want: true},
		{name: "unknown mode: defaults to skip behavior", header: "X-Skip-Me", mode: "bogus", want: false},
		{name: "unknown mode: not in skip list passes", header: "X-Tenant", mode: "bogus", want: true},
		{name: "credential blocked regardless of mode", header: "Authorization", mode: "skip", want: false},
		{name: "credential blocked in allow mode", header: "Authorization", mode: "allow", want: false},
		{name: "internal header blocked", header: "X-GoModel-User-Path", mode: "skip", want: false},
		{name: "user path alias blocked", header: "X-My-Alias", mode: "skip", userPathAlias: "X-My-Alias", want: false},
		{name: "user path alias blocked in allow mode", header: "X-My-Alias", mode: "allow", userPathAlias: "X-My-Alias", want: false},
		{name: "user path alias not set does not block extras", header: "X-My-Alias", mode: "skip", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldForward(tt.header, skipSet, tt.mode, tt.userPathAlias)
			if got != tt.want {
				t.Errorf("shouldForward(%q, mode=%q, alias=%q) = %v, want %v", tt.header, tt.mode, tt.userPathAlias, got, tt.want)
			}
		})
	}
}

func TestApplyHeaderOverrides_PassthroughAppendsToExisting(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "http://example.com", nil)
	req.Header.Set("X-Existing", "preset")
	ctx := WithPassthroughHeaders(req.Context(), http.Header{
		"X-Tenant": {"acme"},
	})
	req = req.WithContext(ctx)

	cfg := HeaderOverridesConfig{
		PassthroughUserHeaders: true,
		SkipMode:               "skip",
	}
	ApplyHeaderOverrides(req, cfg, "")

	if got := req.Header.Get("X-Existing"); got != "preset" {
		t.Errorf("existing header overwritten: %q", got)
	}
	if got := req.Header.Get("X-Tenant"); got != "acme" {
		t.Errorf("passthrough header not applied: %q", got)
	}
}

// TestApplyHeaderOverrides_PassthroughMode_ForwardsFilteredHeaders verifies
// the happy-path passthrough contract: with PassthroughUserHeaders enabled and
// no skip/allow list, every non-blocked context header is forwarded to the
// outbound request, and the request header map is enriched without mutating
// the source context.
func TestApplyHeaderOverrides_PassthroughMode_ForwardsFilteredHeaders(t *testing.T) {
	src := http.Header{
		"X-Tenant":     {"acme"},
		"X-Custom":     {"hello"},
		"Content-Type": {"application/json"},
		"Accept":       {"application/json"},
	}

	req, _ := http.NewRequest(http.MethodPost, "http://example.com", nil)
	ctx := WithPassthroughHeaders(req.Context(), src)
	req = req.WithContext(ctx)

	cfg := HeaderOverridesConfig{
		PassthroughUserHeaders: true,
		SkipMode:               "skip",
	}
	ApplyHeaderOverrides(req, cfg, "")

	for k, want := range map[string]string{
		"X-Tenant":     "acme",
		"X-Custom":     "hello",
		"Content-Type": "application/json",
		"Accept":       "application/json",
	} {
		if got := req.Header.Get(k); got != want {
			t.Errorf("header %q = %q, want %q", k, got, want)
		}
	}

	// Source context must remain untouched.
	if got := src.Get("X-Tenant"); got != "acme" {
		t.Errorf("source context X-Tenant mutated: %q", got)
	}
}

// TestApplyHeaderOverrides_PassthroughMode_SkipList verifies that the skip
// mode (default) forwards all headers except those explicitly listed in
// SkipHeaders. Listing is case-insensitive and trims whitespace.
func TestApplyHeaderOverrides_PassthroughMode_SkipList(t *testing.T) {
	src := http.Header{
		"X-Tenant":     {"acme"},
		"X-Skip-Me":    {"nope"},
		"Content-Type": {"application/json"},
	}

	req, _ := http.NewRequest(http.MethodPost, "http://example.com", nil)
	ctx := WithPassthroughHeaders(req.Context(), src)
	req = req.WithContext(ctx)

	cfg := HeaderOverridesConfig{
		PassthroughUserHeaders: true,
		SkipHeaders:            []string{"  x-skip-me  "},
		SkipMode:               "skip",
	}
	ApplyHeaderOverrides(req, cfg, "")

	if got := req.Header.Get("X-Tenant"); got != "acme" {
		t.Errorf("X-Tenant = %q, want %q", got, "acme")
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}
	if got := req.Header.Get("X-Skip-Me"); got != "" {
		t.Errorf("X-Skip-Me should be skipped, got %q", got)
	}
}

// TestApplyHeaderOverrides_PassthroughMode_AllowMode verifies that allow mode
// forwards only headers present in SkipHeaders (interpreted as the allow
// list). Any other context header must not reach the outbound request.
func TestApplyHeaderOverrides_PassthroughMode_AllowMode(t *testing.T) {
	src := http.Header{
		"X-Tenant":     {"acme"},
		"X-Allowed":    {"yes"},
		"X-Not-Listed": {"no"},
		"X-Other":      {"x"},
	}

	req, _ := http.NewRequest(http.MethodPost, "http://example.com", nil)
	ctx := WithPassthroughHeaders(req.Context(), src)
	req = req.WithContext(ctx)

	cfg := HeaderOverridesConfig{
		PassthroughUserHeaders: true,
		SkipHeaders:            []string{"X-Tenant", "X-Allowed"},
		SkipMode:               "allow",
	}
	ApplyHeaderOverrides(req, cfg, "")

	if got := req.Header.Get("X-Tenant"); got != "acme" {
		t.Errorf("X-Tenant = %q, want %q", got, "acme")
	}
	if got := req.Header.Get("X-Allowed"); got != "yes" {
		t.Errorf("X-Allowed = %q, want %q", got, "yes")
	}
	for _, missing := range []string{"X-Not-Listed", "X-Other"} {
		if got := req.Header.Get(missing); got != "" {
			t.Errorf("%s must be blocked in allow mode, got %q", missing, got)
		}
	}
}

// TestApplyHeaderOverrides_PassthroughMode_BlockedHeadersNeverForwarded
// guarantees the hard-coded floor can never be bypassed. Even when a credential
// header is explicitly listed in the allow list, allow mode must drop it
// because the floor check runs before skip/allow evaluation.
func TestApplyHeaderOverrides_PassthroughMode_BlockedHeadersNeverForwarded(t *testing.T) {
	src := http.Header{
		"Authorization":       {"Bearer user"},
		"X-Api-Key":           {"k"},
		"X-GoModel-Key":       {"mgmt"},
		"Cookie":              {"session=secret"},
		"X-GoModel-User-Path": {"/v1/x"},
		"X-Tenant":            {"acme"},
	}

	req, _ := http.NewRequest(http.MethodPost, "http://example.com", nil)
	ctx := WithPassthroughHeaders(req.Context(), src)
	req = req.WithContext(ctx)

	cfg := HeaderOverridesConfig{
		PassthroughUserHeaders: true,
		SkipHeaders: []string{
			"Authorization",
			"X-Api-Key",
			"X-GoModel-Key",
			"Cookie",
			"X-GoModel-User-Path",
			"X-Tenant",
		},
		SkipMode: "allow",
	}
	ApplyHeaderOverrides(req, cfg, "")

	// X-Tenant is the only non-blocked header and must come through.
	if got := req.Header.Get("X-Tenant"); got != "acme" {
		t.Errorf("X-Tenant = %q, want %q", got, "acme")
	}
	// Every blocked header must remain absent regardless of the allow list.
	for _, blocked := range []string{"Authorization", "X-Api-Key", "X-GoModel-Key", "Cookie", "X-GoModel-User-Path"} {
		if got := req.Header.Get(blocked); got != "" {
			t.Errorf("blocked header %q leaked through passthrough allow list: %q", blocked, got)
		}
	}
}

// capturingSlogHandler is a minimal slog.Handler used by the passthrough
// logging test to assert on emitted log records without coupling to the
// global slog default.
type capturingSlogHandler struct {
	records []slogRecord
}

type slogRecord struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

func (h *capturingSlogHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *capturingSlogHandler) Handle(_ context.Context, r slog.Record) error {
	rec := slogRecord{level: r.Level, msg: r.Message, attrs: make(map[string]any)}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.Any()
		return true
	})
	h.records = append(h.records, rec)
	return nil
}
func (h *capturingSlogHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *capturingSlogHandler) WithGroup(_ string) slog.Handler      { return h }

// TestApplyHeaderOverrides_PassthroughMode_LogsIgnoredCustomHeaders verifies
// the operator-facing debug log emitted when both PassthroughUserHeaders and
// CustomUpstreamHeaders are configured. The test installs a dedicated
// capturing slog.Handler on a transient logger and asserts the message lands
// at debug level with the expected content.
func TestApplyHeaderOverrides_PassthroughMode_LogsIgnoredCustomHeaders(t *testing.T) {
	handler := &capturingSlogHandler{}
	logger := slog.New(handler)
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(prev) })

	req, _ := http.NewRequest(http.MethodPost, "http://example.com", nil)
	ctx := WithPassthroughHeaders(req.Context(), http.Header{"X-Tenant": {"acme"}})
	req = req.WithContext(ctx)

	cfg := HeaderOverridesConfig{
		PassthroughUserHeaders: true,
		CustomUpstreamHeaders: map[string]string{
			"X-Provider-Region": "us-east-1",
			"X-Trace-Id":        "abc-123",
		},
	}
	ApplyHeaderOverrides(req, cfg, "")

	// Find the ignorelog record.
	var found *slogRecord
	for i := range handler.records {
		if handler.records[i].level == slog.LevelDebug &&
			strings.Contains(handler.records[i].msg, "custom_upstream_headers") {
			found = &handler.records[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected debug log about ignored custom headers, got records: %+v", handler.records)
	}
	if !strings.Contains(found.msg, "ignored") {
		t.Errorf("expected 'ignored' in message, got %q", found.msg)
	}
	if !strings.Contains(found.msg, "passthrough_user_headers") {
		t.Errorf("expected message to reference passthrough_user_headers, got %q", found.msg)
	}

	// Sanity: passthrough still applied, custom headers did not leak.
	if got := req.Header.Get("X-Tenant"); got != "acme" {
		t.Errorf("passthrough header X-Tenant not applied: %q", got)
	}
	if got := req.Header.Get("X-Provider-Region"); got != "" {
		t.Errorf("X-Provider-Region should be ignored when passthrough active, got %q", got)
	}
}
