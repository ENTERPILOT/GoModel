// Package together provides Together AI integration for the LLM gateway.
package together

import (
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://api.together.ai/v1"

// Registration provides factory registration for the Together AI provider.
var Registration = providers.Registration{
	Type: "together",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements Together AI's OpenAI-compatible API through the shared
// chat-centric adapter: chat completions, embeddings, and passthrough go
// upstream unchanged, and Responses requests are translated through chat
// completions because Together's native /responses beta rejects many of its
// serverless models. Only the model listing differs (see ListModels).
type Provider struct {
	*openai.ChatCompatible
}

var (
	_ core.Provider            = (*Provider)(nil)
	_ core.PassthroughProvider = (*Provider)(nil)
)

// New creates a new Together AI provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{openai.NewChatCompatible(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName: "together",
		BaseURL:      providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
	})}
}
