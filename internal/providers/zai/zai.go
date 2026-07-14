// Package zai provides Z.ai API integration for the LLM gateway.
package zai

import (
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
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
// keys is retained to inject auth on the GLM-Realtime websocket target.
type Provider struct {
	*openai.ChatCompatible
	keys *providers.Keyring
}

var _ core.Provider = (*Provider)(nil)

// New creates a new Z.ai provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{
		ChatCompatible: openai.NewChatCompatible(cfg.APIKey, opts, openai.CompatibleProviderConfig{
			ProviderName: "zai",
			BaseURL:      providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
		}),
		keys: opts.Keyring(cfg.APIKey),
	}
}

// NewWithHTTPClient creates a new Z.ai provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	return &Provider{
		ChatCompatible: openai.NewChatCompatibleWithHTTPClient(apiKey, httpClient, hooks, openai.CompatibleProviderConfig{
			ProviderName: "zai",
			BaseURL:      providers.ResolveBaseURL(baseURL, defaultBaseURL),
		}),
		keys: providers.NewKeyring(apiKey),
	}
}
