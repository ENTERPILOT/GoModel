// Package githubcopilot routes requests to the GitHub Copilot API, billed
// against a GitHub Copilot subscription instead of a model vendor's API key.
//
// The upstream is OpenAI-compatible but splits its models across endpoints:
// GPT-5 and later answer only on /responses, while Claude, Gemini, and older
// GPT models answer on /chat/completions. The provider learns each model's
// endpoints from GET /models and serves both APIs on every model, translating
// when the model lacks the endpoint the client called.
package githubcopilot

import (
	"context"
	"io"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const (
	providerType = "github-copilot"
	// defaultBaseURL serves the individual Copilot plans. Business and
	// Enterprise seats use the endpoint GitHub assigns the account, set with
	// GITHUB_COPILOT_BASE_URL.
	defaultBaseURL = "https://api.githubcopilot.com"
)

// Registration provides factory registration for the GitHub Copilot provider.
// The "github-copilot" type derives GITHUB_COPILOT_API_KEY,
// GITHUB_COPILOT_BASE_URL, and GITHUB_COPILOT_MODELS by convention.
var Registration = providers.Registration{
	Type: providerType,
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for GitHub Copilot. Chat
// completions, embeddings, and passthrough go through the shared
// chat-centric adapter; ChatCompletion and Responses pick the upstream
// endpoint per model (see endpoints.go), and normalize.go smooths over
// Copilot's departures from the OpenAI wire format.
type Provider struct {
	*openai.ChatCompatible
	routes *modelRoutes
}

var _ core.Provider = (*Provider)(nil)

// New creates a new GitHub Copilot provider. The API key is a GitHub OAuth
// token for an account with Copilot access; it is sent as-is, with no
// exchange for a short-lived Copilot token.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	p := &Provider{routes: &modelRoutes{}}
	p.ChatCompatible = openai.NewChatCompatible(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName:       providerType,
		BaseURL:            providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
		SetHeaders:         setHeaders,
		ChatRequestHeaders: chatHeaders,
		RequestMutator:     addResponsesHeaders,
	})
	return p
}

// ChatCompletion sends the request to /chat/completions, or through the
// Responses API for a model Copilot serves only there.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	if req != nil && p.responsesOnly(ctx, req.Model) {
		return openai.ChatViaResponses(ctx, p.Compatible(), req, providerType)
	}
	return p.chatCompletion(ctx, req)
}

// StreamChatCompletion is ChatCompletion for streaming requests.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	if req != nil && p.responsesOnly(ctx, req.Model) {
		return openai.StreamChatViaResponses(ctx, p.Compatible(), req, providerType)
	}
	return p.ChatCompatible.StreamChatCompletion(ctx, req)
}

// Responses forwards the request to /responses for a model Copilot serves
// there, and translates it to chat completions otherwise.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	if req != nil && p.servesResponses(ctx, req.Model) {
		return p.Compatible().Responses(ctx, req)
	}
	return providers.ResponsesViaChat(ctx, p, req, providerType)
}

// StreamResponses is Responses for streaming requests.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	if req != nil && p.servesResponses(ctx, req.Model) {
		return p.Compatible().StreamResponses(ctx, req)
	}
	return providers.StreamResponsesViaChat(ctx, p, req, providerType)
}
