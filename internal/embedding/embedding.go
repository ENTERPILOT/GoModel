package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	all_minilm "github.com/clems4ever/all-minilm-l6-v2-go/all_minilm_l6_v2"

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
// its api_key and base_url are reused to call POST /v1/embeddings (OpenAI-compatible).
// When base_url is omitted, defaults match the gateway's provider packages (e.g. gemini → Google OpenAI-compatible host), not api.openai.com.
func NewEmbedder(cfg config.EmbedderConfig, rawProviders map[string]config.RawProviderConfig) (Embedder, error) {
	if cfg.Provider == "" || cfg.Provider == "local" {
		return newMiniLMEmbedder(cfg.ModelPath)
	}
	raw, ok := rawProviders[cfg.Provider]
	if !ok {
		return nil, fmt.Errorf("embedding: provider %q not found in providers map", cfg.Provider)
	}
	baseURL := embeddingAPIBaseURL(raw)
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
		baseURL:    baseURL,
		apiKey:     raw.APIKey,
		model:      model,
		httpClient: &http.Client{Timeout: defaultTimeout},
	}, nil
}

// normalizeGeminiEmbeddingModel maps OpenAI-style embedding IDs that do not exist on
// Google's OpenAI-compatible /v1/embeddings surface to a supported Gemini model.
// See: https://ai.google.dev/gemini-api/docs/openai#embeddings
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

func embeddingAPIBaseURL(raw config.RawProviderConfig) string {
	if u := strings.TrimSpace(raw.BaseURL); u != "" {
		return strings.TrimSuffix(u, "/")
	}
	switch strings.ToLower(strings.TrimSpace(raw.Type)) {
	case "gemini":
		return "https://generativelanguage.googleapis.com/v1beta/openai"
	case "groq":
		return "https://api.groq.com/openai"
	case "xai":
		return "https://api.x.ai/v1"
	case "openrouter":
		return "https://openrouter.ai/api/v1"
	default:
		return "https://api.openai.com"
	}
}

// MiniLMEmbedder uses the local all-MiniLM-L6-v2 ONNX model.
// No network calls are made; the model runs in-process.
type miniLMEmbedder struct {
	model *all_minilm.Model
}

func newMiniLMEmbedder(runtimePath string) (*miniLMEmbedder, error) {
	if runtimePath == "" {
		runtimePath = os.Getenv("ONNXRUNTIME_LIB_PATH")
	}
	var opts []all_minilm.ModelOption
	if runtimePath != "" {
		opts = append(opts, all_minilm.WithRuntimePath(runtimePath))
	}
	m, err := all_minilm.NewModel(opts...)
	if err != nil {
		return nil, fmt.Errorf("embedding: failed to load local MiniLM model: %w", err)
	}
	return &miniLMEmbedder{model: m}, nil
}

func (e *miniLMEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	type result struct {
		vec []float32
		err error
	}
	ch := make(chan result, 1)
	go func() {
		vec, err := e.model.Compute(text, true)
		ch <- result{vec, err}
	}()
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("embedding: MiniLM compute failed: %w", ctx.Err())
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("embedding: MiniLM compute failed: %w", r.err)
		}
		return r.vec, nil
	}
}

func (e *miniLMEmbedder) Close() error {
	if e.model != nil {
		e.model.Close()
	}
	return nil
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
