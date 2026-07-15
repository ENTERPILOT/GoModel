package xiaomi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

func TestChatCompletion_UsesBearerAuthAndChatEndpoint(t *testing.T) {
	var gotPath string
	var gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-xiaomi",
			"created":1677652288,
			"model":"mimo-v2.5-pro",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("mimo-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "mimo-v2.5-pro",
		Messages: []core.Message{
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if resp.Model != "mimo-v2.5-pro" {
		t.Fatalf("resp.Model = %q, want mimo-v2.5-pro", resp.Model)
	}
	if resp.Usage.TotalTokens != 4 {
		t.Fatalf("resp.Usage = %+v, want total_tokens=4", resp.Usage)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer mimo-key" {
		t.Fatalf("authorization = %q, want Bearer mimo-key", gotAuth)
	}
}

func TestEmbeddings_ReturnsUnsupportedError(t *testing.T) {
	provider := NewWithHTTPClient("mimo-key", "", nil, llmclient.Hooks{})

	_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "mimo-v2.5-pro",
		Input: "hello",
	})
	if err == nil {
		t.Fatal("Embeddings() expected error, got nil")
	}
	var ge *core.GatewayError
	if !errors.As(err, &ge) {
		t.Fatalf("error type = %T, want *core.GatewayError", err)
	}
	if ge.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("error type = %q, want %q", ge.Type, core.ErrorTypeInvalidRequest)
	}
	if ge.HTTPStatusCode() != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", ge.HTTPStatusCode(), http.StatusBadRequest)
	}
}

func TestProvider_DefaultBaseURL(t *testing.T) {
	provider := NewWithHTTPClient("mimo-key", "", nil, llmclient.Hooks{})
	if provider == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestProvider_DoesNotExposeOptionalOpenAICompatibleInterfaces(t *testing.T) {
	provider := NewWithHTTPClient("mimo-key", "", nil, llmclient.Hooks{})

	if _, ok := any(provider).(core.NativeBatchProvider); ok {
		t.Fatal("xiaomi provider should not implement native batch provider")
	}
	if _, ok := any(provider).(core.NativeFileProvider); ok {
		t.Fatal("xiaomi provider should not implement native file provider")
	}
}
