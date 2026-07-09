// Package edenai provides Eden AI integration for the LLM gateway.
package edenai

import (
	"net/http"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
	"gomodel/internal/providers/openai"
)

// Eden AI (https://www.edenai.co) is an EU-hosted, OpenAI-compatible gateway
// that exposes 100+ models from many providers through a single endpoint and
// API key. Models use the "provider/model" naming scheme (e.g.
// "anthropic/claude-sonnet-4-5"); the full id is sent upstream unchanged.
const defaultBaseURL = "https://api.edenai.run/v3"

// Registration provides factory registration for the Eden AI provider.
var Registration = providers.Registration{
	Type:                        "edenai",
	New:                         New,
	PassthroughSemanticEnricher: openai.Registration.PassthroughSemanticEnricher,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for Eden AI. Eden AI's API
// is OpenAI-compatible, so all transport goes through the shared compatible
// provider with no request quirks.
type Provider struct {
	*openai.CompatibleProvider
}

var _ core.Provider = (*Provider)(nil)

// New creates a new Eden AI provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{
		CompatibleProvider: openai.NewCompatibleProvider(cfg.APIKey, opts, compatibleConfig(providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL))),
	}
}

// NewWithHTTPClient creates a new Eden AI provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	return &Provider{
		CompatibleProvider: openai.NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, compatibleConfig(providers.ResolveBaseURL(baseURL, defaultBaseURL))),
	}
}

func compatibleConfig(baseURL string) openai.CompatibleProviderConfig {
	return openai.CompatibleProviderConfig{
		ProviderName: "edenai",
		BaseURL:      baseURL,
		SetHeaders:   setHeaders,
	}
}

func setHeaders(req *http.Request, apiKey string) {
	providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{
		AuthScheme: "Bearer ",
	})
}
