package chutes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

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

func TestListModels_PreservesChutesMetadata(t *testing.T) {
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object":"list",
			"data":[{
				"id":"Qwen/Qwen3.5-397B-A17B-TEE",
				"owned_by":"sglang",
				"created":1677652288,
				"context_length":262144,
				"max_output_length":65536,
				"input_modalities":["text","image"],
				"supported_features":["json_mode","tools","structured_outputs","reasoning"],
				"confidential_compute":true,
				"pricing":{"prompt":0.45,"completion":3.0,"input_cache_read":0.045}
			}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("cpk_test", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if gotPath != "/models" || gotAuth != "Bearer cpk_test" {
		t.Fatalf("request path/auth = %q/%q, want /models/Bearer cpk_test", gotPath, gotAuth)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("len(resp.Data) = %d, want 1", len(resp.Data))
	}
	model := resp.Data[0]
	if model.Object != "model" {
		t.Fatalf("model.Object = %q, want model", model.Object)
	}
	if model.Metadata == nil || model.Metadata.ContextWindow == nil || *model.Metadata.ContextWindow != 262144 {
		t.Fatalf("model context metadata = %+v, want 262144", model.Metadata)
	}
	if model.Metadata.MaxOutputTokens == nil || *model.Metadata.MaxOutputTokens != 65536 {
		t.Fatalf("max output tokens = %+v, want 65536", model.Metadata.MaxOutputTokens)
	}
	if !model.Metadata.Capabilities["tools"] || !model.Metadata.Capabilities["vision"] || !model.Metadata.Capabilities["confidential_compute"] {
		t.Fatalf("capabilities = %v, want tools, vision, and confidential_compute", model.Metadata.Capabilities)
	}
	pricing := model.Metadata.Pricing
	if pricing == nil || pricing.Currency != "USD" || pricing.InputPerMtok == nil || *pricing.InputPerMtok != 0.45 ||
		pricing.OutputPerMtok == nil || *pricing.OutputPerMtok != 3.0 ||
		pricing.CachedInputPerMtok == nil || *pricing.CachedInputPerMtok != 0.045 {
		t.Fatalf("pricing = %+v, want Chutes per-MTok USD pricing", pricing)
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
