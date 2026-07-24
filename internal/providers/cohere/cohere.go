// Package cohere provides Cohere API v2 integration for the LLM gateway.
package cohere

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

const defaultBaseURL = "https://api.cohere.com"

// Registration provides factory registration for the Cohere provider.
var Registration = providers.Registration{
	Type: "cohere",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements Cohere's native v2 chat and embedding APIs.
type Provider struct {
	client *llmclient.Client
	keys   *providers.Keyring
}

var _ core.Provider = (*Provider)(nil)
var _ core.AudioProvider = (*Provider)(nil)
var _ core.PassthroughProvider = (*Provider)(nil)

// New creates a Cohere provider using the shared resilience and observability settings.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	p := &Provider{keys: opts.Keyring(cfg.APIKey)}
	clientCfg := llmclient.Config{
		ProviderName:   "cohere",
		BaseURL:        providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}
	p.client = llmclient.New(clientCfg, p.setHeaders)
	return p
}

// NewWithHTTPClient creates a Cohere provider with a custom HTTP client.
func NewWithHTTPClient(apiKey, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	p := &Provider{keys: providers.NewKeyring(apiKey)}
	cfg := llmclient.DefaultConfig("cohere", providers.ResolveBaseURL(baseURL, defaultBaseURL))
	cfg.Hooks = hooks
	p.client = llmclient.NewWithHTTPClient(httpClient, cfg, p.setHeaders)
	return p
}

// SetBaseURL changes the Cohere API base URL.
func (p *Provider) SetBaseURL(baseURL string) {
	p.client.SetBaseURL(baseURL)
}

func (p *Provider) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+p.keys.Next())
	req.Header.Set("X-Client-Name", "GoModel")
	if requestID := core.GetRequestID(req.Context()); requestID != "" {
		req.Header.Set("X-Request-Id", requestID)
	}
}

// ListModels returns Cohere's model catalog in OpenAI-compatible form.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	var upstream modelsResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/v1/models?page_size=1000",
	}, &upstream)
	if err != nil {
		return nil, err
	}

	models := make([]core.Model, 0, len(upstream.Models))
	for _, model := range upstream.Models {
		if strings.TrimSpace(model.Name) == "" || !supportedModel(model) {
			continue
		}
		var metadata *core.ModelMetadata
		if model.ContextLength > 0 {
			contextWindow := int(model.ContextLength)
			metadata = &core.ModelMetadata{ContextWindow: &contextWindow}
		}
		models = append(models, core.Model{
			ID:       model.Name,
			Object:   "model",
			OwnedBy:  "cohere",
			Metadata: metadata,
		})
	}
	return &core.ModelsResponse{Object: "list", Data: models}, nil
}

func supportedModel(model modelInfo) bool {
	if len(model.Endpoints) == 0 {
		return true
	}
	for _, endpoint := range model.Endpoints {
		switch strings.ToLower(strings.TrimSpace(endpoint)) {
		case "chat", "embed", "transcriptions":
			return true
		}
	}
	return false
}

// Responses translates the OpenAI Responses API through Cohere chat.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return providers.ResponsesViaChat(ctx, p, req)
}

// StreamResponses translates a streaming OpenAI Responses request through Cohere chat.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return providers.StreamResponsesViaChat(ctx, p, req, "cohere")
}

// Passthrough forwards a Cohere-native request without typed translation.
func (p *Provider) Passthrough(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("passthrough request is required", nil)
	}
	resp, err := p.client.DoPassthrough(ctx, llmclient.Request{
		Method:        req.Method,
		Endpoint:      providers.PassthroughEndpoint(req.Endpoint),
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
