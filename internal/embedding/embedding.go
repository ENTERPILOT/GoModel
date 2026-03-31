package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"gomodel/config"
)

// defaultTimeout caps how long embedding HTTP calls may block the client.
const defaultTimeout = 120 * time.Second

// Embedder converts text into a float32 vector representation.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	Close() error
}

// NewEmbedder creates an Embedder based on cfg.
//
// When cfg.Provider is "local" or empty, a MiniLMEmbedder backed by the
// bundled all-MiniLM-L6-v2 ONNX model is returned. The ONNX Runtime shared
// library is discovered via the ONNXRUNTIME_LIB_PATH environment variable or
// cfg.ModelPath if set.
//
// For any other provider value, the named provider must exist in rawProviders;
// its api_key and base_url are reused to call POST /v1/embeddings.
func NewEmbedder(cfg config.EmbedderConfig, rawProviders map[string]config.RawProviderConfig) (Embedder, error) {
	if cfg.Provider == "" || cfg.Provider == "local" {
		return newMiniLMEmbedder(cfg.ModelPath)
	}
	raw, ok := rawProviders[cfg.Provider]
	if !ok {
		return nil, fmt.Errorf("embedding: provider %q not found in providers map", cfg.Provider)
	}
	baseURL := raw.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	model := cfg.Model
	if model == "" {
		model = "text-embedding-ada-002"
	}
	return &apiEmbedder{
		baseURL:    baseURL,
		apiKey:     raw.APIKey,
		model:      model,
		httpClient: &http.Client{Timeout: defaultTimeout},
	}, nil
}

// apiEmbedder calls POST /v1/embeddings on any OpenAI-compatible endpoint.
type apiEmbedder struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

type embeddingRequest struct {
	Input string `json:"input"`
	Model string `json:"model"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (e *apiEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(embeddingRequest{Input: text, Model: e.model})
	if err != nil {
		return nil, fmt.Errorf("embedding: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embedding: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}
	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding: API call failed: %w", err)
	}
	defer resp.Body.Close()
	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("embedding: read response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embedding: API returned status %d: %s", resp.StatusCode, string(rawBody))
	}
	var parsed embeddingResponse
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		return nil, fmt.Errorf("embedding: decode response: %w", err)
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("embedding: API error: %s", parsed.Error.Message)
	}
	if len(parsed.Data) == 0 || len(parsed.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("embedding: API returned empty embedding")
	}
	return parsed.Data[0].Embedding, nil
}

func (e *apiEmbedder) Close() error { return nil }
