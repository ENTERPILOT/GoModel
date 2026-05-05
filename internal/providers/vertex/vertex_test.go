package vertex

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestOpenAIEmbeddingResponseSupportsBase64Encoding(t *testing.T) {
	resp, err := openAIEmbeddingResponse(&core.EmbeddingRequest{
		Model:          "google/text-embedding-005",
		EncodingFormat: "base64",
	}, &vertexEmbeddingPredictResponse{
		Predictions: []vertexEmbeddingPrediction{{
			Embeddings: vertexEmbeddingValues{
				Values:     []float64{0.5, -1.25},
				Statistics: vertexEmbeddingStatistics{TokenCount: 3},
			},
		}},
	})
	if err != nil {
		t.Fatalf("openAIEmbeddingResponse() error = %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("data = %+v, want one embedding", resp.Data)
	}

	var encoded string
	if err := json.Unmarshal(resp.Data[0].Embedding, &encoded); err != nil {
		t.Fatalf("embedding is not JSON string: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("embedding is not valid base64: %v", err)
	}
	if len(decoded) != 8 {
		t.Fatalf("decoded length = %d, want 8", len(decoded))
	}
	values := []float32{
		math.Float32frombits(binary.LittleEndian.Uint32(decoded[0:4])),
		math.Float32frombits(binary.LittleEndian.Uint32(decoded[4:8])),
	}
	if values[0] != 0.5 || values[1] != -1.25 {
		t.Fatalf("decoded values = %v, want [0.5 -1.25]", values)
	}
	if resp.Usage.PromptTokens != 3 || resp.Usage.TotalTokens != 3 {
		t.Fatalf("usage = %+v, want 3 prompt/total tokens", resp.Usage)
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

func TestNewAuthFormsInjectBearerToken(t *testing.T) {
	tests := []struct {
		name      string
		authType  string
		token     string
		grantType string
		configure func(t *testing.T, cfg *providers.ProviderConfig, tokenURL string)
	}{
		{
			name:      "ADC",
			authType:  "gcp_adc",
			token:     "adc-token",
			grantType: "refresh_token",
			configure: func(t *testing.T, cfg *providers.ProviderConfig, tokenURL string) {
				t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", vertexADCCredentialsFile(t, tokenURL))
			},
		},
		{
			name:      "service account JSON",
			authType:  "gcp_service_account",
			token:     "service-account-token",
			grantType: "urn:ietf:params:oauth:grant-type:jwt-bearer",
			configure: func(t *testing.T, cfg *providers.ProviderConfig, tokenURL string) {
				cfg.ServiceAccountJSON = vertexServiceAccountCredentials(t, tokenURL)
			},
		},
		{
			name:      "service account file",
			authType:  "gcp_service_account",
			token:     "service-account-token",
			grantType: "urn:ietf:params:oauth:grant-type:jwt-bearer",
			configure: func(t *testing.T, cfg *providers.ProviderConfig, tokenURL string) {
				cfg.ServiceAccountFile = vertexServiceAccountCredentialsFile(t, tokenURL)
			},
		},
		{
			name:      "service account JSON base64",
			authType:  "gcp_service_account",
			token:     "service-account-token",
			grantType: "urn:ietf:params:oauth:grant-type:jwt-bearer",
			configure: func(t *testing.T, cfg *providers.ProviderConfig, tokenURL string) {
				credentials := vertexServiceAccountCredentials(t, tokenURL)
				cfg.ServiceAccountJSONBase64 = base64.StdEncoding.EncodeToString([]byte(credentials))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotGrantType string
			tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Fatalf("ParseForm() error = %v", err)
				}
				gotGrantType = r.PostForm.Get("grant_type")
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token": tt.token,
					"token_type":   "Bearer",
					"expires_in":   3600,
				})
			}))
			defer tokenServer.Close()

			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/projects/prod-ai/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent" {
					t.Errorf("Path = %q, want Vertex native generateContent endpoint", r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer "+tt.token {
					t.Errorf("Authorization = %q, want Bearer %s", got, tt.token)
				}
				if got := r.Header.Get("x-goog-api-key"); got != "" {
					t.Errorf("x-goog-api-key = %q, want empty for Vertex OAuth", got)
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{
					"responseId": "vertex-auth",
					"candidates": [{
						"content": {"role": "model", "parts": [{"text": "ok"}]},
						"finishReason": "STOP"
					}]
				}`))
			}))
			defer upstream.Close()

			cfg := testConfig()
			cfg.AuthType = tt.authType
			cfg.APIMode = "native"
			cfg.BaseURL = upstream.URL + "/v1/projects/prod-ai/locations/us-central1/publishers/google"
			tt.configure(t, &cfg, tokenServer.URL)

			provider := New(cfg, providers.ProviderOptions{})
			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model: "google/gemini-2.5-flash",
				Messages: []core.Message{
					{Role: "user", Content: "Hello"},
				},
			})
			if err != nil {
				t.Fatalf("ChatCompletion() error = %v", err)
			}
			if resp == nil || resp.Provider != "vertex" {
				t.Fatalf("response = %+v, want vertex response", resp)
			}
			if gotGrantType != tt.grantType {
				t.Fatalf("grant_type = %q, want %q", gotGrantType, tt.grantType)
			}
		})
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

func vertexADCCredentialsFile(t *testing.T, tokenURL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "adc.json")
	contents := map[string]string{
		"type":          "authorized_user",
		"client_id":     "adc-client-id",
		"client_secret": "adc-client-secret",
		"refresh_token": "adc-refresh-token",
		"token_uri":     tokenURL,
	}
	encoded, err := json.Marshal(contents)
	if err != nil {
		t.Fatalf("failed to marshal ADC credentials: %v", err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("failed to write ADC credentials: %v", err)
	}
	return path
}

func vertexServiceAccountCredentialsFile(t *testing.T, tokenURL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "service-account.json")
	if err := os.WriteFile(path, []byte(vertexServiceAccountCredentials(t, tokenURL)), 0o600); err != nil {
		t.Fatalf("failed to write service account credentials: %v", err)
	}
	return path
}

func vertexServiceAccountCredentials(t *testing.T, tokenURL string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate test RSA key: %v", err)
	}
	keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("failed to marshal test RSA key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: keyBytes,
	})
	contents := map[string]string{
		"type":           "service_account",
		"client_email":   "service@example.com",
		"private_key_id": "test-key-id",
		"private_key":    string(keyPEM),
		"token_uri":      tokenURL,
	}
	encoded, err := json.Marshal(contents)
	if err != nil {
		t.Fatalf("failed to marshal service account credentials: %v", err)
	}
	return string(encoded)
}
