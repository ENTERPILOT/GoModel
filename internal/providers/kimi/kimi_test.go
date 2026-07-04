package kimi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
)

func TestNew_DefaultBaseURL(t *testing.T) {
	// Kimi.New routes through the provider factory wiring. Without a configured
	// BaseURL, the resolver must fall back to the package-level default.
	provider := New(providers.ProviderConfig{
		Type:  "kimi",
		APIKey: "kimi-key",
	}, providers.ProviderOptions{})
	if provider == nil {
		t.Fatal("New() returned nil provider")
	}

	kp, ok := provider.(*Provider)
	if !ok {
		t.Fatalf("New() returned %T, want *Provider", provider)
	}
	if got := kp.GetBaseURL(); got != defaultBaseURL {
		t.Fatalf("GetBaseURL() = %q, want %q", got, defaultBaseURL)
	}
}

func TestNew_CustomBaseURL(t *testing.T) {
	// NewWithHTTPClient honors an explicit BaseURL via the resolver.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-custom",
			"created":1677652288,
			"model":"kimi-k2",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("kimi-key", server.URL, server.Client(), llmclient.Hooks{}, nil, "")
	if got := provider.GetBaseURL(); got != server.URL {
		t.Fatalf("GetBaseURL() = %q, want %q", got, server.URL)
	}

	// Sanity-check: requests actually flow to the custom URL.
	if _, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "kimi-k2",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
}

func TestNewWithHTTPClient_BearerAuth(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-bearer",
			"created":1677652288,
			"model":"kimi-k2",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("kimi-secret", server.URL, server.Client(), llmclient.Hooks{}, nil, "")

	if _, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "kimi-k2",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}

	if gotAuth != "Bearer kimi-secret" {
		t.Fatalf("authorization = %q, want %q", gotAuth, "Bearer kimi-secret")
	}
}

func TestProvider_SatisfiesInterface(t *testing.T) {
	// Compile-time check (mirrors the package-level assertion in kimi.go).
	var _ core.Provider = (*Provider)(nil)

	// Runtime check: New returns something that satisfies core.Provider.
	provider := New(providers.ProviderConfig{
		Type:   "kimi",
		APIKey: "kimi-key",
	}, providers.ProviderOptions{})
	if provider == nil {
		t.Fatal("New() returned nil")
	}

	// Kimi does not expose optional native batch or file provider surfaces.
	if _, ok := any(provider).(core.NativeBatchProvider); ok {
		t.Fatal("kimi provider should not implement NativeBatchProvider")
	}
	if _, ok := any(provider).(core.NativeFileProvider); ok {
		t.Fatal("kimi provider should not implement NativeFileProvider")
	}
}

func TestEmbeddings_DelegatesToCompatibleProvider(t *testing.T) {
	// Kimi has no provider-specific Embeddings override, so calls delegate
	// through the embedded openai.ChatCompatible adapter to /embeddings.
	var gotPath string
	var gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object":"list",
			"model":"kimi-embedding",
			"data":[{"object":"embedding","embedding":[0.1,0.2,0.3],"index":0}],
			"usage":{"prompt_tokens":2,"total_tokens":2}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("kimi-key", server.URL, server.Client(), llmclient.Hooks{}, nil, "")

	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "kimi-embedding",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embeddings() error = %v", err)
	}
	if resp.Model != "kimi-embedding" {
		t.Fatalf("resp.Model = %q, want kimi-embedding", resp.Model)
	}
	if gotPath != "/embeddings" {
		t.Fatalf("path = %q, want /embeddings", gotPath)
	}
	if gotAuth != "Bearer kimi-key" {
		t.Fatalf("authorization = %q, want Bearer kimi-key", gotAuth)
	}
}