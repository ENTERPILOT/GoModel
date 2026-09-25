// Package vllm provides vLLM OpenAI-compatible API integration for the LLM gateway.
package vllm

import (
	"context"
	"io"
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const defaultBaseURL = "http://localhost:8000/v1"

// Registration provides factory registration for the vLLM provider.
var Registration = providers.Registration{
	Type:                        "vllm",
	New:                         New,
	PassthroughSemanticEnricher: passthroughSemanticEnricher,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL:  defaultBaseURL,
		AllowAPIKeyless: true,
	},
}

// Provider implements the core.Provider interface for vLLM.
type Provider struct {
	compatible *openai.CompatibleProvider
	rootClient *llmclient.Client
}

// New creates a new vLLM provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	baseURL := providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL)
	rootBaseURL := providers.PassthroughBaseURL(baseURL)
	return &Provider{
		compatible: openai.NewCompatibleProvider(cfg.APIKey, opts, openai.CompatibleProviderConfig{
			ProviderName:     "vllm",
			BaseURL:          baseURL,
			SetHeaders:       setHeaders,
			AdaptChatRequest: adaptChatRequest,
		}),
		rootClient: llmclient.NewWithOptionalHTTPClient(opts.HTTPClient, llmclient.Config{
			ProviderName:   opts.ClientName("vllm"),
			BaseURL:        rootBaseURL,
			Retry:          opts.Resilience.Retry,
			Hooks:          opts.Hooks,
			CircuitBreaker: opts.Resilience.CircuitBreaker,
		}, func(req *http.Request) {
			setHeaders(req, cfg.APIKey)
		}),
	}
}

// SetBaseURL allows configuring a custom base URL for the provider.
func (p *Provider) SetBaseURL(url string) {
	p.compatible.SetBaseURL(url)
	p.rootClient.SetBaseURL(providers.PassthroughBaseURL(url))
}

func setHeaders(req *http.Request, apiKey string) {
	providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{
		AuthScheme:      "Bearer ",
		RequestIDHeader: "X-Request-Id",
		OptionalAPIKey:  true,
	})
}

// ChatCompletion sends a chat completion request to vLLM.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return p.compatible.ChatCompletion(ctx, req)
}

// StreamChatCompletion returns a raw response body for streaming.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return p.compatible.StreamChatCompletion(ctx, req)
}

// ListModels retrieves the list of available models from vLLM.
// ListModels lists the served models, keeping the max_model_len vLLM reports
// per entry as the context window.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	return p.compatible.ListModelsWithMaxModelLen(ctx)
}

// Responses sends a Responses API request to vLLM.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return p.compatible.Responses(ctx, req)
}

// StreamResponses streams a Responses API request to vLLM.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return p.compatible.StreamResponses(ctx, req)
}

// Embeddings sends an embeddings request to vLLM.
func (p *Provider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return p.compatible.Embeddings(ctx, req)
}

// Passthrough routes an opaque provider-native request to vLLM.
func (p *Provider) Passthrough(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("passthrough request is required", nil)
	}
	endpoint := providers.PassthroughEndpoint(req.Endpoint)
	if !providers.UsesV1PassthroughBase(endpoint, v1PassthroughPrefixes) {
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

// v1PassthroughPrefixes are the paths vllm serves from its
// OpenAI-compatible /v1 surface; everything else goes to its root paths.
var v1PassthroughPrefixes = []string{
	"/models",
	"/chat/completions",
	"/responses",
	"/completions",
	"/embeddings",
	"/messages",
	"/audio",
	"/files",
	"/batches",
}
