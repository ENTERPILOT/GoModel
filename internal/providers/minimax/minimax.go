// Package minimax provides MiniMax API integration for the LLM gateway.
package minimax

import (
	"context"
	"io"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://api.minimax.io/v1"

// defaultTemperature is the fallback for legacy MiniMax models that reject zero.
const defaultTemperature = 1.0

// Registration provides factory registration for the MiniMax provider.
var Registration = providers.Registration{
	Type: "minimax",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for MiniMax.
type Provider struct {
	*openai.ChatCompatible
}

var _ core.Provider = (*Provider)(nil)

// New creates a new MiniMax provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{openai.NewChatCompatible(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName:     "minimax",
		BaseURL:          providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
		AdaptChatRequest: adaptFlashEffort,
	})}
}

// clampTemperature keeps the legacy fallback for models that reject zero.
// Current M3 models accept temperature in [0, 2], including zero.
func clampTemperature(req *core.ChatRequest) *core.ChatRequest {
	if req == nil || req.Temperature == nil || *req.Temperature > 0 {
		return req
	}
	if strings.EqualFold(req.Model, "MiniMax-M3.1-Flash") || strings.EqualFold(req.Model, "MiniMax-M3") {
		return req
	}
	t := defaultTemperature
	cloned := *req
	cloned.Temperature = &t
	return &cloned
}

// ChatCompletion sends a chat completion request to MiniMax. Reasoning
// models are asked to split their chain of thought out of the answer; the
// reasoning then arrives natively in the reasoning_content member and is
// relayed untouched.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return p.ChatCompatible.ChatCompletion(ctx, adaptChatRequest(clampTemperature(req)))
}

// StreamChatCompletion returns a raw response body for streaming (caller
// must close). On the reasoning models the redundant reasoning_details
// member is stripped from deltas that also carry reasoning_content; every
// other stream is relayed byte for byte.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	adapted := adaptChatRequest(clampTemperature(req))
	stream, err := p.ChatCompatible.StreamChatCompletion(ctx, adapted)
	if err != nil {
		return nil, err
	}
	model := ""
	if req != nil {
		model = req.Model
	}
	return normalizeChatStream(stream, model), nil
}

// Responses sends a Responses API request to MiniMax using chat-completions
// translation, dispatched through the clamped ChatCompletion above.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return providers.ResponsesViaChat(ctx, p, req, "minimax")
}

// StreamResponses streams a Responses API request to MiniMax using
// chat-completions translation, dispatched through the clamped streaming above.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return providers.StreamResponsesViaChat(ctx, p, req, "minimax")
}
