package embedding

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

func TestNewEmbedder_EmptyProvider(t *testing.T) {
	_, err := NewEmbedder(config.EmbedderConfig{}, map[string]config.RawProviderConfig{})
	if err == nil {
		t.Fatal("expected error for empty provider")
	}
}

func TestNewEmbedder_LocalRejected(t *testing.T) {
	_, err := NewEmbedder(config.EmbedderConfig{Provider: "local"}, map[string]config.RawProviderConfig{"local": {}})
	if err == nil {
		t.Fatal("expected error for local provider")
	}
}

func TestNewEmbedder_UnknownProvider(t *testing.T) {
	_, err := NewEmbedder(config.EmbedderConfig{Provider: "nonexistent-provider"}, map[string]config.RawProviderConfig{})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestNewEmbedder_APIEmbedder(t *testing.T) {
	rawProviders := map[string]config.RawProviderConfig{
		"openai": {
			Type:    "openai",
			APIKey:  "sk-test",
			BaseURL: "https://api.openai.com",
		},
	}
	emb, err := NewEmbedder(config.EmbedderConfig{
		Provider: "openai",
		Model:    "text-embedding-3-small",
	}, rawProviders)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	defer emb.Close()
	a, ok := emb.(*apiEmbedder)
	if !ok {
		t.Fatalf("expected *apiEmbedder, got %T", emb)
	}
	if a.endpointURL != "https://api.openai.com/v1/embeddings" {
		t.Fatalf("endpointURL = %q", a.endpointURL)
	}
}

func TestAPIEmbedder_SessionStickyKeys(t *testing.T) {
	disabled := false
	tests := []struct {
		name          string
		stickySetting *bool
		wantSticky    bool
	}{
		{name: "enabled by default", wantSticky: true},
		{name: "explicitly disabled", stickySetting: &disabled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen := make(chan string, 3)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen <- r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[{"embedding":[0.25]}]}`))
			}))
			defer server.Close()

			emb, err := NewEmbedder(config.EmbedderConfig{Provider: "openai"}, map[string]config.RawProviderConfig{
				"openai": {
					Type:              "openai",
					APIKeys:           []string{"key-1", "key-2"},
					BaseURL:           server.URL,
					SessionStickyKeys: tt.stickySetting,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer emb.Close()

			ctx := core.WithSessionID(context.Background(), "same-session")
			for range 3 {
				if _, err := emb.Embed(ctx, "hello"); err != nil {
					t.Fatal(err)
				}
			}
			got := []string{<-seen, <-seen, <-seen}
			if tt.wantSticky {
				if got[0] == "" || got[1] != got[0] || got[2] != got[0] {
					t.Fatalf("authorization sequence = %v, want one sticky key", got)
				}
				return
			}
			want := []string{"Bearer key-1", "Bearer key-2", "Bearer key-1"}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("authorization sequence = %v, want %v", got, want)
				}
			}
		})
	}
}

func TestNewEmbedder_GeminiUsesProviderBaseURL(t *testing.T) {
	const geminiOpenAICompat = "https://generativelanguage.googleapis.com/v1beta/openai"
	rawProviders := map[string]config.RawProviderConfig{
		"gemini": {
			Type:    "gemini",
			APIKey:  "AIza-test",
			BaseURL: geminiOpenAICompat,
		},
	}
	emb, err := NewEmbedder(config.EmbedderConfig{
		Provider: "gemini",
		Model:    "text-embedding-004",
	}, rawProviders)
	if err != nil {
		t.Fatal(err)
	}
	defer emb.Close()
	a, ok := emb.(*apiEmbedder)
	if !ok {
		t.Fatalf("expected *apiEmbedder, got %T", emb)
	}
	wantURL := geminiOpenAICompat + "/v1/embeddings"
	if a.endpointURL != wantURL {
		t.Fatalf("endpointURL = %q, want %q", a.endpointURL, wantURL)
	}
	if a.model != "gemini-embedding-001" {
		t.Fatalf("model = %q, want gemini-embedding-001 (text-embedding-* is not valid on Gemini OpenAI compat)", a.model)
	}
}

func TestNewEmbedder_GeminiEmptyModelDefault(t *testing.T) {
	rawProviders := map[string]config.RawProviderConfig{
		"gemini": {
			Type:    "gemini",
			APIKey:  "k",
			BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai",
		},
	}
	emb, err := NewEmbedder(config.EmbedderConfig{Provider: "gemini", Model: ""}, rawProviders)
	if err != nil {
		t.Fatal(err)
	}
	defer emb.Close()
	a := emb.(*apiEmbedder)
	if a.model != "gemini-embedding-001" {
		t.Fatalf("model = %q", a.model)
	}
}

func TestOpenAIEmbeddingsEndpointURL_BaseURLTrimAndJoin(t *testing.T) {
	got, err := openAIEmbeddingsEndpointURL("https://example.com/custom/")
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://example.com/custom/v1/embeddings"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	got2, err := openAIEmbeddingsEndpointURL("https://api.openai.com/v1")
	if err != nil {
		t.Fatal(err)
	}
	if want2 := "https://api.openai.com/v1/embeddings"; got2 != want2 {
		t.Fatalf("got %q, want %q", got2, want2)
	}
}

func TestAPIEmbedder_UsesProviderCredentials(t *testing.T) {
	rawProviders := map[string]config.RawProviderConfig{
		"groq": {
			Type:    "groq",
			APIKey:  "gsk-abc",
			BaseURL: "https://api.groq.com/openai",
		},
	}
	emb, err := NewEmbedder(config.EmbedderConfig{
		Provider: "groq",
		Model:    "nomic-embed-text-v1_5",
	}, rawProviders)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	a, ok := emb.(*apiEmbedder)
	if !ok {
		t.Fatalf("expected *apiEmbedder, got %T", emb)
	}
	if got := a.keys.Primary(); got != "gsk-abc" {
		t.Errorf("expected primary key gsk-abc, got %q", got)
	}
	if want := "https://api.groq.com/openai/v1/embeddings"; a.endpointURL != want {
		t.Errorf("endpointURL = %q, want %q", a.endpointURL, want)
	}
}
