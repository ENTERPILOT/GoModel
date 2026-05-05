package vertex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/providers"
	"gomodel/internal/providers/googleauth"

	"golang.org/x/oauth2"
)

func TestProviderDoesNotExposeFilesOrBatches(t *testing.T) {
	provider := newProvider(testConfig(), providers.ProviderOptions{}, authedTestClient(http.DefaultClient))

	if _, ok := any(provider).(core.NativeFileProvider); ok {
		t.Fatal("Vertex provider must not expose native files")
	}
	if _, ok := any(provider).(core.NativeBatchProvider); ok {
		t.Fatal("Vertex provider must not expose native batches")
	}
}

func TestEmbeddingsUsesNativePrediction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects/prod-ai/locations/us-central1/publishers/google/models/text-embedding-005:predict" {
			t.Errorf("Path = %q, want Vertex native predict endpoint", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer vertex-token" {
			t.Errorf("Authorization = %q, want Bearer vertex-token", got)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "" {
			t.Errorf("x-goog-api-key = %q, want empty for Vertex OAuth", got)
		}

		var payload vertexEmbeddingPredictRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if len(payload.Instances) != 2 {
			t.Fatalf("instances = %+v, want 2", payload.Instances)
		}
		if payload.Instances[0].Content != "hello" || payload.Instances[1].Content != "world" {
			t.Fatalf("instances = %+v, want hello/world", payload.Instances)
		}
		if got := payload.Parameters["outputDimensionality"]; got != float64(3) {
			t.Fatalf("outputDimensionality = %#v, want 3", got)
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"predictions": [
				{"embeddings": {"values": [0.1, 0.2, 0.3], "statistics": {"token_count": 4}}},
				{"embeddings": {"values": [0.4, 0.5, 0.6], "statistics": {"token_count": 5}}}
			]
		}`))
	}))
	defer server.Close()

	dimensions := 3
	cfg := testConfig()
	cfg.BaseURL = server.URL + "/v1/projects/prod-ai/locations/us-central1/publishers/google"
	provider := newProvider(cfg, providers.ProviderOptions{}, authedTestClient(server.Client()))

	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model:      "google/text-embedding-005",
		Input:      []string{"hello", "world"},
		Dimensions: &dimensions,
	})
	if err != nil {
		t.Fatalf("Embeddings() error = %v", err)
	}
	if resp.Provider != "vertex" {
		t.Fatalf("Provider = %q, want vertex", resp.Provider)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("data = %+v, want 2 embeddings", resp.Data)
	}
	if got := string(resp.Data[0].Embedding); got != `[0.1,0.2,0.3]` {
		t.Fatalf("embedding = %s, want [0.1,0.2,0.3]", got)
	}
	if resp.Usage.PromptTokens != 9 || resp.Usage.TotalTokens != 9 {
		t.Fatalf("usage = %+v, want 9 prompt/total tokens", resp.Usage)
	}
}

func TestEmbeddingsRejectsEmptyStringInBatch(t *testing.T) {
	provider := newProvider(testConfig(), providers.ProviderOptions{}, authedTestClient(http.DefaultClient))

	_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "google/text-embedding-005",
		Input: []string{"hello", "", "world"},
	})
	if err == nil {
		t.Fatal("expected empty batch embedding input to be rejected")
	}
	if !strings.Contains(err.Error(), "embedding input must not be empty") {
		t.Fatalf("error = %v, want empty input error", err)
	}
}

func TestNewAcceptsBaseURLWithoutProjectLocation(t *testing.T) {
	provider := newProvider(providers.ProviderConfig{
		Type:     "vertex",
		AuthType: "gcp_adc",
		BaseURL:  "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/publishers/google",
	}, providers.ProviderOptions{}, authedTestClient(http.DefaultClient))

	if err := provider.ready(); err != nil {
		t.Fatalf("ready() error = %v, want nil for custom Vertex base URL", err)
	}
}

func TestNewRejectsUnsupportedAuthType(t *testing.T) {
	provider := newProvider(providers.ProviderConfig{
		Type:     "vertex",
		AuthType: "api_key",
		BaseURL:  "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/publishers/google",
	}, providers.ProviderOptions{}, authedTestClient(http.DefaultClient))

	err := provider.ready()
	if err == nil {
		t.Fatal("expected unsupported auth type error")
	}
	if !strings.Contains(err.Error(), `unsupported vertex AI auth type "api_key"`) {
		t.Fatalf("error = %v, want unsupported auth type", err)
	}
}

func TestVertexBaseURLs(t *testing.T) {
	tests := []struct {
		name       string
		cfg        providers.ProviderConfig
		wantCompat string
		wantNative string
	}{
		{
			name: "derives official vertex bases from project and location",
			cfg: providers.ProviderConfig{
				VertexProject:  "prod-ai",
				VertexLocation: "us-central1",
			},
			wantCompat: "https://aiplatform.googleapis.com/v1/projects/prod-ai/locations/us-central1/endpoints/openapi",
			wantNative: "https://aiplatform.googleapis.com/v1/projects/prod-ai/locations/us-central1/publishers/google",
		},
		{
			name: "custom OpenAI-compatible vertex URL derives native sibling",
			cfg: providers.ProviderConfig{
				BaseURL: "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/endpoints/openapi/",
			},
			wantCompat: "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/endpoints/openapi",
			wantNative: "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/publishers/google",
		},
		{
			name: "custom native vertex URL derives OpenAI-compatible sibling",
			cfg: providers.ProviderConfig{
				BaseURL: "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/publishers/google/",
			},
			wantCompat: "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/endpoints/openapi",
			wantNative: "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/publishers/google",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCompat, gotNative := vertexBaseURLs(tt.cfg)
			if gotCompat != tt.wantCompat {
				t.Fatalf("OpenAI-compatible base = %q, want %q", gotCompat, tt.wantCompat)
			}
			if gotNative != tt.wantNative {
				t.Fatalf("native base = %q, want %q", gotNative, tt.wantNative)
			}
		})
	}
}

func testConfig() providers.ProviderConfig {
	return providers.ProviderConfig{
		Type:           "vertex",
		AuthType:       "gcp_adc",
		VertexProject:  "prod-ai",
		VertexLocation: "us-central1",
	}
}

func authedTestClient(base *http.Client) *http.Client {
	return googleauth.HTTPClient(base, oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: "vertex-token",
		TokenType:   "Bearer",
	}))
}
