// Package kimicode provides Kimi Code API integration for the LLM gateway.
//
// The "kimicode" provider routes to Kimi Code's OpenAI-compatible API: chat
// completions, model listing, embeddings, and passthrough go through the
// shared chat-centric adapter, while the Responses API is served natively by
// the upstream /responses endpoint.
package kimicode

import (
	"context"
	"io"
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
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
// forwarded natively to the upstream /responses endpoint (see responses.go).
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

// NewWithHTTPClient creates a new Kimi Code provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
//
// The signature is intentionally stable and matches every other chat-compatible
// provider on main: (apiKey, baseURL, httpClient, hooks).
func NewWithHTTPClient(apiKey string, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	resolved := providers.ResolveBaseURL(baseURL, defaultBaseURL)
	return &Provider{
		ChatCompatible: openai.NewChatCompatibleWithHTTPClient(apiKey, httpClient, hooks, compatibleConfig(resolved)),
		responses:      openai.NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, compatibleConfig(resolved)),
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
// endpoint, with the state the upstream rejects adapted away first.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return p.responses.Responses(ctx, adaptResponsesRequest(req))
}

// StreamResponses forwards the request to the upstream /responses endpoint
// with stream enabled, returning its Responses SSE stream.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return p.responses.StreamResponses(ctx, adaptResponsesRequest(req))
}

// adaptResponsesRequest removes state the Kimi Code endpoint rejects: the
// service retains no responses, so store=true and previous_response_id both
// fail upstream with a 400 (Postel's law — adapt instead of failing).
func adaptResponsesRequest(req *core.ResponsesRequest) *core.ResponsesRequest {
	if req == nil {
		return nil
	}
	if (req.Store == nil || !*req.Store) && req.PreviousResponseID == "" {
		return req
	}
	cp := *req
	if cp.Store != nil && *cp.Store {
		disabled := false
		cp.Store = &disabled
	}
	cp.PreviousResponseID = ""
	return &cp
}
