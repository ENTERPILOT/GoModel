package opencodego

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
			"id":"chatcmpl-opencode",
			"created":1677652288,
			"model":"glm-5.1",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("sk-opencode", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "glm-5.1",
		Messages: []core.Message{
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if resp.Model != "glm-5.1" {
		t.Fatalf("resp.Model = %q, want glm-5.1", resp.Model)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer sk-opencode" {
		t.Fatalf("authorization = %q, want Bearer sk-opencode", gotAuth)
	}
}

func TestListModels_NormalizesResponse(t *testing.T) {
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object":"list",
			"data":[
				{"id":"kimi-k2.7-code","object":"model","created":1781462836,"owned_by":"opencode"},
				{"id":"glm-5.1","object":"model","created":1781462836,"owned_by":"opencode"}
			]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("sk-opencode", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if gotPath != "/models" {
		t.Fatalf("path = %q, want /models", gotPath)
	}
	if len(resp.Data) != 2 || resp.Data[0].ID != "kimi-k2.7-code" {
		t.Fatalf("unexpected models response: %+v", resp.Data)
	}
}

func TestEmbeddings_Unsupported(t *testing.T) {
	provider := NewWithHTTPClient("sk-opencode", "", nil, llmclient.Hooks{})

	_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "glm-5.1",
		Input: "hello",
	})
	if err == nil {
		t.Fatal("Embeddings() error = nil, want invalid_request_error")
	}
	gwErr, ok := err.(*core.GatewayError)
	if !ok {
		t.Fatalf("error type = %T, want *core.GatewayError", err)
	}
	if gwErr.HTTPStatusCode() != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", gwErr.HTTPStatusCode(), http.StatusBadRequest)
	}
}

func TestProvider_DoesNotExposeOptionalOpenAICompatibleInterfaces(t *testing.T) {
	provider := NewWithHTTPClient("sk-opencode", "", nil, llmclient.Hooks{})

	if _, ok := any(provider).(core.NativeBatchProvider); ok {
		t.Fatal("opencode_go provider should not implement native batch provider")
	}
	if _, ok := any(provider).(core.NativeFileProvider); ok {
		t.Fatal("opencode_go provider should not implement native file provider")
	}
}
