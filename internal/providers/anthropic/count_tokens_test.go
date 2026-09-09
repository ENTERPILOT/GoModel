package anthropic

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/llmclient"
)

// Anthropic counts tokens exactly through /v1/messages/count_tokens. The
// original request body goes upstream unchanged apart from two things: the
// model is the resolved one, and fields the count endpoint does not accept
// (max_tokens, stream, sampling) are left out, because Anthropic rejects
// unknown fields rather than ignoring them.
func TestCountMessagesTokens(t *testing.T) {
	var gotPath string
	var gotBody map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"input_tokens":2414}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	body := []byte(`{"model":"anthropic/claude-haiku-4-5","max_tokens":64,"stream":true,"temperature":0.2,"metadata":{"user_id":"u"},
		"system":"be terse","messages":[{"role":"user","content":"hi"}],
		"tools":[{"name":"t","input_schema":{"type":"object"}}],"tool_choice":{"type":"auto"},"thinking":{"type":"enabled","budget_tokens":1024}}`)
	count, err := provider.CountMessagesTokens(context.Background(), "claude-haiku-4-5", body)
	if err != nil {
		t.Fatalf("CountMessagesTokens: %v", err)
	}
	if count != 2414 {
		t.Errorf("count = %d, want 2414 from upstream", count)
	}
	if gotPath != "/messages/count_tokens" {
		t.Errorf("path = %q, want /messages/count_tokens", gotPath)
	}
	if got := string(gotBody["model"]); got != `"claude-haiku-4-5"` {
		t.Errorf("model = %s, want the resolved model", got)
	}
	keys := make([]string, 0, len(gotBody))
	for k := range gotBody {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	want := []string{"messages", "model", "system", "thinking", "tool_choice", "tools"}
	if !slices.Equal(keys, want) {
		t.Errorf("forwarded fields = %v, want %v", keys, want)
	}
}

// An upstream failure is returned as an error so the caller can fall back.
func TestCountMessagesTokens_UpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`))
	}))
	defer server.Close()
	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)
	if _, err := provider.CountMessagesTokens(context.Background(), "claude-haiku-4-5", []byte(`{"model":"m","messages":[]}`)); err == nil {
		t.Fatal("expected an error from a 400 upstream")
	}
}
