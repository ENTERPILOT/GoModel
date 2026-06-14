// Package opencodego provides OpenCode Zen (Go subscription) integration for
// the LLM gateway.
package opencodego

import (
	"context"
	"net/http"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
	"gomodel/internal/providers/openai"
)

// defaultBaseURL is the OpenCode Zen "Go" endpoint. Its /chat/completions and
// /models routes are OpenAI-compatible and use Bearer auth, so the chat-centric
// adapter works unchanged. Override with OPENCODE_GO_BASE_URL when needed.
const defaultBaseURL = "https://opencode.ai/zen/go/v1"

// Registration provides factory registration for the OpenCode Go provider.
// The "opencode_go" type derives OPENCODE_GO_API_KEY, OPENCODE_GO_BASE_URL, and
// OPENCODE_GO_MODELS by convention.
var Registration = providers.Registration{
	Type: "opencode_go",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for OpenCode Go.
type Provider struct {
	*openai.ChatCompatible
}

var _ core.Provider = (*Provider)(nil)

// New creates a new OpenCode Go provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{openai.NewChatCompatible(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName: "opencode_go",
		BaseURL:      providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
	})}
}

// NewWithHTTPClient creates a new OpenCode Go provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	return &Provider{openai.NewChatCompatibleWithHTTPClient(apiKey, httpClient, hooks, openai.CompatibleProviderConfig{
		ProviderName: "opencode_go",
		BaseURL:      providers.ResolveBaseURL(baseURL, defaultBaseURL),
	})}
}

// Embeddings returns an error because OpenCode Go does not expose an embeddings endpoint.
func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("opencode_go does not support embeddings", nil)
}
