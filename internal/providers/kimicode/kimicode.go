// Package kimicode provides Kimi Code API integration for the LLM gateway.
//
// The "kimicode" provider routes to Kimi Code's OpenAI-compatible API: chat
// completions, model listing, embeddings, and passthrough go through the
// shared chat-centric adapter, while the Responses API is served natively by
// the upstream /responses endpoint. Kimi Code retains no responses, so
// store=true is pinned to false.
package kimicode

import (
	"context"
	"io"
	"strings"

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
// forwarded natively to the upstream /responses endpoint through the same
// adapter instance.
type Provider struct {
	*openai.ChatCompatible
}

var _ core.Provider = (*Provider)(nil)

// New creates a new Kimi Code provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{openai.NewChatCompatible(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName: "kimicode",
		BaseURL:      providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
	})}
}

// Responses serves the Responses API natively through the upstream /responses
// endpoint. Kimi Code retains no responses, so a non-empty
// previous_response_id is rejected before any upstream call (see
// rejectPreviousResponseID); store=true is pinned to false by
// adaptResponsesRequest.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	if err := rejectPreviousResponseID(req); err != nil {
		return nil, err
	}
	return p.Compatible().Responses(ctx, adaptResponsesRequest(req))
}

// StreamResponses forwards the request to the upstream /responses endpoint
// with stream enabled, returning its Responses SSE stream. Like Responses, it
// rejects a non-empty previous_response_id before any upstream call.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	if err := rejectPreviousResponseID(req); err != nil {
		return nil, err
	}
	return p.Compatible().StreamResponses(ctx, adaptResponsesRequest(req))
}

// rejectPreviousResponseID fails requests chaining from earlier state:
// Kimi Code cannot resolve a previous response ID or a gateway-local
// conversation upstream, and answering statelessly would silently drop the
// conversation context the caller expects. The rejection only fires when the
// gateway has no store to expand the chain with; requests whose state the
// gateway already replayed into input (both fields cleared) pass through.
// The ID check mirrors the gateway and the chat-translation validator, both
// of which treat a whitespace-only ID as empty.
func rejectPreviousResponseID(req *core.ResponsesRequest) error {
	if req == nil {
		return nil
	}
	if req.Conversation != nil {
		return core.NewInvalidRequestError(
			"kimicode does not retain responses: conversation is not supported", nil)
	}
	if strings.TrimSpace(req.PreviousResponseID) == "" {
		return nil
	}
	return core.NewInvalidRequestError(
		"kimicode does not retain responses: previous_response_id is not supported", nil)
}

// adaptResponsesRequest pins store to false: the service retains no
// responses, so store=true fails upstream with a 400 (Postel's law — adapt
// instead of failing). A whitespace-only previous_response_id is treated as
// empty by rejectPreviousResponseID and cleared here, because omitempty does
// not omit a non-empty whitespace string and the upstream cannot resolve it.
func adaptResponsesRequest(req *core.ResponsesRequest) *core.ResponsesRequest {
	if req == nil {
		return nil
	}
	whitespaceID := req.PreviousResponseID != "" && strings.TrimSpace(req.PreviousResponseID) == ""
	if (req.Store == nil || !*req.Store) && !whitespaceID {
		return req
	}
	cp := *req
	if req.Store != nil && *req.Store {
		disabled := false
		cp.Store = &disabled
	}
	if whitespaceID {
		cp.PreviousResponseID = ""
	}
	return &cp
}
