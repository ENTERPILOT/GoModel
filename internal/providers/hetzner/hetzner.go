// Package hetzner provides Hetzner Inference API integration for the LLM gateway.
//
// The "hetzner" provider routes to Hetzner's experimental OpenAI-compatible
// inference endpoint, so all transport goes through the shared chat-centric
// adapter and model IDs are forwarded unchanged.
//
// Note: Hetzner declares this inference API as experimental. Breaking changes
// may ship without notice and there is no SLA. Hetzner does not document an
// embeddings endpoint; chat completions, model listing, and passthrough are
// the supported surfaces.
package hetzner

import (
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://inference.hetzner.com/api/v1"

// Registration provides factory registration for the Hetzner provider.
var Registration = providers.Registration{
	Type: "hetzner",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for Hetzner. Hetzner is
// OpenAI-compatible, so all transport goes through the shared chat-centric
// adapter: chat completions, model listing, and passthrough are exposed via
// the embedded *openai.ChatCompatible. Hetzner documents no embeddings
// endpoint, so embedding requests fail upstream.
type Provider struct {
	*openai.ChatCompatible
}

var _ core.Provider = (*Provider)(nil)

// New creates a new Hetzner provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{openai.NewChatCompatible(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName: "hetzner",
		BaseURL:      providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
	})}
}

// NewWithHTTPClient creates a new Hetzner provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
//
// The signature is intentionally stable and matches every other chat-compatible
// provider on main: (apiKey, baseURL, httpClient, hooks).
func NewWithHTTPClient(apiKey string, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	return &Provider{openai.NewChatCompatibleWithHTTPClient(apiKey, httpClient, hooks, openai.CompatibleProviderConfig{
		ProviderName: "hetzner",
		BaseURL:      providers.ResolveBaseURL(baseURL, defaultBaseURL),
	})}
}
