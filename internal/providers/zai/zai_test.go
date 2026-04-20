package zai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
)

func TestChatCompletion_UsesBearerAuthAndChatEndpoint(t *testing.T) {
	var gotPath string
	var gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-zai",
			"created":1677652288,
			"model":"glm-5",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("zai-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "glm-5",
		Messages: []core.Message{
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if resp.Model != "glm-5" {
		t.Fatalf("resp.Model = %q, want glm-5", resp.Model)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer zai-key" {
		t.Fatalf("authorization = %q, want Bearer zai-key", gotAuth)
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
			"model":"embedding-model",
			"data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],
			"usage":{"prompt_tokens":3,"total_tokens":3}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("zai-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "embedding-model",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embeddings() error = %v", err)
	}
	if resp.Model != "embedding-model" {
		t.Fatalf("resp.Model = %q, want embedding-model", resp.Model)
	}
	if gotPath != "/embeddings" {
		t.Fatalf("path = %q, want /embeddings", gotPath)
	}
	if gotAuth != "Bearer zai-key" {
		t.Fatalf("authorization = %q, want Bearer zai-key", gotAuth)
	}
}

func TestProvider_DoesNotExposeOptionalOpenAICompatibleInterfaces(t *testing.T) {
	provider := NewWithHTTPClient("zai-key", "", nil, llmclient.Hooks{})

	if _, ok := any(provider).(core.NativeBatchProvider); ok {
		t.Fatal("zai provider should not implement native batch provider")
	}
	if _, ok := any(provider).(core.NativeFileProvider); ok {
		t.Fatal("zai provider should not implement native file provider")
	}
}
