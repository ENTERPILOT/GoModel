// Package llamacpp provides llama.cpp llama-server OpenAI-compatible API
// integration for the LLM gateway. The same provider type fits LM Studio and
// other plain OpenAI-compatible local servers.
package llamacpp

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

// Registration provides factory registration for the llama.cpp provider.
// llama-server's default port (8080) collides with the gateway's own, so the
// base URL is required rather than defaulted; the API key is optional and only
// needed when llama-server was started with --api-key.
var Registration = providers.Registration{
	Type:                        "llamacpp",
	New:                         New,
	PassthroughSemanticEnricher: passthroughSemanticEnricher,
	Discovery: providers.DiscoveryConfig{
		RequireBaseURL:  true,
		AllowAPIKeyless: true,
	},
}

// Provider implements the core.Provider interface for llama.cpp.
type Provider struct {
	compatible *openai.CompatibleProvider
	rootClient *llmclient.Client
}

var (
	_ core.Provider            = (*Provider)(nil)
	_ core.PassthroughProvider = (*Provider)(nil)
)

// New creates a new llama.cpp provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	return &Provider{
		compatible: openai.NewCompatibleProvider(cfg.APIKey, opts, openai.CompatibleProviderConfig{
			ProviderName: "llamacpp",
			BaseURL:      baseURL,
			SetHeaders:   setHeaders,
		}),
		rootClient: llmclient.New(llmclient.Config{
			ProviderName:   "llamacpp",
			BaseURL:        passthroughBaseURL(baseURL),
			Retry:          opts.Resilience.Retry,
			Hooks:          opts.Hooks,
			CircuitBreaker: opts.Resilience.CircuitBreaker,
		}, func(req *http.Request) {
			setHeaders(req, cfg.APIKey)
		}),
	}
}

// NewWithHTTPClient creates a new llama.cpp provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	resolvedBaseURL := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	rootClientCfg := llmclient.DefaultConfig("llamacpp", passthroughBaseURL(resolvedBaseURL))
	rootClientCfg.Hooks = hooks
	return &Provider{
		compatible: openai.NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, openai.CompatibleProviderConfig{
			ProviderName: "llamacpp",
			BaseURL:      resolvedBaseURL,
			SetHeaders:   setHeaders,
		}),
		rootClient: llmclient.NewWithHTTPClient(httpClient, rootClientCfg, func(req *http.Request) {
			setHeaders(req, apiKey)
		}),
	}
}

// SetBaseURL allows configuring a custom base URL for the provider.
func (p *Provider) SetBaseURL(url string) {
	p.compatible.SetBaseURL(url)
	p.rootClient.SetBaseURL(passthroughBaseURL(url))
}

func setHeaders(req *http.Request, apiKey string) {
	providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{
		AuthScheme:      "Bearer ",
		RequestIDHeader: "X-Request-Id",
		OptionalAPIKey:  true,
	})
}

// ChatCompletion sends a chat completion request to llama-server.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return p.compatible.ChatCompletion(ctx, req)
}

// StreamChatCompletion returns a raw response body for streaming.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return p.compatible.StreamChatCompletion(ctx, req)
}

// ListModels retrieves the list of available models from llama-server.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	return p.compatible.ListModels(ctx)
}

// Responses sends a Responses API request to llama-server.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return p.compatible.Responses(ctx, req)
}

// StreamResponses streams a Responses API request to llama-server.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return p.compatible.StreamResponses(ctx, req)
}

// Embeddings sends an embeddings request to llama-server. The loaded model
// must use a pooling type other than none for llama-server to serve it.
func (p *Provider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return p.compatible.Embeddings(ctx, req)
}

// Passthrough routes an opaque provider-native request to llama-server.
// OpenAI-shaped endpoints go through the /v1 base; llama-server's native
// endpoints (/health, /props, /slots, /rerank, /infill, /tokenize, ...) live
// at the server root.
func (p *Provider) Passthrough(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("passthrough request is required", nil)
	}
	endpoint := providers.PassthroughEndpoint(req.Endpoint)
	if !usesV1PassthroughBase(endpoint) {
		resp, err := p.rootClient.DoPassthrough(ctx, llmclient.Request{
			Method:          req.Method,
			Endpoint:        endpoint,
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
	return p.compatible.Passthrough(ctx, req)
}

func passthroughBaseURL(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if before, ok := strings.CutSuffix(trimmed, "/v1"); ok {
		return before
	}
	return trimmed
}

func usesV1PassthroughBase(endpoint string) bool {
	endpoint = providers.PassthroughEndpoint(endpoint)
	if strings.HasPrefix(endpoint, "/v1/") {
		return false
	}

	v1Prefixes := []string{
		"/models",
		"/chat/completions",
		"/responses",
		"/completions",
		"/embeddings",
	}
	for _, prefix := range v1Prefixes {
		if endpoint == prefix || strings.HasPrefix(endpoint, prefix+"/") {
			return true
		}
	}
	return false
}
