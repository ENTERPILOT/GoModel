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
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/googlecommon"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"golang.org/x/oauth2"
)

const (
	nativeBasePath      = "/v1/projects/prod-ai/locations/us-central1/publishers/google"
	generateContentJSON = `{
		"responseId": "vertex-auth",
		"candidates": [{
			"content": {"role": "model", "parts": [{"text": "ok"}]},
			"finishReason": "STOP"
		}]
	}`
)

// tokenServer answers every OAuth token exchange with accessToken; the
// recorder keeps the form body so the test can inspect the grant type.
func tokenServer(t *testing.T, accessToken string) (string, *providertest.Capture) {
	t.Helper()
	server, capture := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": accessToken,
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	})
	return server.URL, capture
}

func TestProviderDoesNotExposeFilesOrBatches(t *testing.T) {
	provider := newProvider(testConfig(), providers.ProviderOptions{}, authedTestClient(http.DefaultClient))
	_, ok := any(provider).(core.NativeFileProvider)
	assert.False(t, ok, "provider should not implement core.NativeFileProvider")
	_, ok = any(provider).(core.NativeBatchProvider)
	assert.False(t, ok, "provider should not implement core.NativeBatchProvider")
}

func TestEmbeddingsUsesNativePrediction(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"predictions": [
			{"embeddings": {"values": [0.1, 0.2, 0.3], "statistics": {"token_count": 4}}},
			{"embeddings": {"values": [0.4, 0.5, 0.6], "statistics": {"token_count": 5}}}
		]
	}`)

	var operation string
	dimensions := 3
	cfg := testConfig()
	cfg.BaseURL = server.URL + nativeBasePath
	provider := newProvider(cfg, providers.ProviderOptions{Hooks: llmclient.Hooks{
		OnRequestStart: func(ctx context.Context, info llmclient.RequestInfo) context.Context {
			operation = info.Operation
			return ctx
		},
	}}, authedTestClient(server.Client()))

	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model:      "google/text-embedding-005",
		Input:      []string{"hello", "world"},
		Dimensions: &dimensions,
	})
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, nativeBasePath+"/models/text-embedding-005:predict", req.Path)
	assert.Equal(t, "Bearer vertex-token", req.Header.Get("Authorization"))
	assert.Empty(t, req.Header.Get("x-goog-api-key"))
	var payload vertexEmbeddingPredictRequest
	require.NoError(t, json.Unmarshal(req.Body, &payload))
	require.Len(t, payload.Instances, 2)
	assert.Equal(t, "hello", payload.Instances[0].Content)
	assert.Equal(t, "world", payload.Instances[1].Content)
	assert.Equal(t, float64(3), payload.Parameters["outputDimensionality"])

	assert.Equal(t, "vertex", resp.Provider)
	require.Len(t, resp.Data, 2)
	assert.Equal(t, `[0.1,0.2,0.3]`, string(resp.Data[0].Embedding))
	assert.Equal(t, 9, resp.Usage.PromptTokens)
	assert.Equal(t, 9, resp.Usage.TotalTokens)
	assert.Equal(t, llmclient.OperationEmbeddings, operation)
}

func TestEmbeddingsRejectsEmptyStringInBatch(t *testing.T) {
	provider := newProvider(testConfig(), providers.ProviderOptions{}, authedTestClient(http.DefaultClient))

	_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "google/text-embedding-005",
		Input: []string{"hello", "", "world"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "embedding input must not be empty")
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
	require.NoError(t, err)
	require.Len(t, resp.Data, 1)

	var encoded string
	require.NoError(t, json.Unmarshal(resp.Data[0].Embedding, &encoded))
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	require.Len(t, decoded, 8)

	values := []float32{
		math.Float32frombits(binary.LittleEndian.Uint32(decoded[0:4])),
		math.Float32frombits(binary.LittleEndian.Uint32(decoded[4:8])),
	}
	assert.Equal(t, []float32{0.5, -1.25}, values)
	assert.Equal(t, 3, resp.Usage.PromptTokens)
	assert.Equal(t, 3, resp.Usage.TotalTokens)
}

func TestNewAcceptsBaseURLWithoutProjectLocation(t *testing.T) {
	provider := newProvider(providers.ProviderConfig{
		Type:     "vertex",
		AuthType: "gcp_adc",
		BaseURL:  "https://proxy.example.com" + nativeBasePath,
	}, providers.ProviderOptions{}, authedTestClient(http.DefaultClient))
	require.NoError(t, provider.ready())
}

func TestNewRejectsUnsupportedAuthType(t *testing.T) {
	provider := newProvider(providers.ProviderConfig{
		Type:     "vertex",
		AuthType: "api_key",
		BaseURL:  "https://proxy.example.com" + nativeBasePath,
	}, providers.ProviderOptions{}, authedTestClient(http.DefaultClient))

	err := provider.ready()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unsupported vertex AI auth type "api_key"`)
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
			tokenURL, tokenCapture := tokenServer(t, tt.token)
			upstream, upstreamCapture := providertest.JSONServer(t, http.StatusOK, generateContentJSON)

			cfg := testConfig()
			cfg.AuthType = tt.authType
			cfg.APIMode = "native"
			cfg.BaseURL = upstream.URL + nativeBasePath
			tt.configure(t, &cfg, tokenURL)

			provider := New(cfg, providers.ProviderOptions{})
			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:    "google/gemini-2.5-flash",
				Messages: []core.Message{{Role: "user", Content: "Hello"}},
			})
			require.NoError(t, err)
			require.NotNil(t, resp)
			assert.Equal(t, "vertex", resp.Provider)

			form, err := url.ParseQuery(string(tokenCapture.Last(t).Body))
			require.NoError(t, err)
			assert.Equal(t, tt.grantType, form.Get("grant_type"))

			req := upstreamCapture.Last(t)
			assert.Equal(t, nativeBasePath+"/models/gemini-2.5-flash:generateContent", req.Path)
			assert.Equal(t, "Bearer "+tt.token, req.Header.Get("Authorization"))
			assert.Empty(t, req.Header.Get("x-goog-api-key"))
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
			wantNative: "https://aiplatform.googleapis.com" + nativeBasePath,
		},
		{
			name: "custom OpenAI-compatible vertex URL derives native sibling",
			cfg: providers.ProviderConfig{
				BaseURL: "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/endpoints/openapi/",
			},
			wantCompat: "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/endpoints/openapi",
			wantNative: "https://proxy.example.com" + nativeBasePath,
		},
		{
			name: "custom native vertex URL derives OpenAI-compatible sibling",
			cfg: providers.ProviderConfig{
				BaseURL: "https://proxy.example.com" + nativeBasePath + "/",
			},
			wantCompat: "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/endpoints/openapi",
			wantNative: "https://proxy.example.com" + nativeBasePath,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCompat, gotNative := googlecommon.VertexBaseURLs(tt.cfg.BaseURL, tt.cfg.VertexProject, tt.cfg.VertexLocation)
			assert.Equal(t, tt.wantCompat, gotCompat)
			assert.Equal(t, tt.wantNative, gotNative)
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
	return googlecommon.HTTPClient(base, oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: "vertex-token",
		TokenType:   "Bearer",
	}), "")
}

func vertexADCCredentialsFile(t *testing.T, tokenURL string) string {
	t.Helper()
	return vertexADCCredentialsFileWithQuotaProject(t, tokenURL, "")
}

func vertexADCCredentialsFileWithQuotaProject(t *testing.T, tokenURL, quotaProject string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "adc.json")
	contents := map[string]string{
		"type":          "authorized_user",
		"client_id":     "adc-client-id",
		"client_secret": "adc-client-secret",
		"refresh_token": "adc-refresh-token",
		"token_uri":     tokenURL,
	}
	if quotaProject != "" {
		contents["quota_project_id"] = quotaProject
	}
	encoded, err := json.Marshal(contents)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, encoded, 0o600))
	return path
}

func TestNewSetsQuotaProjectHeaderOnVertexRequests(t *testing.T) {
	tests := []struct {
		name      string
		configure func(t *testing.T, cfg *providers.ProviderConfig, tokenURL string)
		wantProj  string
	}{
		{
			name: "ADC quota project wins over VERTEX_PROJECT",
			configure: func(t *testing.T, cfg *providers.ProviderConfig, tokenURL string) {
				t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", vertexADCCredentialsFileWithQuotaProject(t, tokenURL, "billing-project"))
				cfg.AuthType = "gcp_adc"
			},
			wantProj: "billing-project",
		},
		{
			name: "ADC without quota project falls back to VERTEX_PROJECT",
			configure: func(t *testing.T, cfg *providers.ProviderConfig, tokenURL string) {
				t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", vertexADCCredentialsFile(t, tokenURL))
				cfg.AuthType = "gcp_adc"
			},
			wantProj: "prod-ai",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokenURL, _ := tokenServer(t, "token")
			upstream, capture := providertest.JSONServer(t, http.StatusOK, generateContentJSON)

			cfg := testConfig()
			cfg.APIMode = "native"
			cfg.BaseURL = upstream.URL + nativeBasePath
			tt.configure(t, &cfg, tokenURL)

			provider := New(cfg, providers.ProviderOptions{})
			_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:    "google/gemini-2.5-flash",
				Messages: []core.Message{{Role: "user", Content: "hi"}},
			})
			require.NoError(t, err)
			assert.Equal(t, tt.wantProj, capture.Last(t).Header.Get("X-Goog-User-Project"))
		})
	}
}

func vertexServiceAccountCredentialsFile(t *testing.T, tokenURL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "service-account.json")
	require.NoError(t, os.WriteFile(path, []byte(vertexServiceAccountCredentials(t, tokenURL)), 0o600))
	return path
}

func vertexServiceAccountCredentials(t *testing.T, tokenURL string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)

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
	require.NoError(t, err)
	return string(encoded)
}

func TestCreateImageDelegatesToGeminiPredict(t *testing.T) {
	t.Setenv("USE_GOOGLE_GEMINI_NATIVE_API", "true")
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"predictions": [{"bytesBase64Encoded": "aW1n", "mimeType": "image/png"}]}`)

	cfg := testConfig()
	cfg.BaseURL = server.URL + nativeBasePath
	provider := newProvider(cfg, providers.ProviderOptions{}, authedTestClient(server.Client()))

	resp, err := provider.CreateImage(context.Background(), &core.ImageGenerationRequest{
		Model:  "google/imagen-4.0-generate-001",
		Prompt: "a mountain",
	})
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, nativeBasePath+"/models/imagen-4.0-generate-001:predict", req.Path)
	assert.Equal(t, "Bearer vertex-token", req.Header.Get("Authorization"))
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "aW1n", resp.Data[0].B64JSON)
	assert.Equal(t, "vertex", resp.Provider)
}
