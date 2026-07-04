// Package kimi provides Kimi API integration for the LLM gateway.
package kimi

import (
	"net/http"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
	"gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://api.kimi.com/coding/v1"

// Registration provides factory registration for the Kimi provider.
var Registration = providers.Registration{
	Type: "kimi",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for Kimi.
type Provider struct {
	*openai.ChatCompatible
}

var _ core.Provider = (*Provider)(nil)

// New creates a new Kimi provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{openai.NewChatCompatible(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName: "kimi",
		BaseURL:      providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
	})}
}

// NewWithHTTPClient creates a new Kimi provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used. headerOverrides and
// userPathAlias are optional; pass nil and "" to disable header overrides.
func NewWithHTTPClient(apiKey string, baseURL string, httpClient *http.Client, hooks llmclient.Hooks, headerOverrides *providers.HeaderOverridesConfig, userPathAlias string) *Provider {
	return &Provider{openai.NewChatCompatibleWithHTTPClient(apiKey, httpClient, hooks, openai.CompatibleProviderConfig{
		ProviderName:     "kimi",
		BaseURL:          providers.ResolveBaseURL(baseURL, defaultBaseURL),
		HeaderOverrides:  headerOverrides,
		UserPathAlias:    userPathAlias,
	})}
}