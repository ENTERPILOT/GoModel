package openrouter

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// ListModels must keep OpenRouter's architecture modalities and context
// length, mapping output modalities onto gateway modes so the catalog's long
// tail is categorized without remote-registry entries.
func TestListModels_StampsArchitectureModalities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("Path = %q, want /models", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[
			{"id":"openai/gpt-4o-mini","created":1721260800,"context_length":128000,
			 "architecture":{"input_modalities":["text","image"],"output_modalities":["text"]}},
			{"id":"google/gemini-3-pro-image","created":1721260800,
			 "architecture":{"input_modalities":["text"],"output_modalities":["image"]}},
			{"id":"mystery/no-architecture","created":1721260800}
		]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Data) != 3 {
		t.Fatalf("len(Data) = %d, want 3", len(resp.Data))
	}
	byID := map[string]core.Model{}
	for _, m := range resp.Data {
		byID[m.ID] = m
	}

	chat := byID["openai/gpt-4o-mini"]
	if chat.Metadata == nil || len(chat.Metadata.Modes) != 1 || chat.Metadata.Modes[0] != "chat" {
		t.Errorf("gpt-4o-mini metadata = %+v, want chat modes", chat.Metadata)
	}
	if chat.Metadata == nil || chat.Metadata.ContextWindow == nil || *chat.Metadata.ContextWindow != 128000 {
		t.Errorf("gpt-4o-mini context window = %+v, want 128000", chat.Metadata)
	}
	image := byID["google/gemini-3-pro-image"]
	if image.Metadata == nil || len(image.Metadata.Modes) != 1 || image.Metadata.Modes[0] != "image_generation" {
		t.Errorf("image model metadata = %+v, want image_generation modes", image.Metadata)
	}
	if image.Metadata == nil || len(image.Metadata.Categories) != 1 || image.Metadata.Categories[0] != core.CategoryImage {
		t.Errorf("image model categories = %+v, want [image]", image.Metadata)
	}
	if byID["mystery/no-architecture"].Metadata != nil {
		t.Errorf("no-architecture metadata = %+v, want nil", byID["mystery/no-architecture"].Metadata)
	}
}

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

func TestChatCompletion_ForwardsGoModelSessionID(t *testing.T) {
	gotSessionID := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSessionID <- r.Header.Get("X-Session-Id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-123","object":"chat.completion","created":1677652288,
			"model":"openai/gpt-4o-mini",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)
	ctx := core.WithSessionID(context.Background(), "conversation-42")
	_, err := provider.ChatCompletion(ctx, &core.ChatRequest{
		Model:    "openai/gpt-4o-mini",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if got := <-gotSessionID; got != "conversation-42" {
		t.Fatalf("X-Session-Id = %q, want conversation-42", got)
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
