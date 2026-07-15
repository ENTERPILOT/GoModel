// Package meta provides Meta Model API integration for the LLM gateway.
package meta

import (
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://api.meta.ai/v1"

// Registration provides factory registration for the Meta Model API provider.
var Registration = providers.Registration{
	Type: "meta",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for the Meta Model API
// (Muse Spark models). The API is OpenAI-compatible (chat completions, model
// listing, streaming, tool calling) with bearer authentication.
type Provider struct {
	*openai.ChatCompatible
}

var _ core.Provider = (*Provider)(nil)

// New creates a new Meta Model API provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{openai.NewChatCompatible(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName: "meta",
		BaseURL:      providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
	})}
}

// NewWithHTTPClient creates a new Meta Model API provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	return &Provider{openai.NewChatCompatibleWithHTTPClient(apiKey, httpClient, hooks, openai.CompatibleProviderConfig{
		ProviderName: "meta",
		BaseURL:      providers.ResolveBaseURL(baseURL, defaultBaseURL),
	})}
}
