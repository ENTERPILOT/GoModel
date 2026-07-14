// Package xiaomi provides Xiaomi MiMo API integration for the LLM gateway.
package xiaomi

import (
	"context"
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://api.xiaomimimo.com/v1"

// Registration provides factory registration for the Xiaomi MiMo provider.
var Registration = providers.Registration{
	Type: "xiaomi",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for Xiaomi MiMo.
// MiMo-specific extensions such as the "thinking" parameter pass through
// unchanged as unknown JSON fields.
type Provider struct {
	*openai.ChatCompatible
}

var _ core.Provider = (*Provider)(nil)

// New creates a new Xiaomi MiMo provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{openai.NewChatCompatible(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName: "xiaomi",
		BaseURL:      providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
	})}
}

// NewWithHTTPClient creates a new Xiaomi MiMo provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	return &Provider{openai.NewChatCompatibleWithHTTPClient(apiKey, httpClient, hooks, openai.CompatibleProviderConfig{
		ProviderName: "xiaomi",
		BaseURL:      providers.ResolveBaseURL(baseURL, defaultBaseURL),
	})}
}

// Embeddings returns an error because Xiaomi MiMo does not expose an embeddings endpoint.
func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("xiaomi does not support embeddings", nil)
}
