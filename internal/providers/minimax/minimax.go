// Package minimax provides MiniMax API integration for the LLM gateway.
package minimax

import (
	"context"
	"io"
	"net/http"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
	"gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://api.minimax.io/v1"

// defaultTemperature is the fallback temperature for MiniMax.
// MiniMax requires temperature to be in (0.0, 1.0] — zero is not allowed.
const defaultTemperature = 1.0

// Registration provides factory registration for the MiniMax provider.
var Registration = providers.Registration{
	Type: "minimax",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for MiniMax.
type Provider struct {
	compatible *openai.CompatibleProvider
}

// New creates a new MiniMax provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	baseURL := providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL)
	return &Provider{
		compatible: openai.NewCompatibleProvider(cfg.APIKey, opts, openai.CompatibleProviderConfig{
			ProviderName: "minimax",
			BaseURL:      baseURL,
			SetHeaders:   setHeaders,
		}),
	}
}

// NewWithHTTPClient creates a new MiniMax provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	resolvedBaseURL := providers.ResolveBaseURL(baseURL, defaultBaseURL)
	return &Provider{
		compatible: openai.NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, openai.CompatibleProviderConfig{
			ProviderName: "minimax",
			BaseURL:      resolvedBaseURL,
			SetHeaders:   setHeaders,
		}),
	}
}

// SetBaseURL allows configuring a custom base URL for the provider.
func (p *Provider) SetBaseURL(url string) {
	p.compatible.SetBaseURL(url)
}

func setHeaders(req *http.Request, apiKey string) {
	req.Header.Set("Authorization", "Bearer "+apiKey)
}

// clampTemperature returns the request with temperature clamped to (0.0, 1.0].
// MiniMax rejects temperature=0; if zero or negative, defaultTemperature is used.
func clampTemperature(req *core.ChatRequest) *core.ChatRequest {
	if req == nil || req.Temperature == nil || *req.Temperature > 0 {
		return req
	}
	t := defaultTemperature
	cloned := *req
	cloned.Temperature = &t
	return &cloned
}

// ChatCompletion sends a chat completion request to MiniMax.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return p.compatible.ChatCompletion(ctx, clampTemperature(req))
}

// StreamChatCompletion returns a raw response body for streaming.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return p.compatible.StreamChatCompletion(ctx, clampTemperature(req))
}

// ListModels retrieves the list of available models from MiniMax.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	return p.compatible.ListModels(ctx)
}

// Responses sends a Responses API request to MiniMax using chat-completions translation.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return providers.ResponsesViaChat(ctx, p, req)
}

// StreamResponses streams a Responses API request to MiniMax using chat-completions translation.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return providers.StreamResponsesViaChat(ctx, p, req, "minimax")
}

// Embeddings sends an embeddings request to MiniMax.
func (p *Provider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return p.compatible.Embeddings(ctx, req)
}

// Passthrough routes an opaque provider-native request to MiniMax.
func (p *Provider) Passthrough(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	return p.compatible.Passthrough(ctx, req)
}
