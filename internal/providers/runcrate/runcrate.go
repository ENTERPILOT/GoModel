// Package runcrate provides Runcrate integration for the LLM gateway.
package runcrate

import (
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://api.runcrate.ai/v1"

// Registration provides factory registration for the Runcrate provider.
var Registration = providers.Registration{
	Type: "runcrate",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements Runcrate's OpenAI-compatible API through the shared
// chat-centric adapter: chat completions, embeddings, and passthrough go
// upstream unchanged, and Responses requests are translated through chat
// completions. Only the model listing differs (see ListModels).
type Provider struct {
	*openai.ChatCompatible
}

var (
	_ core.Provider            = (*Provider)(nil)
	_ core.PassthroughProvider = (*Provider)(nil)
)

// New creates a new Runcrate provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{openai.NewChatCompatible(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName: "runcrate",
		BaseURL:      providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
	})}
}
