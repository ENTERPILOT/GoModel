package sglang

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

func TestChatCompletionUsesOptionalBearerAuthAndV1Endpoint(t *testing.T) {
	for _, tt := range []struct {
		name     string
		apiKey   string
		wantAuth string
	}{
		{name: "with API key", apiKey: "sglang-key", wantAuth: "Bearer sglang-key"},
		{name: "without API key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath, gotAuth string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotAuth = r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{
					"id":"chatcmpl-sglang",
					"created":1677652288,
					"model":"HuggingFaceTB/SmolLM2-135M-Instruct",
					"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]
				}`))
			}))
			defer server.Close()

			provider := NewWithHTTPClient(tt.apiKey, server.URL+"/v1", server.Client(), llmclient.Hooks{})
			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:    "HuggingFaceTB/SmolLM2-135M-Instruct",
				Messages: []core.Message{{Role: "user", Content: "hi"}},
			})
			if err != nil {
				t.Fatalf("ChatCompletion() error = %v", err)
			}
			if resp.Model != "HuggingFaceTB/SmolLM2-135M-Instruct" {
				t.Fatalf("resp.Model = %q", resp.Model)
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

func TestChatCompletionPreservesSGLangExtensionFields(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-sglang","model":"test","choices":[]}`))
	}))
	defer server.Close()

	var req core.ChatRequest
	if err := json.Unmarshal([]byte(`{
		"model":"test",
		"messages":[{"role":"user","content":"hi"}],
		"chat_template_kwargs":{"enable_thinking":false},
		"separate_reasoning":true
	}`), &req); err != nil {
		t.Fatalf("decode ChatRequest: %v", err)
	}

	provider := NewWithHTTPClient("", server.URL+"/v1", server.Client(), llmclient.Hooks{})
	if _, err := provider.ChatCompletion(context.Background(), &req); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}

	kwargs, ok := gotBody["chat_template_kwargs"].(map[string]any)
	if !ok || kwargs["enable_thinking"] != false {
		t.Fatalf("chat_template_kwargs = %#v", gotBody["chat_template_kwargs"])
	}
	if gotBody["separate_reasoning"] != true {
		t.Fatalf("separate_reasoning = %#v", gotBody["separate_reasoning"])
	}
}

func TestOpenAICompatibleEndpoints(t *testing.T) {
	tests := []struct {
		name     string
		wantPath string
		call     func(*Provider) error
		response string
	}{
		{
			name:     "models",
			wantPath: "/v1/models",
			response: `{"object":"list","data":[]}`,
			call: func(p *Provider) error {
				_, err := p.ListModels(context.Background())
				return err
			},
		},
		{
			name:     "responses",
			wantPath: "/v1/responses",
			response: `{"id":"resp-sglang","object":"response","status":"completed","model":"test","output":[]}`,
			call: func(p *Provider) error {
				_, err := p.Responses(context.Background(), &core.ResponsesRequest{Model: "test"})
				return err
			},
		},
		{
			name:     "embeddings",
			wantPath: "/v1/embeddings",
			response: `{"object":"list","model":"test","data":[{"object":"embedding","embedding":[0.1],"index":0}],"usage":{"prompt_tokens":1,"total_tokens":1}}`,
			call: func(p *Provider) error {
				_, err := p.Embeddings(context.Background(), &core.EmbeddingRequest{Model: "test", Input: "hello"})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			provider := NewWithHTTPClient("", server.URL+"/v1", server.Client(), llmclient.Hooks{})
			if err := tt.call(provider); err != nil {
				t.Fatalf("call error = %v", err)
			}
			if gotPath != tt.wantPath {
				t.Fatalf("path = %q, want %q", gotPath, tt.wantPath)
			}
		})
	}
}

func TestProviderExposesOnlyVerifiedOptionalInterfaces(t *testing.T) {
	provider := NewWithHTTPClient("", "", nil, llmclient.Hooks{})

	if _, ok := any(provider).(core.PassthroughProvider); !ok {
		t.Fatal("sglang provider should implement passthrough provider")
	}
	if _, ok := any(provider).(core.NativeBatchProvider); ok {
		t.Fatal("sglang provider should not implement native batch provider")
	}
	if _, ok := any(provider).(core.NativeFileProvider); ok {
		t.Fatal("sglang provider should not implement native file provider")
	}
	if _, ok := any(provider).(core.NativeResponseLifecycleProvider); ok {
		t.Fatal("sglang provider should not implement native response lifecycle provider")
	}
}

func TestPassthroughRoutesNativeAndOpenAIEndpoints(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		wantPath string
	}{
		{name: "native generate", endpoint: "generate", wantPath: "/generate"},
		{name: "native health", endpoint: "health", wantPath: "/health"},
		{name: "explicit v1 rerank", endpoint: "v1/rerank", wantPath: "/v1/rerank"},
		{name: "OpenAI chat", endpoint: "chat/completions", wantPath: "/v1/chat/completions"},
		{name: "OpenAI models with query", endpoint: "models?limit=1", wantPath: "/v1/models"},
		{name: "OpenAI responses lifecycle", endpoint: "responses/resp-1", wantPath: "/v1/responses/resp-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath, gotAuth string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotAuth = r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer server.Close()

			provider := NewWithHTTPClient("sglang-key", server.URL+"/v1", server.Client(), llmclient.Hooks{})
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
			if gotAuth != "Bearer sglang-key" {
				t.Fatalf("authorization = %q, want Bearer sglang-key", gotAuth)
			}
		})
	}
}

func TestNewSharesKeyRotationWithNativePassthrough(t *testing.T) {
	var gotAuth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer server.Close()

	keys := providers.NewKeyring("key-one", "key-two")
	provider := New(providers.ProviderConfig{
		Type:    "sglang",
		APIKey:  "key-one",
		BaseURL: server.URL + "/v1",
	}, providers.ProviderOptions{Keys: keys}).(*Provider)

	if _, err := provider.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodGet,
		Endpoint: "health",
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	defer resp.Body.Close()

	if len(gotAuth) != 2 || gotAuth[0] != "Bearer key-one" || gotAuth[1] != "Bearer key-two" {
		t.Fatalf("authorization headers = %v", gotAuth)
	}
}

func TestSetBaseURLUpdatesOpenAIAndNativeClients(t *testing.T) {
	var gotPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("", "http://127.0.0.1:1/v1", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL + "/v1")
	if _, err := provider.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodGet,
		Endpoint: "health",
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	defer resp.Body.Close()

	if len(gotPaths) != 2 || gotPaths[0] != "/v1/models" || gotPaths[1] != "/health" {
		t.Fatalf("paths = %v, want [/v1/models /health]", gotPaths)
	}
}
