// Package chatgpt routes Responses API traffic to the ChatGPT Codex backend,
// billed against a ChatGPT subscription instead of an OpenAI Platform API key.
//
// The upstream is the endpoint the Codex CLI itself calls when signed in with
// ChatGPT. It speaks a deliberately narrow dialect of the Responses API — a
// strict parameter allowlist, streaming only, no stored responses — so all the
// adaptation lives in request.go and stream.go and never leaks into GoModel's
// OpenAI-compatible surface.
package chatgpt

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

// defaultBaseURL is the Codex backend served to ChatGPT subscribers. Override
// with CHATGPT_BASE_URL.
const defaultBaseURL = "https://chatgpt.com/backend-api/codex"

// defaultModels lists the models a ChatGPT subscription may call through the
// Codex backend. The backend exposes no /models endpoint, so the inventory is
// declared here and overridden with CHATGPT_MODELS when a plan serves a
// different set.
var defaultModels = []string{"gpt-5.6-sol", "gpt-5.5", "gpt-5.4"}

// Registration provides factory registration for the ChatGPT subscription
// provider. The "chatgpt" type derives CHATGPT_API_KEY, CHATGPT_BASE_URL, and
// CHATGPT_MODELS by convention.
var Registration = providers.Registration{
	Type: "chatgpt",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for the ChatGPT Codex
// backend. Only the Responses surface is served: the upstream has no chat
// completions, embeddings, or models endpoint, and advertising them would
// route traffic that can only fail.
type Provider struct {
	client *llmclient.Client
	keys   *providers.Keyring
	models []string
}

var _ core.Provider = (*Provider)(nil)

// New creates a new ChatGPT subscription provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	p := &Provider{
		keys:   opts.Keyring(cfg.APIKey),
		models: resolveModels(opts.Models),
	}
	p.client = llmclient.New(llmclient.Config{
		ProviderName:   "chatgpt",
		BaseURL:        providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}, nil)
	return p
}

// NewWithHTTPClient creates a new ChatGPT provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	clientCfg := llmclient.DefaultConfig("chatgpt", providers.ResolveBaseURL(baseURL, defaultBaseURL))
	clientCfg.Hooks = hooks
	return &Provider{
		client: llmclient.NewWithHTTPClient(httpClient, clientCfg, nil),
		keys:   providers.NewKeyring(apiKey),
		models: resolveModels(nil),
	}
}

// resolveModels returns the operator-configured inventory when present,
// otherwise the shipped default.
func resolveModels(configured []string) []string {
	if len(configured) > 0 {
		return configured
	}
	return defaultModels
}

// SetBaseURL overrides the upstream endpoint.
func (p *Provider) SetBaseURL(url string) { p.client.SetBaseURL(url) }

// ListModels returns the declared inventory. The Codex backend has no /models
// endpoint, so nothing is fetched upstream.
func (p *Provider) ListModels(_ context.Context) (*core.ModelsResponse, error) {
	created := time.Now().Unix()
	models := make([]core.Model, 0, len(p.models))
	for _, id := range p.models {
		models = append(models, core.Model{ID: id, Object: "model", OwnedBy: "chatgpt", Created: created})
	}
	return &core.ModelsResponse{Object: "list", Data: models}, nil
}

// Responses serves a non-streaming request by collapsing the upstream stream:
// the Codex backend rejects `stream: false`, so GoModel streams on the client's
// behalf and returns the final response object.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	stream, err := p.StreamResponses(ctx, req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = stream.Close() }()
	resp, err := collapseResponsesStream(stream)
	if err != nil {
		return nil, err
	}
	core.EnsureModel(&resp.Model, req.Model)
	return resp, nil
}

// StreamResponses forwards the request to the Codex backend and returns its SSE
// stream unchanged.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("responses request is required", nil)
	}
	body, err := newUpstreamRequest(req)
	if err != nil {
		return nil, err
	}
	headers, err := authHeaders(p.keys.NextForContext(ctx))
	if err != nil {
		return nil, err
	}
	stream, err := p.client.DoStream(ctx, llmclient.Request{
		Method:    http.MethodPost,
		Endpoint:  "/responses",
		Operation: llmclient.OperationChat,
		Model:     req.Model,
		Stream:    true,
		Body:      body,
		Headers:   headers,
	})
	if err != nil {
		return nil, err
	}
	return providers.EnsureResponsesDone(stream), nil
}

// ChatCompletion is unsupported: the ChatGPT Codex backend serves only the
// Responses API. Clients reach these models through /v1/responses.
func (p *Provider) ChatCompletion(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, unsupported("chat completions")
}

// StreamChatCompletion is unsupported for the same reason as ChatCompletion.
func (p *Provider) StreamChatCompletion(_ context.Context, _ *core.ChatRequest) (io.ReadCloser, error) {
	return nil, unsupported("chat completions")
}

// Embeddings is unsupported: the Codex backend exposes no embeddings endpoint.
func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, unsupported("embeddings")
}

func unsupported(surface string) error {
	return core.NewInvalidRequestError(
		"chatgpt serves only the Responses API; "+surface+" are not available on a ChatGPT subscription", nil)
}

// authHeaders builds the per-request credential headers. The account ID is
// derived from the token itself, so a subscription needs no extra configuration.
func authHeaders(token string) (http.Header, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, core.NewAuthenticationError("chatgpt",
			"missing ChatGPT access token; set CHATGPT_API_KEY to the token from your Codex sign-in")
	}
	headers := http.Header{"Authorization": []string{"Bearer " + token}}
	if accountID := accountIDFromToken(token); accountID != "" {
		headers.Set("chatgpt-account-id", accountID)
	}
	return headers, nil
}
