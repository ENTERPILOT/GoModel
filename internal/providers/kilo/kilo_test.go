package kilo

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
)

func TestChatCompletion_UsesBearerAuthAndPreservesModelAndTools(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			http.Error(w, "decode error", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-kilo",
			"created":1677652288,
			"model":"anthropic/claude-sonnet-4.5",
			"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("kilo-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "anthropic/claude-sonnet-4.5",
		Messages: []core.Message{{Role: "user", Content: "Use the tool"}},
		Tools: []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":       "lookup",
				"parameters": map[string]any{"type": "object"},
			},
		}},
		ToolChoice: "auto",
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer kilo-key" {
		t.Fatalf("authorization = %q, want Bearer kilo-key", gotAuth)
	}
	if gotBody["model"] != "anthropic/claude-sonnet-4.5" {
		t.Fatalf("request model = %#v, want slash-delimited model unchanged", gotBody["model"])
	}
	if gotBody["tool_choice"] != "auto" {
		t.Fatalf("tool_choice = %#v, want auto", gotBody["tool_choice"])
	}
	tools, ok := gotBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v, want one tool", gotBody["tools"])
	}
	if resp.Model != "anthropic/claude-sonnet-4.5" || len(resp.Choices) != 1 || len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestStreamChatCompletion_UsesSSEAndPreservesStreamOptions(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			http.Error(w, "decode error", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-kilo\",\"object\":\"chat.completion.chunk\",\"choices\":[]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewWithHTTPClient("kilo-key", server.URL, server.Client(), llmclient.Hooks{})
	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:         "openai/gpt-5.5",
		Messages:      []core.Message{{Role: "user", Content: "hi"}},
		StreamOptions: &core.StreamOptions{IncludeUsage: true},
	})
	if err != nil {
		t.Fatalf("StreamChatCompletion() error = %v", err)
	}
	defer stream.Close()
	body, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if gotPath != "/chat/completions" || gotAuth != "Bearer kilo-key" {
		t.Fatalf("request path/auth = %q/%q", gotPath, gotAuth)
	}
	if gotBody["model"] != "openai/gpt-5.5" || gotBody["stream"] != true {
		t.Fatalf("stream request body = %#v", gotBody)
	}
	streamOptions, ok := gotBody["stream_options"].(map[string]any)
	if !ok || streamOptions["include_usage"] != true {
		t.Fatalf("stream_options = %#v, want include_usage=true", gotBody["stream_options"])
	}
	if !strings.Contains(string(body), "data: [DONE]") {
		t.Fatalf("stream body = %q, want SSE terminator", body)
	}
}

func TestListModels_PreservesProviderQualifiedIDs(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"google/gemini-3.1-pro","object":"model","owned_by":"google"}]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("kilo-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if gotPath != "/models" {
		t.Fatalf("path = %q, want /models", gotPath)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "google/gemini-3.1-pro" {
		t.Fatalf("models = %+v, want provider-qualified Kilo model", resp.Data)
	}
}

func TestEmbeddings_ReturnsUnsupportedError(t *testing.T) {
	provider := NewWithHTTPClient("kilo-key", "", nil, llmclient.Hooks{})
	_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{Model: "any"})
	if err == nil || !strings.Contains(err.Error(), "kilo does not support embeddings") {
		t.Fatalf("Embeddings() error = %v, want unsupported error", err)
	}
}

func TestProvider_DoesNotExposeOptionalOpenAICompatibleInterfaces(t *testing.T) {
	provider := NewWithHTTPClient("kilo-key", "", nil, llmclient.Hooks{})

	if _, ok := any(provider).(core.NativeBatchProvider); ok {
		t.Fatal("kilo provider should not implement native batch provider")
	}
	if _, ok := any(provider).(core.NativeFileProvider); ok {
		t.Fatal("kilo provider should not implement native file provider")
	}
	if _, ok := any(provider).(core.AudioProvider); ok {
		t.Fatal("kilo provider should not implement audio provider")
	}
}
