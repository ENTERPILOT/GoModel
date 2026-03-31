package embedding

import (
	"context"
	"testing"

	"gomodel/config"
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
	if a.apiKey != "gsk-abc" {
		t.Errorf("expected apiKey gsk-abc, got %q", a.apiKey)
	}
	if want := "https://api.groq.com/openai/v1/embeddings"; a.endpointURL != want {
		t.Errorf("endpointURL = %q, want %q", a.endpointURL, want)
	}
}

// MockEmbedder is an Embedder implementation for testing that returns a fixed vector.
type MockEmbedder struct {
	Vector []float32
	Err    error
	Calls  int
}

func (m *MockEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	m.Calls++
	return m.Vector, m.Err
}

func (m *MockEmbedder) Close() error { return nil }
