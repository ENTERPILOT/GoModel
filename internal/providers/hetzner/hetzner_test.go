package hetzner

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

// TestNew_ReturnsProvider asserts that New returns a non-nil *Provider whose embedded
// ChatCompatible is non-nil. Matches kimicode's surface.
func TestNew_ReturnsProvider(t *testing.T) {
	provider := New(providers.ProviderConfig{APIKey: "test-api-key"}, providers.ProviderOptions{})

	if provider == nil {
		t.Fatal("provider should not be nil")
	}

	concrete, ok := provider.(*Provider)
	if !ok {
		t.Fatalf("New() returned %T, want *hetzner.Provider", provider)
	}
	if concrete.ChatCompatible == nil {
		t.Error("embedded ChatCompatible should not be nil")
	}
}

// TestNewWithHTTPClient_ReturnsProvider asserts the explicit HTTP-client constructor
// returns a valid Provider with a non-nil ChatCompatible.
func TestNewWithHTTPClient_ReturnsProvider(t *testing.T) {
	provider := NewWithHTTPClient("test-api-key", "http://example.invalid", &http.Client{}, llmclient.Hooks{})

	if provider == nil {
		t.Fatal("provider should not be nil")
	}
	if provider.ChatCompatible == nil {
		t.Error("embedded ChatCompatible should not be nil")
	}
}

// TestNewWithHTTPClient_NilHTTPClientDoesNotPanic asserts that passing nil for the
// HTTP client falls back to http.DefaultClient without panicking.
func TestNewWithHTTPClient_NilHTTPClientDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewWithHTTPClient(nil, ...) panicked: %v", r)
		}
	}()
	provider := NewWithHTTPClient("test-api-key", "http://example.invalid", nil, llmclient.Hooks{})
	if provider == nil {
		t.Fatal("provider should not be nil")
	}
}

// TestNewWithHTTPClient_ZeroHooksDoesNotPanic asserts that the hooks argument can be
// an empty struct (no hooks registered) without panicking.
func TestNewWithHTTPClient_ZeroHooksDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewWithHTTPClient(..., llmclient.Hooks{}) panicked: %v", r)
		}
	}()
	provider := NewWithHTTPClient("test-api-key", "http://example.invalid", &http.Client{}, llmclient.Hooks{})
	if provider == nil {
		t.Fatal("provider should not be nil")
	}
}

// TestRegistration_TypeAndDiscovery asserts the Registration struct exposes the
// expected type, New function, and default base URL.
func TestRegistration_TypeAndDiscovery(t *testing.T) {
	if Registration.Type != "hetzner" {
		t.Errorf("Registration.Type = %q, want %q", Registration.Type, "hetzner")
	}
	if Registration.New == nil {
		t.Error("Registration.New should not be nil")
	}
	if Registration.Discovery.DefaultBaseURL == "" {
		t.Error("Registration.Discovery.DefaultBaseURL should not be empty")
	}
	want := "https://inference.hetzner.com/api/v1"
	if Registration.Discovery.DefaultBaseURL != want {
		t.Errorf("Registration.Discovery.DefaultBaseURL = %q, want %q", Registration.Discovery.DefaultBaseURL, want)
	}
}

// TestProvider_ImplementsCoreProvider is a compile-time check that *Provider
// satisfies the core.Provider interface used by the factory.
func TestProvider_ImplementsCoreProvider(t *testing.T) {
	var _ core.Provider = (*Provider)(nil)
}

// TestChatCompletion_UsesBearerAuthAndForwardsModel asserts that ChatCompletion
// posts to /chat/completions with the Bearer header and forwards the requested
// model unchanged.
func TestChatCompletion_UsesBearerAuthAndForwardsModel(t *testing.T) {
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
			"id":"chatcmpl-hetzner",
			"created":1677652288,
			"model":"Qwen/Qwen3.6-35B-A3B-FP8",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("hetzner-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "Qwen/Qwen3.6-35B-A3B-FP8",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer hetzner-key" {
		t.Fatalf("authorization = %q, want Bearer hetzner-key", gotAuth)
	}
	if gotBody["model"] != "Qwen/Qwen3.6-35B-A3B-FP8" {
		t.Fatalf("request model = %#v, want Qwen/Qwen3.6-35B-A3B-FP8", gotBody["model"])
	}
	if resp.Model != "Qwen/Qwen3.6-35B-A3B-FP8" {
		t.Fatalf("response model = %q, want Qwen/Qwen3.6-35B-A3B-FP8", resp.Model)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "hello" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

// TestStreamChatCompletion_UsesSSE asserts that streaming requests go to
// /chat/completions with the Bearer header, set stream=true, and return SSE data
// the adapter normalizes.
func TestStreamChatCompletion_UsesSSE(t *testing.T) {
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
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-hetzner\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewWithHTTPClient("hetzner-key", server.URL, server.Client(), llmclient.Hooks{})
	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "Qwen/Qwen3.6-35B-A3B-FP8",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("StreamChatCompletion() error = %v", err)
	}
	defer stream.Close()
	body, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer hetzner-key" {
		t.Fatalf("authorization = %q, want Bearer hetzner-key", gotAuth)
	}
	if gotBody["model"] != "Qwen/Qwen3.6-35B-A3B-FP8" || gotBody["stream"] != true {
		t.Fatalf("stream request body = %#v", gotBody)
	}
	if !strings.Contains(string(body), "data: [DONE]") {
		t.Fatalf("stream body = %q, want SSE terminator", body)
	}
}

// TestListModels_ForwardsToModelsEndpoint asserts that ListModels calls
// /v1/models and returns the parsed model list unchanged.
func TestListModels_ForwardsToModelsEndpoint(t *testing.T) {
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"Qwen/Qwen3.6-35B-A3B-FP8","object":"model","owned_by":"alibaba"}]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("hetzner-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if gotPath != "/models" {
		t.Fatalf("path = %q, want /models", gotPath)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "Qwen/Qwen3.6-35B-A3B-FP8" {
		t.Fatalf("models = %+v, want one hetzner model", resp.Data)
	}
}

// TestEmbeddings_ForwardsToUpstreamWhichReturnsError documents that the embedded
// ChatCompatible forwards embedding requests to /v1/embeddings even though Hetzner
// documents no embeddings endpoint. The call is expected to fail at the upstream;
// GoModel does not currently override Embeddings to return a clearer error.
func TestEmbeddings_ForwardsToUpstreamWhichReturnsError(t *testing.T) {
	var gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()

	provider := NewWithHTTPClient("hetzner-key", server.URL, server.Client(), llmclient.Hooks{})
	_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "anything",
		Input: "hi",
	})
	if err == nil {
		t.Fatal("Embeddings() error = nil, want error (Hetzner has no embeddings endpoint)")
	}
	if gotPath != "/embeddings" {
		t.Errorf("path = %q, want /embeddings (proves request was forwarded)", gotPath)
	}
}

// TestProvider_DoesNotExposeOptionalOpenAICompatibleInterfaces mirrors the kilo
// guard: hetzner wraps *ChatCompatible which does not satisfy the optional native
// interfaces. If Hetzner ever gains native batch/file/audio support, the test
// fails and the implementation must add explicit method overrides to remove
// capabilities it cannot honour upstream.
func TestProvider_DoesNotExposeOptionalOpenAICompatibleInterfaces(t *testing.T) {
	provider := NewWithHTTPClient("hetzner-key", "", nil, llmclient.Hooks{})

	if _, ok := any(provider).(core.NativeBatchProvider); ok {
		t.Fatal("hetzner provider should not implement native batch provider")
	}
	if _, ok := any(provider).(core.NativeFileProvider); ok {
		t.Fatal("hetzner provider should not implement native file provider")
	}
	if _, ok := any(provider).(core.AudioProvider); ok {
		t.Fatal("hetzner provider should not implement audio provider")
	}
}