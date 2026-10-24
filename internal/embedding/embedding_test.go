package embedding

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewEmbedder_EmptyProvider(t *testing.T) {
	_, err := NewEmbedder(config.EmbedderConfig{}, map[string]config.RawProviderConfig{})
	require.Error(t, err)
}

func TestNewEmbedder_LocalRejected(t *testing.T) {
	_, err := NewEmbedder(config.EmbedderConfig{Provider: "local"}, map[string]config.RawProviderConfig{"local": {}})
	require.Error(t, err)
}

func TestNewEmbedder_UnknownProvider(t *testing.T) {
	_, err := NewEmbedder(config.EmbedderConfig{Provider: "nonexistent-provider"}, map[string]config.RawProviderConfig{})
	require.Error(t, err)
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
	require.NoError(t, err)

	defer emb.Close()
	a, ok := emb.(*apiEmbedder)
	require.True(t, ok, "expected *apiEmbedder, got %T", emb)
	require.Equal(t, "https://api.openai.com/v1/embeddings", a.endpointURL)
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
			require.NoError(t, err)

			defer emb.Close()

			ctx := core.WithSessionID(context.Background(), "same-session")
			for range 3 {
				_, err := emb.Embed(ctx, "hello")
				require.NoError(t, err)
			}
			got := []string{<-seen, <-seen, <-seen}
			if tt.wantSticky {
				require.NotEmpty(t, got[0])
				require.Equal(t, got[0], got[1])
				require.Equal(t, got[0], got[2], "authorization sequence = %v, want one sticky key", got)

				return
			}
			want := []string{"Bearer key-1", "Bearer key-2", "Bearer key-1"}
			for i := range want {
				require.Equal(t, want[i], got[i], "authorization sequence = %v, want %v", got, want)
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
	require.NoError(t, err)

	defer emb.Close()
	a, ok := emb.(*apiEmbedder)
	require.True(t, ok, "expected *apiEmbedder, got %T", emb)

	wantURL := geminiOpenAICompat + "/v1/embeddings"
	require.Equal(t, wantURL, a.endpointURL)
	require.Equal(t, "gemini-embedding-001", a.model)
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
	require.NoError(t, err)

	defer emb.Close()
	a := emb.(*apiEmbedder)
	require.Equal(t, "gemini-embedding-001", a.model)
}

func TestOpenAIEmbeddingsEndpointURL_BaseURLTrimAndJoin(t *testing.T) {
	got, err := openAIEmbeddingsEndpointURL("https://example.com/custom/")
	require.NoError(t, err)
	want := "https://example.com/custom/v1/embeddings"
	require.Equal(t, want, got)

	got2, err := openAIEmbeddingsEndpointURL("https://api.openai.com/v1")
	require.NoError(t, err)
	want2 := "https://api.openai.com/v1/embeddings"
	require.Equal(t, want2, got2)
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
	require.NoError(t, err)

	a, ok := emb.(*apiEmbedder)
	require.True(t, ok, "expected *apiEmbedder, got %T", emb)
	got := a.keys.Primary()
	assert.Equal(t, "gsk-abc", got)
	want := "https://api.groq.com/openai/v1/embeddings"
	assert.Equal(t, want, a.endpointURL)
}
