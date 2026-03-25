// Package ollama provides Ollama API integration for the LLM gateway.
package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
)

// Registration provides factory registration for the Ollama provider.
var Registration = providers.Registration{
	Type: "ollama",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL:  defaultBaseURL,
		AllowAPIKeyless: true,
	},
}

const (
	defaultRootURL       = "http://localhost:11434"
	defaultBaseURL       = defaultRootURL + "/v1"
	defaultNativeBaseURL = defaultRootURL
)

// Provider implements the core.Provider interface for Ollama
type Provider struct {
	client       *llmclient.Client
	nativeClient *llmclient.Client
	apiKey       string // Accepted but ignored by Ollama
}

// New creates a new Ollama provider.
func New(providerCfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	p := &Provider{apiKey: providerCfg.APIKey}
	clientCfg := llmclient.Config{
		ProviderName:   "ollama",
		BaseURL:        defaultBaseURL,
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}
	p.client = llmclient.New(clientCfg, p.setHeaders)

	nativeCfg := llmclient.Config{
		ProviderName:   "ollama",
		BaseURL:        defaultNativeBaseURL,
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}
	p.nativeClient = llmclient.New(nativeCfg, p.setHeaders)
	p.SetBaseURL(providers.ResolveBaseURL(providerCfg.BaseURL, defaultBaseURL))
	return p
}

// NewWithHTTPClient creates a new Ollama provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	p := &Provider{apiKey: apiKey}
	cfg := llmclient.DefaultConfig("ollama", defaultBaseURL)
	cfg.Hooks = hooks
	p.client = llmclient.NewWithHTTPClient(httpClient, cfg, p.setHeaders)

	nativeCfg := llmclient.DefaultConfig("ollama", defaultNativeBaseURL)
	nativeCfg.Hooks = hooks
	p.nativeClient = llmclient.NewWithHTTPClient(httpClient, nativeCfg, p.setHeaders)
	return p
}

// SetBaseURL allows configuring a custom base URL for the provider.
// Also updates the native client by deriving the root URL (stripping /v1 suffix).
func (p *Provider) SetBaseURL(url string) {
	p.client.SetBaseURL(url)
	normalized := strings.TrimRight(url, "/")
	normalized = strings.TrimSuffix(normalized, "/v1")
	p.nativeClient.SetBaseURL(normalized)
}

// CheckAvailability verifies that Ollama is running and accessible.
// Makes a lightweight request to the models endpoint.
func (p *Provider) CheckAvailability(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := p.ListModels(ctx)
	return err
}

// setHeaders sets the required headers for Ollama API requests
func (p *Provider) setHeaders(req *http.Request) {
	// Ollama doesn't require authentication, but accepts Bearer token if provided
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	// Forward request ID if present in context
	if requestID := core.GetRequestID(req.Context()); requestID != "" {
		req.Header.Set("X-Request-ID", requestID)
	}
}

// ChatCompletion sends a chat completion request to Ollama
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	var resp core.ChatResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     req,
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return &resp, nil
}

// StreamChatCompletion returns a raw response body for streaming (caller must close)
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return p.client.DoStream(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     req.WithStreaming(),
	})
}

// ListModels retrieves the list of available models from Ollama
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	var resp core.ModelsResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/models",
	}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// Responses sends a Responses API request to Ollama (converted to chat format)
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return providers.ResponsesViaChat(ctx, p, req)
}

// StreamResponses returns a raw response body for streaming Responses API (caller must close)
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return providers.StreamResponsesViaChat(ctx, p, req, "ollama")
}

type ollamaEmbedRequest struct {
	Model string `json:"model"`
	Input any    `json:"input"`
}

type ollamaEmbedResponse struct {
	Model           string      `json:"model"`
	Embeddings      [][]float64 `json:"embeddings"`
	PromptEvalCount int         `json:"prompt_eval_count"`
}

// Embeddings sends an embeddings request to Ollama via its native /api/embed endpoint.
// Converts between OpenAI embedding format and Ollama's native format.
func (p *Provider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	ollamaReq := ollamaEmbedRequest{
		Model: req.Model,
		Input: req.Input,
	}

	var ollamaResp ollamaEmbedResponse
	err := p.nativeClient.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/api/embed",
		Body:     ollamaReq,
	}, &ollamaResp)
	if err != nil {
		return nil, err
	}

	data := make([]core.EmbeddingData, len(ollamaResp.Embeddings))
	for i, emb := range ollamaResp.Embeddings {
		raw, err := json.Marshal(emb)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal embedding at index %d: %w", i, err)
		}
		data[i] = core.EmbeddingData{
			Object:    "embedding",
			Embedding: raw,
			Index:     i,
		}
	}

	model := ollamaResp.Model
	if model == "" {
		model = req.Model
	}

	return &core.EmbeddingResponse{
		Object: "list",
		Data:   data,
		Model:  model,
		Usage: core.EmbeddingUsage{
			PromptTokens: ollamaResp.PromptEvalCount,
			TotalTokens:  ollamaResp.PromptEvalCount,
		},
	}, nil
}

// CreateBatch returns unsupported because Ollama has no native discounted batch API.
func (p *Provider) CreateBatch(_ context.Context, _ *core.BatchRequest) (*core.BatchResponse, error) {
	return nil, core.NewInvalidRequestError("ollama does not support native discounted batch processing", nil)
}

// GetBatch returns unsupported because Ollama has no native discounted batch API.
func (p *Provider) GetBatch(_ context.Context, _ string) (*core.BatchResponse, error) {
	return nil, core.NewInvalidRequestError("ollama does not support native discounted batch processing", nil)
}

// ListBatches returns unsupported because Ollama has no native discounted batch API.
func (p *Provider) ListBatches(_ context.Context, _ int, _ string) (*core.BatchListResponse, error) {
	return nil, core.NewInvalidRequestError("ollama does not support native discounted batch processing", nil)
}

// CancelBatch returns unsupported because Ollama has no native discounted batch API.
func (p *Provider) CancelBatch(_ context.Context, _ string) (*core.BatchResponse, error) {
	return nil, core.NewInvalidRequestError("ollama does not support native discounted batch processing", nil)
}

// GetBatchResults returns unsupported because Ollama has no native discounted batch API.
func (p *Provider) GetBatchResults(_ context.Context, _ string) (*core.BatchResultsResponse, error) {
	return nil, core.NewInvalidRequestError("ollama does not support native discounted batch processing", nil)
}
