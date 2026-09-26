// Package kimicode provides Kimi Code API integration for the LLM gateway.
//
// The "kimicode" provider routes to Kimi Code's OpenAI-compatible API: chat
// completions, model listing, embeddings, and passthrough go through the
// shared chat-centric adapter, while the Responses API is served natively by
// the upstream /responses endpoint. Kimi Code retains no responses, so
// previous_response_id is rejected with an invalid-request error and
// store=true is pinned to false.
package kimicode

import (
	"context"
	"io"
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://api.kimi.com/coding/v1"

// Registration provides factory registration for the Kimi Code provider.
var Registration = providers.Registration{
	Type: "kimicode",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for Kimi Code. Kimi Code is
// OpenAI-compatible, so most transport goes through the shared chat-centric
// adapter: chat completions, model listing, embeddings, and passthrough are
// exposed via the embedded *openai.ChatCompatible. The Responses API is
// forwarded natively to the upstream /responses endpoint (rejecting
// previous_response_id and pinning store to false — see Responses below).
type Provider struct {
	*openai.ChatCompatible
	responses *openai.CompatibleProvider
}

var _ core.Provider = (*Provider)(nil)

// New creates a new Kimi Code provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	baseURL := providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL)
	return &Provider{
		ChatCompatible: openai.NewChatCompatible(cfg.APIKey, opts, compatibleConfig(baseURL)),
		responses:      openai.NewCompatibleProvider(cfg.APIKey, opts, compatibleConfig(baseURL)),
	}
}

// compatibleConfig is the shared OpenAI-compatible configuration for both
// adapter instances. SetHeaders defaults to plain Bearer auth because
// NewCompatibleProvider (unlike NewChatCompatible) applies no default.
func compatibleConfig(baseURL string) openai.CompatibleProviderConfig {
	return openai.CompatibleProviderConfig{
		ProviderName: "kimicode",
		BaseURL:      baseURL,
		SetHeaders: func(req *http.Request, apiKey string) {
			providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{AuthScheme: "Bearer "})
		},
	}
}

// SetBaseURL overrides the upstream endpoint for both the chat-centric
// adapter and the native Responses adapter.
func (p *Provider) SetBaseURL(url string) {
	p.ChatCompatible.SetBaseURL(url)
	p.responses.SetBaseURL(url)
}

// Responses serves the Responses API natively through the upstream /responses
// endpoint. Kimi Code retains no responses, so a non-empty
// previous_response_id is rejected with an invalid-request error before any
// upstream call; store=true is pinned to false by adaptResponsesRequest.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	if err := rejectPreviousResponseID(req); err != nil {
		return nil, err
	}
	return p.responses.Responses(ctx, adaptResponsesRequest(req))
}

// StreamResponses forwards the request to the upstream /responses endpoint
// with stream enabled, returning its Responses SSE stream. Like Responses, it
// rejects a non-empty previous_response_id before any upstream call.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	if err := rejectPreviousResponseID(req); err != nil {
		return nil, err
	}
	return p.responses.StreamResponses(ctx, adaptResponsesRequest(req))
}

// rejectPreviousResponseID fails requests chaining from earlier state:
// Kimi Code does not retain responses, so neither a previous response ID nor
// a gateway-local conversation can be resolved upstream, and answering
// statelessly would silently drop the conversation context the caller
// expects. Requests whose state the gateway already expanded (both fields
// cleared) pass through.
func rejectPreviousResponseID(req *core.ResponsesRequest) error {
	if req == nil {
		return nil
	}
	if req.Conversation != nil {
		return core.NewInvalidRequestError(
			"kimicode does not retain responses: conversation is not supported", nil)
	}
	if req.PreviousResponseID == "" {
		return nil
	}
	return core.NewInvalidRequestError(
		"kimicode does not retain responses: previous_response_id is not supported", nil)
}

// adaptResponsesRequest pins store to false: the service retains no
// responses, so store=true fails upstream with a 400 (Postel's law — adapt
// instead of failing). previous_response_id is not adapted here;
// rejectPreviousResponseID rejects it instead.
func adaptResponsesRequest(req *core.ResponsesRequest) *core.ResponsesRequest {
	if req == nil {
		return nil
	}
	if req.Store == nil || !*req.Store {
		return req
	}
	cp := *req
	disabled := false
	cp.Store = &disabled
	return &cp
}
