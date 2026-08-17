package llamacpp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

func TestChatCompletion_UsesOptionalBearerAuthAndChatEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		apiKey   string
		wantAuth string
	}{
		{name: "with api key", apiKey: "llamacpp-key", wantAuth: "Bearer llamacpp-key"},
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
					"id":"chatcmpl-llamacpp",
					"created":1677652288,
					"model":"gemma-3-4b-it",
					"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]
				}`))
			}))
			defer server.Close()

			provider := NewWithHTTPClient(tt.apiKey, server.URL+"/v1", server.Client(), llmclient.Hooks{})

			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model: "gemma-3-4b-it",
				Messages: []core.Message{
					{Role: "user", Content: "hi"},
				},
			})
			if err != nil {
				t.Fatalf("ChatCompletion() error = %v", err)
			}
			if resp.Model != "gemma-3-4b-it" {
				t.Fatalf("resp.Model = %q, want gemma-3-4b-it", resp.Model)
			}
			if gotPath != "/v1/chat/completions" {
				t.Fatalf("path = %q, want /v1/chat/completions", gotPath)
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
			"model":"nomic-embed-text-v1.5",
			"data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],
			"usage":{"prompt_tokens":3,"total_tokens":3}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("", server.URL+"/v1", server.Client(), llmclient.Hooks{})

	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "nomic-embed-text-v1.5",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embeddings() error = %v", err)
	}
	if resp.Model != "nomic-embed-text-v1.5" {
		t.Fatalf("resp.Model = %q, want nomic-embed-text-v1.5", resp.Model)
	}
	if gotPath != "/v1/embeddings" {
		t.Fatalf("path = %q, want /v1/embeddings", gotPath)
	}
}

func TestProvider_ExposesPassthroughButNotOptionalNativeInterfaces(t *testing.T) {
	provider := NewWithHTTPClient("", "", nil, llmclient.Hooks{})

	if _, ok := any(provider).(core.PassthroughProvider); !ok {
		t.Fatal("llamacpp provider should implement passthrough provider")
	}
	if _, ok := any(provider).(core.NativeBatchProvider); ok {
		t.Fatal("llamacpp provider should not implement native batch provider")
	}
	if _, ok := any(provider).(core.NativeFileProvider); ok {
		t.Fatal("llamacpp provider should not implement native file provider")
	}
	if _, ok := any(provider).(core.NativeResponseLifecycleProvider); ok {
		t.Fatal("llamacpp provider should not implement native response lifecycle provider")
	}
	if _, ok := any(provider).(core.AudioProvider); ok {
		t.Fatal("llamacpp provider should not implement audio provider")
	}
}

func TestPassthrough_RoutesNativeEndpointsToServerRoot(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		wantPath string
	}{
		{name: "rerank", endpoint: "rerank", wantPath: "/rerank"},
		{name: "health", endpoint: "health", wantPath: "/health"},
		{name: "tokenize", endpoint: "tokenize", wantPath: "/tokenize"},
		{name: "explicit v1 rerank", endpoint: "v1/rerank", wantPath: "/v1/rerank"},
		{name: "chat completions", endpoint: "chat/completions", wantPath: "/v1/chat/completions"},
		{name: "embeddings", endpoint: "embeddings", wantPath: "/v1/embeddings"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			var gotAuth string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotAuth = r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer server.Close()

			provider := NewWithHTTPClient("llamacpp-key", server.URL+"/v1", server.Client(), llmclient.Hooks{})

			resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
				Method:   http.MethodPost,
				Endpoint: tt.endpoint,
				Body:     io.NopCloser(strings.NewReader("{}")),
				Headers:  http.Header{"Content-Type": []string{"application/json"}},
			})
			if err != nil {
				t.Fatalf("Passthrough() error = %v", err)
			}
			defer resp.Body.Close()

			if gotPath != tt.wantPath {
				t.Fatalf("path = %q, want %q", gotPath, tt.wantPath)
			}
			if gotAuth != "Bearer llamacpp-key" {
				t.Fatalf("authorization = %q, want Bearer llamacpp-key", gotAuth)
			}
		})
	}
}
