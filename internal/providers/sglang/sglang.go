// Package sglang provides SGLang OpenAI-compatible API integration for the LLM gateway.
package sglang

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

const defaultBaseURL = "http://localhost:30000/v1"

// Registration provides factory registration for the SGLang provider.
var Registration = providers.Registration{
	Type:                        "sglang",
	New:                         New,
	PassthroughSemanticEnricher: passthroughSemanticEnricher,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL:  defaultBaseURL,
		AllowAPIKeyless: true,
	},
}

// Provider implements the OpenAI-compatible SGLang surface explicitly and
// keeps a root client for SGLang-native endpoints such as /generate.
type Provider struct {
	compatible *openai.CompatibleProvider
	rootClient *llmclient.Client
}

var _ core.Provider = (*Provider)(nil)
var _ core.PassthroughProvider = (*Provider)(nil)

// New creates a new SGLang provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	baseURL := providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL)
	keys := opts.Keyring(cfg.APIKey)
	opts.Keys = keys
	return &Provider{
		compatible: openai.NewCompatibleProvider(cfg.APIKey, opts, openai.CompatibleProviderConfig{
			ProviderName: "sglang",
			BaseURL:      baseURL,
			SetHeaders:   setHeaders,
		}),
		rootClient: llmclient.New(llmclient.Config{
			ProviderName:   "sglang",
			BaseURL:        passthroughBaseURL(baseURL),
			Retry:          opts.Resilience.Retry,
			Hooks:          opts.Hooks,
			CircuitBreaker: opts.Resilience.CircuitBreaker,
		}, func(req *http.Request) {
			setHeaders(req, keys.NextForContext(req.Context()))
		}),
	}
}

// NewWithHTTPClient creates a new SGLang provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	resolvedBaseURL := providers.ResolveBaseURL(baseURL, defaultBaseURL)
	rootClientCfg := llmclient.DefaultConfig("sglang", passthroughBaseURL(resolvedBaseURL))
	rootClientCfg.Hooks = hooks
	return &Provider{
		compatible: openai.NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, openai.CompatibleProviderConfig{
			ProviderName: "sglang",
			BaseURL:      resolvedBaseURL,
			SetHeaders:   setHeaders,
		}),
		rootClient: llmclient.NewWithHTTPClient(httpClient, rootClientCfg, func(req *http.Request) {
			setHeaders(req, apiKey)
		}),
	}
}

// SetBaseURL updates both the OpenAI-compatible and native endpoint clients.
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

// ChatCompletion sends a chat completion request to SGLang.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return p.compatible.ChatCompletion(ctx, req)
}

// StreamChatCompletion streams a chat completion request from SGLang.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return p.compatible.StreamChatCompletion(ctx, req)
}

// ListModels retrieves the models served by SGLang.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	return p.compatible.ListModels(ctx)
}

// Responses sends an OpenAI Responses API request to SGLang.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return p.compatible.Responses(ctx, req)
}

// StreamResponses streams an OpenAI Responses API request from SGLang.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return p.compatible.StreamResponses(ctx, req)
}

// Embeddings sends an embeddings request to SGLang.
func (p *Provider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return p.compatible.Embeddings(ctx, req)
}

// Passthrough routes opaque requests to either SGLang's /v1 API or its native root API.
func (p *Provider) Passthrough(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("passthrough request is required", nil)
	}
	endpoint := providers.PassthroughEndpoint(req.Endpoint)
	if usesV1PassthroughBase(endpoint) {
		return p.compatible.Passthrough(ctx, req)
	}

	resp, err := p.rootClient.DoPassthrough(ctx, llmclient.Request{
		Method:        req.Method,
		Endpoint:      endpoint,
		Operation:     req.Operation,
		Model:         req.Model,
		Stream:        req.Stream,
		RawBodyReader: req.Body,
		Headers:       req.Headers,
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

func passthroughBaseURL(baseURL string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if before, ok := strings.CutSuffix(trimmed, "/v1"); ok {
		return before
	}
	return trimmed
}

func usesV1PassthroughBase(endpoint string) bool {
	endpoint = providers.PassthroughEndpoint(endpoint)
	endpoint, _, _ = strings.Cut(endpoint, "?")
	if strings.HasPrefix(endpoint, "/v1/") {
		return false
	}

	v1Prefixes := []string{
		"/models",
		"/chat/completions",
		"/responses",
		"/completions",
		"/embeddings",
		"/rerank",
		"/tokenize",
		"/audio",
		"/files",
		"/batches",
	}
	for _, prefix := range v1Prefixes {
		if endpoint == prefix || strings.HasPrefix(endpoint, prefix+"/") {
			return true
		}
	}
	return false
}
