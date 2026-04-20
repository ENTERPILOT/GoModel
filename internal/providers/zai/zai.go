// Package zai provides Z.ai API integration for the LLM gateway.
package zai

import (
	"context"
	"io"
	"net/http"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
	"gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://api.z.ai/api/paas/v4"

// Registration provides factory registration for the Z.ai provider.
var Registration = providers.Registration{
	Type: "zai",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for Z.ai.
type Provider struct {
	compatible *openai.CompatibleProvider
}

// New creates a new Z.ai provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	baseURL := providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL)
	return &Provider{
		compatible: openai.NewCompatibleProvider(cfg.APIKey, opts, openai.CompatibleProviderConfig{
			ProviderName: "zai",
			BaseURL:      baseURL,
			SetHeaders:   setHeaders,
		}),
	}
}

// NewWithHTTPClient creates a new Z.ai provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	resolvedBaseURL := providers.ResolveBaseURL(baseURL, defaultBaseURL)
	return &Provider{
		compatible: openai.NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, openai.CompatibleProviderConfig{
			ProviderName: "zai",
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

// ChatCompletion sends a chat completion request to Z.ai.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return p.compatible.ChatCompletion(ctx, req)
}

// StreamChatCompletion returns a raw response body for streaming.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return p.compatible.StreamChatCompletion(ctx, req)
}

// ListModels retrieves the list of available models from Z.ai.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	return p.compatible.ListModels(ctx)
}

// Responses sends a Responses API request to Z.ai using chat-completions translation.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return providers.ResponsesViaChat(ctx, p, req)
}

// StreamResponses streams a Responses API request to Z.ai using chat-completions translation.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return providers.StreamResponsesViaChat(ctx, p, req, "zai")
}

// Embeddings sends an embeddings request to Z.ai.
func (p *Provider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return p.compatible.Embeddings(ctx, req)
}

// Passthrough routes an opaque provider-native request to Z.ai.
func (p *Provider) Passthrough(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	return p.compatible.Passthrough(ctx, req)
}
