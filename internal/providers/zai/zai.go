// Package zai provides Z.ai API integration for the LLM gateway.
package zai

import (
	"context"
	"io"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://api.z.ai/api/paas/v4"

// Registration provides factory registration for the Z.ai provider.
var Registration = providers.Registration{
	Type:                        "zai",
	New:                         New,
	PassthroughSemanticEnricher: passthroughSemanticEnricher,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
	MCPServers: mcpServers,
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
		ChatCompatible: openai.NewChatCompatible(cfg.APIKey, opts, compatibleConfig(providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL))),
		keys:           opts.Keyring(cfg.APIKey),
	}
}

func compatibleConfig(baseURL string) openai.CompatibleProviderConfig {
	return openai.CompatibleProviderConfig{
		ProviderName:          "zai",
		BaseURL:               baseURL,
		AdaptChatRequest:      adaptChatRequest,
		NormalizeChatResponse: normalizeChatResponse,
	}
}

// StreamChatCompletion returns Z.ai's SSE stream with its own finish_reason
// values mapped onto the OpenAI ones.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("chat request is required", nil)
	}
	stream, err := p.ChatCompatible.StreamChatCompletion(ctx, req)
	if err != nil {
		return nil, err
	}
	return newFinishReasonStream(stream), nil
}

// StreamResponses streams a Responses API request through chat-completions
// translation, picking up the StreamChatCompletion override.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return providers.StreamResponsesViaChat(ctx, p, req, p.ProviderName())
}
