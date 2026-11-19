// Package cline provides Cline API integration for the LLM gateway.
//
// The Cline API (https://docs.cline.bot/api/overview) is the hosted model
// service behind the Cline coding agent's built-in "Cline" and "ClinePass"
// providers. It is OpenAI-compatible and authenticates with an API key from a
// Cline account, so all transport goes through the shared chat-centric
// adapter. Three documented deviations are handled here: buffered responses
// use a legacy envelope unless X-Response-Format asks for the OpenAI shape,
// "stream" defaults to true, and a buffered reply carries neither the object
// type nor usage.total_tokens.
package cline

import (
	"context"
	"net/http"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://api.cline.bot/api/v1"

// responseFormatHeader selects the body shape of a buffered chat completion.
// Without the value "openai" Cline nests the completion under "data" next to
// a "success" flag; streamed responses are OpenAI-shaped either way.
const responseFormatHeader = "X-Response-Format"

// Registration provides factory registration for the Cline provider.
var Registration = providers.Registration{
	Type: "cline",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for the Cline API. Model
// listing, streaming, and passthrough are exposed via the embedded
// *openai.ChatCompatible, and Responses requests are translated through chat
// completions. ChatCompletion is overridden to complete the buffered reply,
// and Embeddings to fail fast with a typed error: Cline documents no
// embeddings endpoint.
type Provider struct {
	*openai.ChatCompatible
}

var _ core.Provider = (*Provider)(nil)

// New creates a new Cline provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{openai.NewChatCompatible(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName:   "cline",
		BaseURL:        providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
		SetHeaders:     setHeaders,
		RequestMutator: explicitNonStreaming,
	})}
}

// setHeaders authenticates the request and asks for OpenAI-shaped buffered
// responses. A passthrough caller's own X-Response-Format is applied after
// this hook and wins.
func setHeaders(req *http.Request, apiKey string) {
	providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{AuthScheme: "Bearer "})
	req.Header.Set(responseFormatHeader, "openai")
}

// explicitNonStreaming spells out "stream": false on a buffered chat request.
// Cline streams unless told otherwise, and the canonical request omits the
// member when it is false, so a buffered call would be answered with SSE.
func explicitNonStreaming(r *llmclient.Request) {
	req, ok := r.Body.(*core.ChatRequest)
	if !ok || req.Stream {
		return
	}
	extra, err := core.MergeUnknownJSONFields(req.ExtraFields, map[string]json.RawMessage{
		"stream": json.RawMessage("false"),
	})
	if err != nil {
		return // the client's own marshal reports the malformed extra fields
	}
	explicit := *req
	explicit.ExtraFields = extra
	r.Body = &explicit
}

// ChatCompletion sends a buffered chat request and fills in what Cline's reply
// leaves out: the object type, and the token total its usage object omits,
// which would otherwise reach clients and the usage log as zero.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	resp, err := p.ChatCompatible.ChatCompletion(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.Object == "" {
		resp.Object = "chat.completion"
	}
	if resp.Usage.TotalTokens == 0 {
		resp.Usage.TotalTokens = resp.Usage.PromptTokens + resp.Usage.CompletionTokens
	}
	return resp, nil
}

// Responses dispatches through this provider's ChatCompletion so the
// translated reply carries the completed usage. Streaming needs no override:
// the usage log and the Responses stream translation derive a missing total.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return providers.ResponsesViaChat(ctx, p, req, p.ProviderName())
}

// Embeddings returns an error because the Cline API does not expose an embeddings endpoint.
func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("cline does not support embeddings", nil)
}
