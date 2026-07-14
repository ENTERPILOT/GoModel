// Package minimax provides MiniMax API integration for the LLM gateway.
package minimax

import (
	"context"
	"io"
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://api.minimax.io/v1"

// defaultTemperature is the fallback temperature for MiniMax.
// MiniMax requires temperature to be in (0.0, 1.0] — zero is not allowed.
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
		ProviderName: "minimax",
		BaseURL:      providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
	})}
}

// NewWithHTTPClient creates a new MiniMax provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	return &Provider{openai.NewChatCompatibleWithHTTPClient(apiKey, httpClient, hooks, openai.CompatibleProviderConfig{
		ProviderName: "minimax",
		BaseURL:      providers.ResolveBaseURL(baseURL, defaultBaseURL),
	})}
}

// clampTemperature returns the request with temperature clamped to (0.0, 1.0].
// MiniMax rejects temperature=0; if zero or negative, defaultTemperature is used.
func clampTemperature(req *core.ChatRequest) *core.ChatRequest {
	if req == nil || req.Temperature == nil || *req.Temperature > 0 {
		return req
	}
	t := defaultTemperature
	cloned := *req
	cloned.Temperature = &t
	return &cloned
}

// ChatCompletion sends a chat completion request to MiniMax.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return p.ChatCompatible.ChatCompletion(ctx, clampTemperature(req))
}

// StreamChatCompletion returns a raw response body for streaming.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return p.ChatCompatible.StreamChatCompletion(ctx, clampTemperature(req))
}

// Responses sends a Responses API request to MiniMax using chat-completions
// translation, dispatched through the clamped ChatCompletion above.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return providers.ResponsesViaChat(ctx, p, req)
}

// StreamResponses streams a Responses API request to MiniMax using
// chat-completions translation, dispatched through the clamped streaming above.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return providers.StreamResponsesViaChat(ctx, p, req, "minimax")
}
