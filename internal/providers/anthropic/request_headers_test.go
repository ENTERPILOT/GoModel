package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

func TestSetRequestHeaders_AddsHookHeadersToEveryRequest(t *testing.T) {
	var (
		mu  sync.Mutex
		got http.Header
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)
	provider.SetRequestHeaders(func(ctx context.Context) http.Header {
		return http.Header{
			"X-Extra":    {"from-hook", "second"},
			"User-Agent": {"gomodel-test"},
		}
	})

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if v := got.Values("X-Extra"); len(v) != 2 || v[0] != "from-hook" || v[1] != "second" {
		t.Fatalf("X-Extra = %v, want [from-hook second]", v)
	}
	if v := got.Get("User-Agent"); v != "gomodel-test" {
		t.Fatalf("User-Agent = %q, want gomodel-test (hook replaces the default)", v)
	}
	if got.Get("x-api-key") != "test-api-key" || got.Get("anthropic-version") == "" {
		t.Fatalf("standard headers must survive the hook, got %v", got)
	}
}
