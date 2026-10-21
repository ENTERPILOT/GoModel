// Package cheaperinference provides Cheaper Inference integration for the LLM gateway.
//
// Cheaper Inference is an OpenAI-compatible LLM gateway at
// https://api.cheaperinference.com/v1. Chat completions, streaming, model
// listing, and passthrough go through the shared chat-centric adapter, and
// model IDs such as "gpt-5.4-mini" are forwarded unchanged.
package cheaperinference

import (
	"context"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://api.cheaperinference.com/v1"

// Registration provides factory registration for the Cheaper Inference provider.
var Registration = providers.Registration{
	Type:                        "cheaperinference",
	New:                         New,
	PassthroughSemanticEnricher: passthroughSemanticEnricher,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for Cheaper Inference. It
// exposes OpenAI-compatible chat completions, streaming, tool calling, and
// model listing. Cheaper Inference has no embeddings endpoint.
type Provider struct {
	*openai.ChatCompatible
}

var _ core.Provider = (*Provider)(nil)

// New creates a new Cheaper Inference provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{openai.NewChatCompatible(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName: "cheaperinference",
		BaseURL:      providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
	})}
}

// Embeddings returns an error because Cheaper Inference does not expose an embeddings endpoint.
func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("cheaperinference does not support embeddings", nil)
}
