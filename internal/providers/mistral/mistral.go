// Package mistral provides Mistral AI (La Plateforme, https://api.mistral.ai)
// integration for the LLM gateway.
//
// Mistral's Chat Completions API is OpenAI-compatible, so chat, models,
// embeddings, and passthrough go through the shared chat-centric adapter, and
// the Responses API is translated through chat completions. One wire quirk
// needs provider-local handling: on reasoning models (Mistral Large 4,
// Mistral Small 4, and hosted reasoning models such as Z.ai GLM) the assistant
// message content is not a plain string but a list of typed chunks —
// {"type":"thinking","thinking":[{"type":"text","text":...}]} for the reasoning
// trace and {"type":"text","text":...} for the answer — in both the buffered
// response (choices[].message.content) and the stream (choices[].delta.content).
// The core chat types reject that shape, and clients expect the standard
// split, so this package flattens the chunk lists into a string content plus a
// reasoning_content member (see thinking.go) on both paths.
package mistral

import (
	"context"
	"io"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://api.mistral.ai/v1"

// Registration provides factory registration for the Mistral provider.
var Registration = providers.Registration{
	Type: "mistral",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for Mistral AI. Transport
// and the request dialect go through the shared chat-centric adapter's hooks;
// StreamChatCompletion is overridden only to wrap the stream that flattens
// Mistral's thinking chunk lists, and the Responses methods to keep replayed
// reasoning in the translated chat request.
type Provider struct {
	*openai.ChatCompatible
}

var _ core.Provider = (*Provider)(nil)

// New creates a new Mistral provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{openai.NewChatCompatible(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName:          "mistral",
		BaseURL:               providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
		AdaptChatRequest:      adaptChatRequest,
		RequestMutator:        adaptChatWire,
		NormalizeChatResponse: normalizeChatResponse,
	})}
}

// normalizeChatResponse flattens a buffered completion's thinking chunks
// (thinking becomes reasoning_content) before the typed decode, which rejects
// Mistral's content chunk arrays.
func normalizeChatResponse(raw []byte) []byte {
	flattened, _ := flattenThinkingChunks(raw)
	return flattened
}

// StreamChatCompletion returns Mistral's SSE stream wrapped so that every
// chunk whose delta.content is a chunk array is rewritten to the standard
// content + reasoning_content delta shape.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("chat request is required", nil)
	}
	stream, err := p.ChatCompatible.StreamChatCompletion(ctx, req)
	if err != nil {
		return nil, err
	}
	return newThinkingChunkStream(stream), nil
}

// Responses serves a Responses API request through chat-completions
// translation. Mistral wants the reasoning of every earlier assistant turn
// back, so a replayed reasoning item is kept on a turn without a tool call
// too, where the shared conversion drops it by default; adaptChatWire then
// sends it as a thinking chunk.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return providers.ResponsesViaChat(ctx, p, req, p.ProviderName(), providers.ReplayOrdinaryTurnReasoning())
}

// StreamResponses is the streaming counterpart of Responses. It also picks up
// the StreamChatCompletion override.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return providers.StreamResponsesViaChat(ctx, p, req, p.ProviderName(), providers.ReplayOrdinaryTurnReasoning())
}
