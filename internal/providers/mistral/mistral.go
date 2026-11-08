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
	"net/http"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
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
// goes through the shared chat-centric adapter; ChatCompletion and
// StreamChatCompletion are overridden only to flatten Mistral's thinking
// chunk lists into the standard content + reasoning_content shape.
type Provider struct {
	*openai.ChatCompatible
}

var _ core.Provider = (*Provider)(nil)

// New creates a new Mistral provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{openai.NewChatCompatible(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName: "mistral",
		BaseURL:      providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
	})}
}

// ChatCompletion sends a chat completion request to Mistral. The raw body is
// flattened (thinking chunks become reasoning_content) before decoding,
// because the typed core decode rejects Mistral's content chunk arrays.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("chat request is required", nil)
	}
	adapted, err := adaptChatRequest(req)
	if err != nil {
		return nil, err
	}
	adapted, err = narrowAllowedTools(adapted)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if err := p.Compatible().Do(ctx, llmclient.Request{
		Method:    http.MethodPost,
		Endpoint:  "/chat/completions",
		Operation: llmclient.OperationChat,
		Model:     req.Model,
		Body:      adapted,
	}, &raw); err != nil {
		return nil, err
	}
	var resp core.ChatResponse
	if err := json.Unmarshal(flattenThinkingChunks(raw), &resp); err != nil {
		return nil, core.NewProviderError("mistral", http.StatusBadGateway, "failed to decode Mistral chat completion response", err)
	}
	core.EnsureModel(&resp.Model, req.Model)
	return &resp, nil
}

// StreamChatCompletion returns Mistral's SSE stream wrapped so that every
// chunk whose delta.content is a chunk array is rewritten to the standard
// content + reasoning_content delta shape.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("chat request is required", nil)
	}
	adapted, err := adaptChatRequest(req)
	if err != nil {
		return nil, err
	}
	// The embedded adapter applies the shared allowed_tools narrowing and the
	// SSE/buffered-JSON normalization; the thinking flattening wraps on top.
	stream, err := p.ChatCompatible.StreamChatCompletion(ctx, adapted)
	if err != nil {
		return nil, err
	}
	return newThinkingChunkStream(stream), nil
}

// Responses serves the Responses API through chat-completions translation, so
// it picks up this provider's ChatCompletion override (and its thinking
// flattening).
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return providers.ResponsesViaChat(ctx, p, req, p.ProviderName())
}

// StreamResponses streams a Responses API request through chat-completions
// translation, picking up the StreamChatCompletion override.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return providers.StreamResponsesViaChat(ctx, p, req, p.ProviderName())
}

// narrowAllowedTools mirrors the shared adapter's default policy (see
// CompatibleProviderConfig.ForwardAllowedTools): an allowed_tools choice is
// narrowed to the allowed subset because Mistral does not document honoring
// it. ChatCompletion applies it itself because it bypasses the adapter's own
// request pipeline to decode the raw body.
func narrowAllowedTools(req *core.ChatRequest) (*core.ChatRequest, error) {
	if _, _, ok := core.AllowedToolsChoice(req.ToolChoice); !ok {
		return req, nil
	}
	tools, choice, err := core.NarrowToAllowedTools(req.Tools, req.ToolChoice)
	if err != nil {
		return nil, err
	}
	narrowed := *req
	narrowed.Tools = tools
	narrowed.ToolChoice = choice
	return &narrowed, nil
}
