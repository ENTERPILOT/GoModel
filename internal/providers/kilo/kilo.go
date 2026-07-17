// Package kilo provides Kilo AI Gateway integration for the LLM gateway.
package kilo

import (
	"context"
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://api.kilo.ai/api/gateway"

// Registration provides factory registration for the Kilo AI provider.
var Registration = providers.Registration{
	Type:                        "kilo",
	New:                         New,
	PassthroughSemanticEnricher: passthroughSemanticEnricher,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for Kilo AI. Kilo exposes
// OpenAI-compatible chat completions, streaming, tool calling, and model
// listing. Its provider/model IDs pass through unchanged.
type Provider struct {
	*openai.ChatCompatible
}

var _ core.Provider = (*Provider)(nil)

// New creates a new Kilo AI provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{openai.NewChatCompatible(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName: "kilo",
		BaseURL:      providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
	})}
}

// NewWithHTTPClient creates a new Kilo AI provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	return &Provider{openai.NewChatCompatibleWithHTTPClient(apiKey, httpClient, hooks, openai.CompatibleProviderConfig{
		ProviderName: "kilo",
		BaseURL:      providers.ResolveBaseURL(baseURL, defaultBaseURL),
	})}
}

// Embeddings returns an error because Kilo AI does not expose an embeddings endpoint.
func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("kilo does not support embeddings", nil)
}
