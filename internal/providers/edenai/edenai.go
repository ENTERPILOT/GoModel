// Package edenai provides Eden AI API integration for the LLM gateway.
//
// Eden AI is a multi-provider gateway exposing an OpenAI-compatible API at
// https://api.edenai.run/v3. Chat completions, streaming, model listing,
// embeddings, and passthrough all go through the shared OpenAI-compatible
// transport, and model IDs use provider/model notation
// ("openai/gpt-4", "deepinfra/inclusionAI/Ling-3.0-flash-VL") which is
// forwarded unchanged.
//
// Eden also serves a /responses route, but it is not the OpenAI Responses
// API: it takes Eden-specific inputs (routing, router_candidates, fallbacks)
// and returns its own response object. Forwarding a GoModel Responses request
// there would hand the client a body it cannot parse, so Responses and
// StreamResponses are translated through chat completions instead and Eden's
// /responses route is never reached.
//
// The provider composes an unexported *openai.CompatibleProvider rather than
// embedding *openai.ChatCompatible. Composition is required for two reasons:
// ListModels needs the raw transport (CompatibleProvider.Do) to decode Eden's
// catalog metadata, and explicit delegation keeps the surface to exactly what
// Eden implements — Go embedding cannot subtract the batch, file, and audio
// methods the router discovers by interface assertion.
package edenai

import (
	"context"
	"io"
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const (
	defaultBaseURL = "https://api.edenai.run/v3"
	providerType   = "edenai"
)

// Registration provides factory registration for the Eden AI provider.
var Registration = providers.Registration{
	Type:                        providerType,
	New:                         New,
	PassthroughSemanticEnricher: passthroughSemanticEnricher,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for Eden AI. Eden
// authenticates with a plain bearer token and exposes an OpenAI-shaped chat,
// models, and embeddings surface. Eden-only request fields such as routing
// and fallbacks reach the upstream unchanged through core.ChatRequest's
// unknown-field passthrough, so no request adaptation is needed.
type Provider struct {
	compat *openai.CompatibleProvider
}

var (
	_ core.Provider            = (*Provider)(nil)
	_ core.PassthroughProvider = (*Provider)(nil)
)

// New creates a new Eden AI provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{compat: openai.NewCompatibleProvider(cfg.APIKey, opts, compatibleConfig(
		providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
	))}
}

// NewWithHTTPClient creates a new Eden AI provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
//
// The signature matches every other chat-compatible provider on main:
// (apiKey, baseURL, httpClient, hooks).
func NewWithHTTPClient(apiKey string, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	return &Provider{compat: openai.NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, compatibleConfig(
		providers.ResolveBaseURL(baseURL, defaultBaseURL),
	))}
}

// compatibleConfig returns the shared OpenAI-compatible transport settings for Eden AI.
func compatibleConfig(baseURL string) openai.CompatibleProviderConfig {
	return openai.CompatibleProviderConfig{
		ProviderName: providerType,
		BaseURL:      baseURL,
		SetHeaders:   setHeaders,
	}
}

// setHeaders applies Eden AI's bearer-token authentication. CompatibleProvider
// sends no credential when SetHeaders is nil (unlike ChatCompatible, which
// defaults to bearer), so this must stay wired up.
func setHeaders(req *http.Request, apiKey string) {
	providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{AuthScheme: "Bearer "})
}

// SetBaseURL changes the Eden AI API base URL.
func (p *Provider) SetBaseURL(baseURL string) {
	p.compat.SetBaseURL(baseURL)
}

// GetBaseURL returns the provider's current base URL.
func (p *Provider) GetBaseURL() string {
	return p.compat.GetBaseURL()
}

// ChatCompletion sends a chat completion request to Eden AI and normalizes
// the Eden-specific response members (see normalizeChatResponse).
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	resp, err := p.compat.ChatCompletion(ctx, req)
	if err != nil {
		return nil, err
	}
	normalizeChatResponse(resp)
	return resp, nil
}

// StreamChatCompletion sends a streaming chat completion request to Eden AI.
//
// The stream is forwarded verbatim, so unlike ChatCompletion there is no
// opportunity to relocate Eden's root-level cost before the usage pipeline
// sees it. The shared stream observer harvests a root-level "cost" from the
// chunk carrying usage (see usage.copyRootLevelCost), which covers Eden
// reporting cost the same way it does on the non-streaming response. Whether
// Eden actually emits cost on streamed chunks is not established by its
// published contract; when it does not, usage falls back to the per-model
// pricing discovered from /models.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return p.compat.StreamChatCompletion(ctx, req)
}

// Responses translates an OpenAI Responses request through Eden chat
// completions. Eden's native /responses route is a different API and is
// deliberately never called.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return providers.ResponsesViaChat(ctx, p, req)
}

// StreamResponses translates a streaming Responses request through Eden chat
// completions, for the same reason as Responses.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return providers.StreamResponsesViaChat(ctx, p, req, providerType)
}

// Embeddings sends an embeddings request to Eden AI. Eden's /embeddings route
// is OpenAI-compatible and takes the same provider/model IDs.
func (p *Provider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	resp, err := p.compat.Embeddings(ctx, req)
	if err != nil {
		return nil, err
	}
	normalizeEmbeddingResponse(resp)
	return resp, nil
}

// Passthrough forwards an opaque request to Eden AI.
func (p *Provider) Passthrough(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	return p.compat.Passthrough(ctx, req)
}
