// Package kimicode provides Kimi Code API integration for the LLM gateway.
//
// The "kimicode" provider routes to Kimi Code's OpenAI-compatible chat
// completions endpoint, so all transport goes through the shared chat-centric
// adapter and model IDs are forwarded unchanged.
package kimicode

import (
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://api.kimi.com/coding/v1"

// Registration provides factory registration for the Kimi Code provider.
var Registration = providers.Registration{
	Type: "kimicode",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for Kimi Code. Kimi Code is
// OpenAI-compatible, so all transport goes through the shared chat-centric
// adapter: chat completions, model listing, embeddings, and passthrough are
// exposed via the embedded *openai.ChatCompatible.
type Provider struct {
	*openai.ChatCompatible
}

var _ core.Provider = (*Provider)(nil)

// New creates a new Kimi Code provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{openai.NewChatCompatible(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName: "kimicode",
		BaseURL:      providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
	})}
}

// NewWithHTTPClient creates a new Kimi Code provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
//
// The signature is intentionally stable and matches every other chat-compatible
// provider on main: (apiKey, baseURL, httpClient, hooks).
func NewWithHTTPClient(apiKey string, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	return &Provider{openai.NewChatCompatibleWithHTTPClient(apiKey, httpClient, hooks, openai.CompatibleProviderConfig{
		ProviderName: "kimicode",
		BaseURL:      providers.ResolveBaseURL(baseURL, defaultBaseURL),
	})}
}
