// Package jev provides TypeSafe's Jev (System One) API integration for the
// gateway, and covers the self-hosted Kev servers that implement the same
// API. System One is a decision API rather than a text-generation one: a
// request carries a state and a map of typed questions (noul, choice, score)
// and the answer is a calibrated probability per question. It has no
// OpenAI-compatible surface, so the gateway reaches it through native
// passthrough at /p/jev/systemone.
package jev

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

const defaultBaseURL = "https://api.typesafe.ai"

// Registration provides factory registration for the Jev provider. The hosted
// TypeSafe API needs a key; a local Kev server has no authentication of its
// own, so a base URL alone configures the provider.
var Registration = providers.Registration{
	Type:                        "jev",
	New:                         New,
	PassthroughSemanticEnricher: passthroughSemanticEnricher,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL:  defaultBaseURL,
		AllowAPIKeyless: true,
	},
}

// Provider implements the model listing and native passthrough against one
// System One server. Chat, Responses, and embeddings return
// invalid_request_error because the API has no such endpoints.
type Provider struct {
	client *llmclient.Client
	keys   *providers.Keyring
}

var (
	_ core.Provider            = (*Provider)(nil)
	_ core.PassthroughProvider = (*Provider)(nil)
)

// New creates a Jev provider. The client is rooted at the API origin, which
// is how TypeSafe's SDKs are configured (https://api.typesafe.ai, with /v1
// added per request); a base URL written with the /v1 suffix every other
// provider here uses is accepted and trimmed, so both spellings address the
// same server.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	p := &Provider{keys: opts.Keyring(cfg.APIKey)}
	clientCfg := llmclient.Config{
		ProviderName:   opts.ClientName("jev"),
		BaseURL:        baseURL(cfg.BaseURL),
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}
	p.client = llmclient.NewWithOptionalHTTPClient(opts.HTTPClient, clientCfg, p.setHeaders)
	return p
}

// SetBaseURL allows configuring a custom base URL for the provider.
func (p *Provider) SetBaseURL(url string) {
	p.client.SetBaseURL(baseURL(url))
}

func baseURL(configured string) string {
	return providers.PassthroughBaseURL(providers.ResolveBaseURL(configured, defaultBaseURL))
}

// setHeaders resolves the credential per request rather than capturing it, so
// several configured keys rotate across calls. A keyless local Kev server
// gets no Authorization header at all.
func (p *Provider) setHeaders(req *http.Request) {
	providers.SetAuthHeaders(req, p.keys.NextForContext(req.Context()), providers.AuthHeaderConfig{
		AuthScheme:      "Bearer ",
		RequestIDHeader: "X-Request-Id",
		OptionalAPIKey:  true,
	})
}

// ChatCompletion reports that System One has no chat API.
func (p *Provider) ChatCompletion(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, unsupported("chat completions")
}

// StreamChatCompletion reports that System One has no chat API.
func (p *Provider) StreamChatCompletion(_ context.Context, _ *core.ChatRequest) (io.ReadCloser, error) {
	return nil, unsupported("chat completions")
}

// Responses reports that System One has no Responses API.
func (p *Provider) Responses(_ context.Context, _ *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return nil, unsupported("the responses API")
}

// StreamResponses reports that System One has no Responses API.
func (p *Provider) StreamResponses(_ context.Context, _ *core.ResponsesRequest) (io.ReadCloser, error) {
	return nil, unsupported("the responses API")
}

// Embeddings reports that System One has no embeddings API.
func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, unsupported("embeddings")
}

func unsupported(surface string) error {
	return core.NewInvalidRequestError("jev does not support "+surface+"; send System One requests to /p/jev/systemone", nil)
}

// Passthrough forwards a System One request as the client wrote it. It is the
// only way to reach the evaluation endpoint, since the request and answer
// shapes have no OpenAI equivalent.
func (p *Provider) Passthrough(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("passthrough request is required", nil)
	}
	resp, err := p.client.DoPassthrough(ctx, llmclient.Request{
		Method:          req.Method,
		Endpoint:        passthroughPath(req.Endpoint),
		Operation:       req.Operation,
		Model:           req.Model,
		Stream:          req.Stream,
		StreamUncertain: req.StreamUncertain,
		RawBodyReader:   req.Body,
		Headers:         req.Headers,
	})
	if err != nil {
		return nil, err
	}
	return &core.PassthroughResponse{
		StatusCode: resp.StatusCode,
		Headers:    providers.CloneHTTPHeaders(resp.Header),
		Body:       resp.Body,
	}, nil
}

// passthroughPath resolves the server path a passthrough endpoint addresses.
// Every System One route lives under /v1, and the gateway strips the optional
// v1 alias before a provider sees the endpoint, so the prefix is restored
// unless the endpoint already carries it.
func passthroughPath(endpoint string) string {
	path := providers.PassthroughEndpoint(endpoint)
	if path == "/v1" || strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "/v1?") {
		return path
	}
	return "/v1" + path
}
