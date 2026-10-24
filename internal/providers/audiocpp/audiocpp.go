// Package audiocpp provides audio.cpp (audiocpp_server) integration for the
// gateway. audio.cpp is a local, audio-only inference server: it serves
// text-to-speech and speech-to-text under OpenAI-shaped /v1/audio paths, and
// has no chat, Responses, or embeddings surface.
package audiocpp

import (
	"context"
	"io"
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

// Registration provides factory registration for the audio.cpp provider.
// audiocpp_server's default port (8080) collides with the gateway's own, so
// the base URL is required rather than defaulted. The server has no
// authentication of its own, so the API key is optional and only useful when a
// reverse proxy in front of it expects a bearer token.
var Registration = providers.Registration{
	Type:                        "audiocpp",
	New:                         New,
	PassthroughSemanticEnricher: passthroughSemanticEnricher,
	Discovery: providers.DiscoveryConfig{
		RequireBaseURL:  true,
		AllowAPIKeyless: true,
	},
}

// Provider implements the audio capability and native passthrough against one
// audiocpp_server. Chat, Responses, and embeddings return
// invalid_request_error because audio.cpp has no such endpoints.
type Provider struct {
	client *llmclient.Client
	keys   *providers.Keyring
}

var (
	_ core.Provider            = (*Provider)(nil)
	_ core.AudioProvider       = (*Provider)(nil)
	_ core.PassthroughProvider = (*Provider)(nil)
)

// New creates an audio.cpp provider. The client is rooted at the server root
// rather than its /v1 prefix: audio.cpp serves /health there, and every other
// endpoint carries its own /v1 prefix. A base URL configured with a trailing
// /v1 (the shape every other local-server provider uses) is accepted and
// trimmed, so both spellings address the same server.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	p := &Provider{keys: opts.Keyring(cfg.APIKey)}
	clientCfg := llmclient.Config{
		ProviderName:   opts.ClientName("audiocpp"),
		BaseURL:        providers.PassthroughBaseURL(cfg.BaseURL),
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}
	p.client = llmclient.NewWithOptionalHTTPClient(opts.HTTPClient, clientCfg, p.setHeaders)
	return p
}

// SetBaseURL allows configuring a custom base URL for the provider.
func (p *Provider) SetBaseURL(url string) {
	p.client.SetBaseURL(providers.PassthroughBaseURL(url))
}

// setHeaders resolves the credential per request rather than capturing it, so
// several configured keys rotate across calls.
func (p *Provider) setHeaders(req *http.Request) {
	providers.SetAuthHeaders(req, p.keys.NextForContext(req.Context()), providers.AuthHeaderConfig{
		AuthScheme:      "Bearer ",
		RequestIDHeader: "X-Request-Id",
		OptionalAPIKey:  true,
	})
}

// ChatCompletion reports that audio.cpp has no chat API.
func (p *Provider) ChatCompletion(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, unsupported("chat completions")
}

// StreamChatCompletion reports that audio.cpp has no chat API.
func (p *Provider) StreamChatCompletion(_ context.Context, _ *core.ChatRequest) (io.ReadCloser, error) {
	return nil, unsupported("chat completions")
}

// Responses reports that audio.cpp has no Responses API.
func (p *Provider) Responses(_ context.Context, _ *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return nil, unsupported("the responses API")
}

// StreamResponses reports that audio.cpp has no Responses API.
func (p *Provider) StreamResponses(_ context.Context, _ *core.ResponsesRequest) (io.ReadCloser, error) {
	return nil, unsupported("the responses API")
}

// Embeddings reports that audio.cpp has no embeddings API.
func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, unsupported("embeddings")
}

func unsupported(surface string) error {
	return core.NewInvalidRequestError("audiocpp does not support "+surface, nil)
}

// Passthrough forwards an audio.cpp-native request. It is what reaches the
// routes the OpenAI-compatible surface cannot express: /health,
// /audio/transcriptions/details, /audio/transcriptions/live,
// /audio/alignments, /audio/voices, and /tasks/*.
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
// audiocpp_server serves everything but /health under /v1, and the gateway
// strips the optional v1 alias before a provider sees the endpoint, so the
// prefix is restored for the paths that live there. An endpoint written with
// the prefix already in place is addressed as given.
func passthroughPath(endpoint string) string {
	path := providers.PassthroughEndpoint(endpoint)
	if providers.UsesV1PassthroughBase(path, v1PassthroughPrefixes) {
		return "/v1" + path
	}
	return path
}

// v1PassthroughPrefixes are the paths audio.cpp serves from its /v1 surface;
// everything else (/health) is a server root path.
var v1PassthroughPrefixes = []string{
	"/audio",
	"/models",
	"/tasks",
	"/ui",
}
