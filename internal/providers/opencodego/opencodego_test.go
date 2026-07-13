package opencodego

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// newTestProvider builds a provider whose OpenAI-compatible and Anthropic
// /messages paths both point at the same test server.
func newTestProvider(serverURL string, client *http.Client) *Provider {
	return NewWithHTTPClient("sk-opencode", serverURL, client, llmclient.Hooks{})
}

func TestChatCompletion_OpenAIStyleModel_UsesChatCompletions(t *testing.T) {
	var gotPath, gotAuth string

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

	resp, err := newTestProvider(server.URL, server.Client()).ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "glm-5.1",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
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

func TestChatCompletion_AnthropicStyleModel_UsesMessages(t *testing.T) {
	var gotPath, gotAuth, gotAPIKey, gotVersion string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_opencode",
			"model":"qwen3.7-max",
			"content":[{"type":"text","text":"hello"}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":5,"output_tokens":2}
		}`))
	}))
	defer server.Close()

	resp, err := newTestProvider(server.URL, server.Client()).ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "qwen3.7-max",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if gotPath != "/messages" {
		t.Fatalf("path = %q, want /messages", gotPath)
	}
	if gotAPIKey != "sk-opencode" {
		t.Fatalf("x-api-key = %q, want sk-opencode", gotAPIKey)
	}
	if gotAuth != "" {
		t.Fatalf("authorization = %q, want empty (messages uses x-api-key)", gotAuth)
	}
	if gotVersion == "" {
		t.Fatal("anthropic-version header missing on /messages request")
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "hello" {
		t.Fatalf("unexpected response: %+v", resp.Choices)
	}
}

func TestStreamChatCompletion_RoutesByModel(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		wantPath string
	}{
		{"openai-style", "glm-5.1", "/chat/completions"},
		{"anthropic-style", "qwen3.7-max", "/messages"},
		{"prefixed anthropic-style", "opencode_go/qwen3.7-max", "/messages"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: [DONE]\n\n"))
			}))
			defer server.Close()

			stream, err := newTestProvider(server.URL, server.Client()).StreamChatCompletion(context.Background(), &core.ChatRequest{
				Model:    tt.model,
				Messages: []core.Message{{Role: "user", Content: "hi"}},
			})
			if err != nil {
				t.Fatalf("StreamChatCompletion() error = %v", err)
			}
			_, _ = io.Copy(io.Discard, stream)
			_ = stream.Close()
			if gotPath != tt.wantPath {
				t.Fatalf("path = %q, want %q", gotPath, tt.wantPath)
			}
		})
	}
}

func TestMessagesModels_EnvOverride(t *testing.T) {
	t.Setenv(messagesModelsEnvVar, "foo-model, bar-model ")
	p := NewWithHTTPClient("sk-opencode", "", nil, llmclient.Hooks{})

	if !p.usesMessages("foo-model") || !p.usesMessages("bar-model") {
		t.Fatal("override models should route to /messages")
	}
	if p.usesMessages("qwen3.7-max") {
		t.Fatal("default model should not apply when override is set")
	}
	if !p.usesMessages("opencode_go/foo-model") {
		t.Fatal("provider-qualified model should match after prefix strip")
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

	resp, err := newTestProvider(server.URL, server.Client()).ListModels(context.Background())
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
	_, err := newTestProvider("", nil).Embeddings(context.Background(), &core.EmbeddingRequest{
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
	provider := newTestProvider("", nil)

	if _, ok := any(provider).(core.NativeBatchProvider); ok {
		t.Fatal("opencode_go provider should not implement native batch provider")
	}
	if _, ok := any(provider).(core.NativeFileProvider); ok {
		t.Fatal("opencode_go provider should not implement native file provider")
	}
}
