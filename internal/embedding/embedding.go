package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"gomodel/config"
)

const defaultTimeout = 120 * time.Second

type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	Close() error
}

// NewEmbedder returns an Embedder that calls POST …/v1/embeddings (OpenAI-compatible)
// for the named provider. cfg.Provider must be a non-empty key in resolvedProviders
// (the env-merged, credential-filtered map from providers.Init). That entry's
// api_key and base_url are reused; base_url must be non-empty (discovery defaults
// fill it when only an API key is set).
func NewEmbedder(cfg config.EmbedderConfig, resolvedProviders map[string]config.RawProviderConfig) (Embedder, error) {
	p := strings.TrimSpace(cfg.Provider)
	if p == "" {
		return nil, fmt.Errorf("embedding: embedder provider is required (set cache.response.semantic.embedder.provider to a key in the providers map, e.g. openai or gemini)")
	}
	if strings.EqualFold(p, "local") {
		return nil, fmt.Errorf("embedding: local embedding is not supported; use a named API provider")
	}
	raw, ok := resolvedProviders[p]
	if !ok {
		return nil, fmt.Errorf("embedding: provider %q not found among credential-resolved providers (check key spelling, env vars, and that the provider passes gateway credential rules)", p)
	}
	endpointURL, err := openAIEmbeddingsEndpointURL(raw.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("embedding: provider %q: %w", p, err)
	}
	typ := strings.ToLower(strings.TrimSpace(raw.Type))
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		if typ == "gemini" {
			model = "gemini-embedding-001"
		} else {
			model = "text-embedding-ada-002"
		}
	} else if typ == "gemini" {
		model = normalizeGeminiEmbeddingModel(model)
	}
	return &apiEmbedder{
		endpointURL: endpointURL,
		apiKey:      raw.APIKey,
		model:       model,
		httpClient:  &http.Client{Timeout: defaultTimeout},
	}, nil
}

func normalizeGeminiEmbeddingModel(model string) string {
	lower := strings.ToLower(strings.TrimSpace(model))
	if lower == "" {
		return "gemini-embedding-001"
	}
	if strings.HasPrefix(lower, "text-embedding-") {
		slog.Warn("embedding: Gemini OpenAI-compatible API uses gemini-embedding-* for /v1/embeddings; replacing configured model",
			"from", model,
			"to", "gemini-embedding-001")
		return "gemini-embedding-001"
	}
	return model
}

// openAIEmbeddingsEndpointURL builds the full POST URL for OpenAI-compatible embeddings.
// Resolved provider base URLs from discovery often end with "/v1"; if so, only "/embeddings"
// is appended to avoid "/v1/v1/embeddings".
func openAIEmbeddingsEndpointURL(base string) (string, error) {
	b := strings.TrimSpace(base)
	if b == "" {
		return "", fmt.Errorf("base_url is empty; set base_url on the provider or rely on provider env defaults")
	}
	b = strings.TrimSuffix(b, "/")
	if strings.HasSuffix(b, "/v1") {
		return b + "/embeddings", nil
	}
	return b + "/v1/embeddings", nil
}

type apiEmbedder struct {
	endpointURL string
	apiKey      string
	model       string
	httpClient  *http.Client
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpointURL, bytes.NewReader(body))
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
