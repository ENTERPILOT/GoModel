package opencodego

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/version"
)

// headerServer records the request headers of the last call and answers with
// a minimal chat completion (or SSE stream) so both endpoint dialects succeed.
func headerServer(t *testing.T) (*httptest.Server, func() http.Header) {
	t.Helper()
	var (
		mu   sync.Mutex
		last http.Header
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		last = r.Header.Clone()
		mu.Unlock()
		if r.Header.Get("Accept") == "text/event-stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/messages" {
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"qwen3.7-max","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","created":1,"model":"glm-5.1","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(server.Close)
	return server, func() http.Header {
		mu.Lock()
		defer mu.Unlock()
		return last
	}
}

func snapshotContext(ctx context.Context, headers map[string][]string) context.Context {
	snapshot := core.NewRequestSnapshot(http.MethodPost, "/v1/chat/completions", nil, nil, headers, "application/json", nil, false, "req-1", nil)
	return core.WithRequestSnapshot(ctx, snapshot)
}

func chatRequest(model string) *core.ChatRequest {
	return &core.ChatRequest{Model: model, Messages: []core.Message{{Role: "user", Content: "hi"}}}
}

func TestRequestHeaders_DetectedSessionForwarded(t *testing.T) {
	server, last := headerServer(t)
	ctx := core.WithSessionID(context.Background(), "session-123")

	for _, model := range []string{"glm-5.1", "qwen3.7-max"} {
		t.Run(model, func(t *testing.T) {
			if _, err := newTestProvider(server.URL, server.Client()).ChatCompletion(ctx, chatRequest(model)); err != nil {
				t.Fatalf("ChatCompletion() error = %v", err)
			}
			got := last()
			if v := got.Get(sessionHeader); v != "session-123" {
				t.Fatalf("%s = %q, want session-123", sessionHeader, v)
			}
			if v := got.Get(clientHeader); v != defaultClient {
				t.Fatalf("%s = %q, want %s", clientHeader, v, defaultClient)
			}
			if v := got.Get("User-Agent"); v != "gomodel/"+version.Version {
				t.Fatalf("User-Agent = %q, want gomodel/%s", v, version.Version)
			}
		})
	}
}

func TestRequestHeaders_StreamCarriesSession(t *testing.T) {
	server, last := headerServer(t)
	ctx := core.WithSessionID(context.Background(), "session-stream")

	for _, model := range []string{"glm-5.1", "qwen3.7-max"} {
		t.Run(model, func(t *testing.T) {
			body, err := newTestProvider(server.URL, server.Client()).StreamChatCompletion(ctx, chatRequest(model))
			if err != nil {
				t.Fatalf("StreamChatCompletion() error = %v", err)
			}
			_, _ = io.ReadAll(body)
			_ = body.Close()
			if v := last().Get(sessionHeader); v != "session-stream" {
				t.Fatalf("%s = %q, want session-stream", sessionHeader, v)
			}
		})
	}
}

func TestRequestHeaders_InboundOpenCodeHeadersWin(t *testing.T) {
	server, last := headerServer(t)
	ctx := core.WithSessionID(context.Background(), "scoped-detected")
	ctx = snapshotContext(ctx, map[string][]string{
		"X-Opencode-Session": {"ses_client"},
		"X-Opencode-Client":  {"pi"},
	})

	if _, err := newTestProvider(server.URL, server.Client()).ChatCompletion(ctx, chatRequest("glm-5.1")); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	got := last()
	if v := got.Get(sessionHeader); v != "ses_client" {
		t.Fatalf("%s = %q, want ses_client", sessionHeader, v)
	}
	if v := got.Get(clientHeader); v != "pi" {
		t.Fatalf("%s = %q, want pi", clientHeader, v)
	}
}

func TestRequestHeaders_NoSessionSendsNoSessionHeader(t *testing.T) {
	server, last := headerServer(t)

	if _, err := newTestProvider(server.URL, server.Client()).ChatCompletion(context.Background(), chatRequest("glm-5.1")); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	got := last()
	if _, ok := got[http.CanonicalHeaderKey(sessionHeader)]; ok {
		t.Fatalf("%s should be absent without a session, got %q", sessionHeader, got.Get(sessionHeader))
	}
	if v := got.Get(clientHeader); v != defaultClient {
		t.Fatalf("%s = %q, want %s", clientHeader, v, defaultClient)
	}
}

func TestRequestHeaders_Disabled(t *testing.T) {
	t.Setenv(sessionHeaderEnvVar, "false")
	server, last := headerServer(t)
	ctx := core.WithSessionID(context.Background(), "session-123")
	ctx = snapshotContext(ctx, map[string][]string{"X-Opencode-Session": {"ses_client"}})

	for _, model := range []string{"glm-5.1", "qwen3.7-max"} {
		t.Run(model, func(t *testing.T) {
			if _, err := newTestProvider(server.URL, server.Client()).ChatCompletion(ctx, chatRequest(model)); err != nil {
				t.Fatalf("ChatCompletion() error = %v", err)
			}
			got := last()
			if _, ok := got[http.CanonicalHeaderKey(sessionHeader)]; ok {
				t.Fatalf("%s should be absent when disabled, got %q", sessionHeader, got.Get(sessionHeader))
			}
			if v := got.Get(clientHeader); v != defaultClient {
				t.Fatalf("%s = %q, want %s (identification is not gated)", clientHeader, v, defaultClient)
			}
		})
	}
}

func TestInboundHeader_RejectsLineBreaks(t *testing.T) {
	ctx := snapshotContext(context.Background(), map[string][]string{
		"X-Opencode-Session": {"bad\r\nvalue", "  good  "},
	})
	if got := inboundHeader(ctx, sessionHeader); got != "good" {
		t.Fatalf("inboundHeader() = %q, want good", got)
	}
	if got := inboundHeader(context.Background(), sessionHeader); got != "" {
		t.Fatalf("inboundHeader() without snapshot = %q, want empty", got)
	}
}

func TestLoadSessionHeaderEnabled(t *testing.T) {
	tests := []struct {
		name  string
		value string
		unset bool
		want  bool
	}{
		{name: "unset defaults on", unset: true, want: true},
		{name: "empty defaults on", value: "  ", want: true},
		{name: "false", value: "false", want: false},
		{name: "zero", value: "0", want: false},
		{name: "true", value: "true", want: true},
		{name: "garbage defaults on", value: "maybe", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.unset {
				t.Setenv(sessionHeaderEnvVar, "") // registers cleanup, then unset for real
				_ = os.Unsetenv(sessionHeaderEnvVar)
			} else {
				t.Setenv(sessionHeaderEnvVar, tt.value)
			}
			if got := loadSessionHeaderEnabled(); got != tt.want {
				t.Fatalf("loadSessionHeaderEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
