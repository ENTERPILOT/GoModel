package vllm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
)

func TestChatCompletion_UsesOptionalBearerAuthAndChatEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		apiKey   string
		wantAuth string
	}{
		{name: "with api key", apiKey: "vllm-key", wantAuth: "Bearer vllm-key"},
		{name: "without api key"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			var gotAuth string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotAuth = r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{
					"id":"chatcmpl-vllm",
					"created":1677652288,
					"model":"meta-llama/Llama-3.1-8B-Instruct",
					"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]
				}`))
			}))
			defer server.Close()

			provider := NewWithHTTPClient(tt.apiKey, server.URL, server.Client(), llmclient.Hooks{})

			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model: "meta-llama/Llama-3.1-8B-Instruct",
				Messages: []core.Message{
					{Role: "user", Content: "hi"},
				},
			})
			if err != nil {
				t.Fatalf("ChatCompletion() error = %v", err)
			}
			if resp.Model != "meta-llama/Llama-3.1-8B-Instruct" {
				t.Fatalf("resp.Model = %q, want meta-llama/Llama-3.1-8B-Instruct", resp.Model)
			}
			if gotPath != "/chat/completions" {
				t.Fatalf("path = %q, want /chat/completions", gotPath)
			}
			if gotAuth != tt.wantAuth {
				t.Fatalf("authorization = %q, want %q", gotAuth, tt.wantAuth)
			}
		})
	}
}

func TestEmbeddings_DelegatesToCompatibleProvider(t *testing.T) {
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object":"list",
			"model":"BAAI/bge-small-en-v1.5",
			"data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],
			"usage":{"prompt_tokens":3,"total_tokens":3}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "BAAI/bge-small-en-v1.5",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embeddings() error = %v", err)
	}
	if resp.Model != "BAAI/bge-small-en-v1.5" {
		t.Fatalf("resp.Model = %q, want BAAI/bge-small-en-v1.5", resp.Model)
	}
	if gotPath != "/embeddings" {
		t.Fatalf("path = %q, want /embeddings", gotPath)
	}
}

func TestProvider_ExposesPassthroughButNotOptionalNativeInterfaces(t *testing.T) {
	provider := NewWithHTTPClient("", "", nil, llmclient.Hooks{})

	if _, ok := any(provider).(core.PassthroughProvider); !ok {
		t.Fatal("vllm provider should implement passthrough provider")
	}
	if _, ok := any(provider).(core.NativeBatchProvider); ok {
		t.Fatal("vllm provider should not implement native batch provider")
	}
	if _, ok := any(provider).(core.NativeFileProvider); ok {
		t.Fatal("vllm provider should not implement native file provider")
	}
	if _, ok := any(provider).(core.NativeResponseLifecycleProvider); ok {
		t.Fatal("vllm provider should not implement native response lifecycle provider")
	}
}

func TestPassthrough_ForwardsProviderNativeEndpoint(t *testing.T) {
	var gotPath string
	var gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tokens":[1,2,3]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("vllm-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "tokenize",
		Body:     io.NopCloser(strings.NewReader("{}")),
		Headers:  http.Header{"Content-Type": []string{"application/json"}},
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	defer resp.Body.Close()

	if gotPath != "/tokenize" {
		t.Fatalf("path = %q, want /tokenize", gotPath)
	}
	if gotAuth != "Bearer vllm-key" {
		t.Fatalf("authorization = %q, want Bearer vllm-key", gotAuth)
	}
}

func TestPassthrough_UsesRootForNativeEndpointsWhenBaseURLIncludesV1(t *testing.T) {
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tokens":[1,2,3]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("", server.URL+"/v1", server.Client(), llmclient.Hooks{})

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "tokenize",
		Body:     io.NopCloser(strings.NewReader("{}")),
		Headers:  http.Header{"Content-Type": []string{"application/json"}},
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	defer resp.Body.Close()

	if gotPath != "/tokenize" {
		t.Fatalf("path = %q, want /tokenize", gotPath)
	}
}

func TestPassthrough_UsesV1ForOpenAICompatibleEndpointsWhenBaseURLIncludesV1(t *testing.T) {
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-vllm",
			"created":1677652288,
			"model":"Qwen/Qwen2.5-0.5B-Instruct",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("", server.URL+"/v1", server.Client(), llmclient.Hooks{})

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "chat/completions",
		Body: io.NopCloser(strings.NewReader(`{
			"model":"Qwen/Qwen2.5-0.5B-Instruct",
			"messages":[{"role":"user","content":"hi"}]
		}`)),
		Headers: http.Header{"Content-Type": []string{"application/json"}},
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	defer resp.Body.Close()

	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q, want /v1/chat/completions", gotPath)
	}
}
