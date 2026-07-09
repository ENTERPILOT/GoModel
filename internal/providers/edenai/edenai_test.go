package edenai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
)

func TestRegistration(t *testing.T) {
	if Registration.Type != "edenai" {
		t.Fatalf("Registration.Type = %q, want edenai", Registration.Type)
	}
	if Registration.Discovery.DefaultBaseURL != defaultBaseURL {
		t.Fatalf("DefaultBaseURL = %q, want %q", Registration.Discovery.DefaultBaseURL, defaultBaseURL)
	}
	if p := New(providers.ProviderConfig{APIKey: "eden-key"}, providers.ProviderOptions{}); p == nil {
		t.Fatal("New() returned nil")
	}
}

func TestChatCompletion_UsesBearerAuthAndChatEndpoint(t *testing.T) {
	var gotPath string
	var gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-edenai",
			"created":1677652288,
			"model":"anthropic/claude-sonnet-4-5",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("eden-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "anthropic/claude-sonnet-4-5",
		Messages: []core.Message{
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if resp.Model != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("resp.Model = %q, want anthropic/claude-sonnet-4-5", resp.Model)
	}
	if resp.Usage.TotalTokens != 4 {
		t.Fatalf("resp.Usage = %+v, want total_tokens=4", resp.Usage)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer eden-key" {
		t.Fatalf("Authorization = %q, want Bearer eden-key", gotAuth)
	}
}
