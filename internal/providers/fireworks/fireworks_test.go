package fireworks

import (
	"context"
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
			"id":"chatcmpl-fireworks",
			"created":1677652288,
			"model":"accounts/fireworks/models/gpt-oss-120b",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("fw-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "accounts/fireworks/models/gpt-oss-120b",
		Messages: []core.Message{
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if resp.Model != "accounts/fireworks/models/gpt-oss-120b" {
		t.Fatalf("resp.Model = %q, want accounts/fireworks/models/gpt-oss-120b", resp.Model)
	}
	if resp.Usage.TotalTokens != 4 {
		t.Fatalf("resp.Usage = %+v, want total_tokens=4", resp.Usage)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer fw-key" {
		t.Fatalf("authorization = %q, want Bearer fw-key", gotAuth)
	}
}

func TestEmbeddings_DelegatesToCompatibleProvider(t *testing.T) {
	var gotPath string
	var gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object":"list",
			"model":"nomic-ai/nomic-embed-text-v1.5",
			"data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],
			"usage":{"prompt_tokens":3,"total_tokens":3}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("fw-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "nomic-ai/nomic-embed-text-v1.5",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embeddings() error = %v", err)
	}
	if resp.Model != "nomic-ai/nomic-embed-text-v1.5" {
		t.Fatalf("resp.Model = %q, want nomic-ai/nomic-embed-text-v1.5", resp.Model)
	}
	if gotPath != "/embeddings" {
		t.Fatalf("path = %q, want /embeddings", gotPath)
	}
	if gotAuth != "Bearer fw-key" {
		t.Fatalf("authorization = %q, want Bearer fw-key", gotAuth)
	}
}

func TestListModels_UsesModelsEndpoint(t *testing.T) {
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object":"list",
			"data":[{"id":"accounts/fireworks/models/gpt-oss-120b","object":"model","created":1677652288,"owned_by":"fireworks"}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("fw-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "accounts/fireworks/models/gpt-oss-120b" {
		t.Fatalf("resp.Data = %+v, want one model accounts/fireworks/models/gpt-oss-120b", resp.Data)
	}
	if gotPath != "/models" {
		t.Fatalf("path = %q, want /models", gotPath)
	}
}

func TestProvider_DoesNotExposeOptionalOpenAICompatibleInterfaces(t *testing.T) {
	provider := NewWithHTTPClient("fw-key", "", nil, llmclient.Hooks{})

	if _, ok := any(provider).(core.NativeBatchProvider); ok {
		t.Fatal("fireworks provider should not implement native batch provider")
	}
	if _, ok := any(provider).(core.NativeFileProvider); ok {
		t.Fatal("fireworks provider should not implement native file provider")
	}
	if _, ok := any(provider).(core.AudioProvider); ok {
		t.Fatal("fireworks provider should not implement audio provider")
	}
}
