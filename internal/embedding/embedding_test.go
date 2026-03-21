package embedding

import (
	"context"
	"testing"

	"gomodel/config"
)

func TestNewEmbedder_LocalDefault(t *testing.T) {
	_, err := NewEmbedder(config.EmbedderConfig{Provider: "local"}, nil)
	if err == nil {
		t.Skip("ONNX Runtime not installed; local embedder test skipped")
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
	if _, ok := emb.(*apiEmbedder); !ok {
		t.Fatalf("expected *apiEmbedder, got %T", emb)
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
	if a.baseURL != "https://api.groq.com/openai" {
		t.Errorf("expected baseURL from provider config, got %q", a.baseURL)
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
