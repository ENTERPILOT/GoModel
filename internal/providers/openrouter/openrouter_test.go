package openrouter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
)

func TestChatCompletion_AddsDefaultAttributionHeaders(t *testing.T) {
	var gotReferer string
	var gotTitle string
	var gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReferer = r.Header.Get("HTTP-Referer")
		gotTitle = r.Header.Get("X-OpenRouter-Title")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-123",
			"object":"chat.completion",
			"created":1677652288,
			"model":"openai/gpt-4o-mini",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "openai/gpt-4o-mini",
		Messages: []core.Message{
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAuth != "Bearer test-api-key" {
		t.Fatalf("authorization = %q, want Bearer test-api-key", gotAuth)
	}
	if gotReferer != defaultSiteURL {
		t.Fatalf("HTTP-Referer = %q, want %q", gotReferer, defaultSiteURL)
	}
	if gotTitle != defaultAppName {
		t.Fatalf("X-OpenRouter-Title = %q, want %q", gotTitle, defaultAppName)
	}
}

func TestChatCompletion_UsesEnvOverridesForAttributionHeaders(t *testing.T) {
	t.Setenv("OPENROUTER_SITE_URL", "https://example.com")
	t.Setenv("OPENROUTER_APP_NAME", "Example App")

	var gotReferer string
	var gotTitle string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReferer = r.Header.Get("HTTP-Referer")
		gotTitle = r.Header.Get("X-OpenRouter-Title")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-123",
			"object":"chat.completion",
			"created":1677652288,
			"model":"openai/gpt-4o-mini",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "openai/gpt-4o-mini",
		Messages: []core.Message{
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotReferer != "https://example.com" {
		t.Fatalf("HTTP-Referer = %q, want https://example.com", gotReferer)
	}
	if gotTitle != "Example App" {
		t.Fatalf("X-OpenRouter-Title = %q, want Example App", gotTitle)
	}
}

func TestPassthrough_PreservesUserProvidedAttributionHeaders(t *testing.T) {
	var gotReferer string
	var gotTitle string
	var gotLegacyTitle string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReferer = r.Header.Get("HTTP-Referer")
		gotTitle = r.Header.Get("X-OpenRouter-Title")
		gotLegacyTitle = r.Header.Get("X-Title")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "responses",
		Body:     io.NopCloser(strings.NewReader(`{"model":"openai/gpt-4o-mini"}`)),
		Headers: http.Header{
			"Content-Type": {"application/json"},
			"HTTP-Referer": {"https://caller.example"},
			"X-Title":      {"Caller App"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if gotReferer != "https://caller.example" {
		t.Fatalf("HTTP-Referer = %q, want https://caller.example", gotReferer)
	}
	if gotLegacyTitle != "Caller App" {
		t.Fatalf("X-Title = %q, want Caller App", gotLegacyTitle)
	}
	if gotTitle != "" {
		t.Fatalf("X-OpenRouter-Title = %q, want empty when caller provided X-Title", gotTitle)
	}
}
