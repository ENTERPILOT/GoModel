// Package chutes provides Chutes AI integration for the LLM gateway.
package chutes

import (
	"context"
	"io"
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://llm.chutes.ai/v1"

// Registration provides factory registration for the Chutes AI provider.
var Registration = providers.Registration{
	Type: "chutes",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements Chutes' OpenAI-compatible chat surface. Chutes does not
// expose native Responses or embeddings endpoints, so Responses requests are
// translated through chat completions and embeddings fail locally.
type Provider struct {
	compat *openai.CompatibleProvider
}

var _ core.Provider = (*Provider)(nil)
var _ core.PassthroughProvider = (*Provider)(nil)

// New creates a new Chutes AI provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{compat: openai.NewCompatibleProvider(cfg.APIKey, opts, compatibleConfig(
		providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
	))}
}

// NewWithHTTPClient creates a new Chutes AI provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	return &Provider{compat: openai.NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, compatibleConfig(
		providers.ResolveBaseURL(baseURL, defaultBaseURL),
	))}
}

// compatibleConfig returns the shared OpenAI-compatible transport settings for Chutes.
func compatibleConfig(baseURL string) openai.CompatibleProviderConfig {
	return openai.CompatibleProviderConfig{
		ProviderName: "chutes",
		BaseURL:      baseURL,
		SetHeaders:   setHeaders,
	}
}

// setHeaders applies Chutes' bearer-token authentication.
func setHeaders(req *http.Request, apiKey string) {
	providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{AuthScheme: "Bearer "})
}

// SetBaseURL changes the Chutes API base URL.
func (p *Provider) SetBaseURL(baseURL string) {
	p.compat.SetBaseURL(baseURL)
}

// ChatCompletion sends a chat completion request to Chutes.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return p.compat.ChatCompletion(ctx, req)
}

// StreamChatCompletion sends a streaming chat completion request to Chutes.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return p.compat.StreamChatCompletion(ctx, req)
}

// Responses translates an OpenAI Responses request through Chutes chat completions.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return providers.ResponsesViaChat(ctx, p, req)
}

// StreamResponses translates a streaming Responses request through Chutes chat completions.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return providers.StreamResponsesViaChat(ctx, p, req, "chutes")
}

// Embeddings returns an error because the shared Chutes LLM endpoint does not
// expose an OpenAI-compatible embeddings route.
func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("chutes does not support embeddings", nil)
}

// Passthrough forwards an opaque request to Chutes.
func (p *Provider) Passthrough(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	return p.compat.Passthrough(ctx, req)
}
