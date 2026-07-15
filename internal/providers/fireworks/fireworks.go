// Package fireworks provides Fireworks AI API integration for the LLM gateway.
package fireworks

import (
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://api.fireworks.ai/inference/v1"

// Registration provides factory registration for the Fireworks AI provider.
var Registration = providers.Registration{
	Type: "fireworks",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for Fireworks AI.
// Fireworks' inference API is OpenAI-compatible (chat completions, model
// listing, embeddings); model IDs are account-scoped paths such as
// "accounts/fireworks/models/llama-v3p1-8b-instruct" and pass through
// unchanged.
type Provider struct {
	*openai.ChatCompatible
}

var _ core.Provider = (*Provider)(nil)

// New creates a new Fireworks AI provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{openai.NewChatCompatible(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName: "fireworks",
		BaseURL:      providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
	})}
}

// NewWithHTTPClient creates a new Fireworks AI provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	return &Provider{openai.NewChatCompatibleWithHTTPClient(apiKey, httpClient, hooks, openai.CompatibleProviderConfig{
		ProviderName: "fireworks",
		BaseURL:      providers.ResolveBaseURL(baseURL, defaultBaseURL),
	})}
}
