package kimicode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

// kimibgeM3EmbedModel is the model ID used by these tests for the Kimi Code /embeddings endpoint.
//
// NOTE: "bge_m3_embed" is not part of Kimi Code's documented public model catalogue. It is
// retained here because the Kimi Code provider package currently forwards embedding model IDs
// unchanged through the OpenAI-compatible adapter. Because the model is undocumented upstream,
// its name, behaviour, or availability may change without notice; if Kimi Code rotates the ID
// these tests (and the provider's embedding round-trip) will need to be updated.
const kimibgeM3EmbedModel = "bge_m3_embed"

func TestNew_ReturnsProvider(t *testing.T) {
	provider := New(providers.ProviderConfig{APIKey: "test-api-key"}, providers.ProviderOptions{})

	if provider == nil {
		t.Fatal("provider should not be nil")
	}

	concrete, ok := provider.(*Provider)
	if !ok {
		t.Fatalf("New() returned %T, want *kimicode.Provider", provider)
	}
	if concrete.ChatCompatible == nil {
		t.Error("embedded ChatCompatible should not be nil")
	}
}

func TestNewWithHTTPClient_ReturnsProvider(t *testing.T) {
	provider := NewWithHTTPClient("test-api-key", "http://example.invalid", &http.Client{}, llmclient.Hooks{})

	if provider == nil {
		t.Fatal("provider should not be nil")
	}
	if provider.ChatCompatible == nil {
		t.Error("embedded ChatCompatible should not be nil")
	}
}

func TestRegistration_TypeIsKimicode(t *testing.T) {
	if Registration.Type != "kimicode" {
		t.Errorf("Registration.Type = %q, want %q", Registration.Type, "kimicode")
	}
	if Registration.New == nil {
		t.Error("Registration.New should not be nil")
	}
	if Registration.Discovery.DefaultBaseURL == "" {
		t.Error("Registration.Discovery.DefaultBaseURL should not be empty")
	}
}

func TestEmbeddings_RoundTrip(t *testing.T) {
	var gotPath string
	var gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object": "list",
			"model": "bge_m3_embed",
			"data": [
				{"object": "embedding", "embedding": [0.1, 0.2, 0.3], "index": 0}
			],
			"usage": {"prompt_tokens": 3, "total_tokens": 3}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("kimi-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: kimibgeM3EmbedModel,
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embeddings() error = %v", err)
	}
	if resp == nil {
		t.Fatal("Embeddings() response should not be nil")
	}
	if resp.Model != kimibgeM3EmbedModel {
		t.Errorf("resp.Model = %q, want %q", resp.Model, kimibgeM3EmbedModel)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("len(resp.Data) = %d, want 1", len(resp.Data))
	}
	if gotPath != "/embeddings" {
		t.Errorf("path = %q, want /embeddings", gotPath)
	}
	if gotAuth != "Bearer kimi-key" {
		t.Errorf("authorization = %q, want %q", gotAuth, "Bearer kimi-key")
	}
}
