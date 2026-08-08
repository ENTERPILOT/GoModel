package chutes

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

func TestNew_ConstructsRegisteredProvider(t *testing.T) {
	provider, ok := New(providers.ProviderConfig{
		APIKey:  "cpk_test",
		BaseURL: "https://chutes.example/v1",
	}, providers.ProviderOptions{}).(*Provider)
	if !ok || provider.compat == nil {
		t.Fatalf("New() = %T, want initialized *Provider", provider)
	}
	if Registration.Discovery.DefaultBaseURL != defaultBaseURL {
		t.Fatalf("registration base URL = %q, want %q", Registration.Discovery.DefaultBaseURL, defaultBaseURL)
	}
}

func TestSetBaseURL_ChangesRequestTarget(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("cpk_test", "https://unused.example/v1", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)
	if _, err := provider.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if gotPath != "/models" {
		t.Fatalf("path = %q, want /models", gotPath)
	}
}

func TestChatCompletion_UsesBearerAuthAndChatEndpoint(t *testing.T) {
	var gotPath, gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-chutes",
			"created":1677652288,
			"model":"Qwen/Qwen3-32B-TEE",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("cpk_test", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "Qwen/Qwen3-32B-TEE",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer cpk_test" {
		t.Fatalf("authorization = %q, want Bearer cpk_test", gotAuth)
	}
	if resp.Model != "Qwen/Qwen3-32B-TEE" || resp.Usage.TotalTokens != 4 {
		t.Fatalf("response = %+v, want model and usage preserved", resp)
	}
}

func TestStreamChatCompletion_UsesBearerAuthAndChatEndpoint(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			http.Error(w, "decode error", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-chutes\",\"object\":\"chat.completion.chunk\",\"choices\":[]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewWithHTTPClient("cpk_test", server.URL, server.Client(), llmclient.Hooks{})
	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "Qwen/Qwen3-32B-TEE",
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
	if gotPath != "/chat/completions" || gotAuth != "Bearer cpk_test" {
		t.Fatalf("request path/auth = %q/%q, want /chat/completions/Bearer cpk_test", gotPath, gotAuth)
	}
	if gotBody["model"] != "Qwen/Qwen3-32B-TEE" || gotBody["stream"] != true {
		t.Fatalf("stream request body = %#v", gotBody)
	}
	if !strings.Contains(string(body), "data: [DONE]") {
		t.Fatalf("stream body = %q, want SSE terminator", body)
	}
}

func TestChatCompletion_ReturnsUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("cpk_test", server.URL, server.Client(), llmclient.Hooks{})
	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "Qwen/Qwen3-32B-TEE",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("ChatCompletion() error = nil, want upstream error")
	}
	gatewayErr, ok := err.(*core.GatewayError)
	if !ok {
		t.Fatalf("error type = %T, want *core.GatewayError", err)
	}
	if gatewayErr.StatusCode != http.StatusTooManyRequests || gatewayErr.Type != core.ErrorTypeRateLimit {
		t.Fatalf("gateway error = %+v, want 429 rate_limit_error", gatewayErr)
	}
}

func TestPassthrough_ForwardsOpaqueRequest(t *testing.T) {
	var gotURI, gotAuth, gotBeta, gotBody string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.URL.RequestURI()
		gotAuth = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("X-Chutes-Beta")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("cpk_test", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "chat/completions?trace=true",
		Body:     io.NopCloser(strings.NewReader(`{"model":"Qwen/Qwen3-32B-TEE"}`)),
		Headers: http.Header{
			"Content-Type":  {"application/json"},
			"X-Chutes-Beta": {"test"},
		},
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if gotURI != "/chat/completions?trace=true" || gotAuth != "Bearer cpk_test" {
		t.Fatalf("request URI/auth = %q/%q", gotURI, gotAuth)
	}
	if gotBeta != "test" || gotBody != `{"model":"Qwen/Qwen3-32B-TEE"}` {
		t.Fatalf("request beta/body = %q/%q", gotBeta, gotBody)
	}
	if resp.StatusCode != http.StatusAccepted || string(responseBody) != `{"accepted":true}` {
		t.Fatalf("response status/body = %d/%q", resp.StatusCode, responseBody)
	}
}

func TestResponses_TranslatesToChatCompletions(t *testing.T) {
	var gotPath string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			http.Error(w, "decode error", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-chutes",
			"created":1677652288,
			"model":"Qwen/Qwen3-32B-TEE",
			"choices":[{"index":0,"message":{"role":"assistant","content":"translated"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("cpk_test", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "Qwen/Qwen3-32B-TEE",
		Input: "hi",
	})
	if err != nil {
		t.Fatalf("Responses() error = %v", err)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", gotPath)
	}
	if gotBody["model"] != "Qwen/Qwen3-32B-TEE" {
		t.Fatalf("request model = %#v, want Qwen/Qwen3-32B-TEE", gotBody["model"])
	}
	if resp.Object != "response" || resp.Status != "completed" {
		t.Fatalf("response metadata = object %q status %q, want response/completed", resp.Object, resp.Status)
	}
}

func TestStreamResponses_TranslatesToChatCompletions(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			http.Error(w, "decode error", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-chutes\",\"object\":\"chat.completion.chunk\",\"created\":1677652288,\"model\":\"Qwen/Qwen3-32B-TEE\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewWithHTTPClient("cpk_test", server.URL, server.Client(), llmclient.Hooks{})
	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "Qwen/Qwen3-32B-TEE",
		Input: "hi",
	})
	if err != nil {
		t.Fatalf("StreamResponses() error = %v", err)
	}
	defer stream.Close()

	body, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if gotPath != "/chat/completions" || gotAuth != "Bearer cpk_test" {
		t.Fatalf("request path/auth = %q/%q", gotPath, gotAuth)
	}
	if gotBody["model"] != "Qwen/Qwen3-32B-TEE" || gotBody["stream"] != true {
		t.Fatalf("stream request body = %#v", gotBody)
	}
	if raw := string(body); !strings.Contains(raw, "response.output_text.delta") || !strings.Contains(raw, "data: [DONE]") {
		t.Fatalf("converted stream missing Responses events or done marker: %s", raw)
	}
}

func TestEmbeddings_ReturnsUnsupportedError(t *testing.T) {
	provider := NewWithHTTPClient("cpk_test", "", nil, llmclient.Hooks{})
	if _, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{}); err == nil {
		t.Fatal("Embeddings() error = nil, want unsupported error")
	}
}

func TestProvider_DoesNotExposeUnsupportedOptionalInterfaces(t *testing.T) {
	provider := NewWithHTTPClient("cpk_test", "", nil, llmclient.Hooks{})

	if _, ok := any(provider).(core.NativeBatchProvider); ok {
		t.Fatal("chutes provider should not implement native batch provider")
	}
	if _, ok := any(provider).(core.NativeFileProvider); ok {
		t.Fatal("chutes provider should not implement native file provider")
	}
	if _, ok := any(provider).(core.NativeResponseLifecycleProvider); ok {
		t.Fatal("chutes provider should not implement native response lifecycle provider")
	}
	if _, ok := any(provider).(core.AudioProvider); ok {
		t.Fatal("chutes provider should not implement audio provider")
	}
}
