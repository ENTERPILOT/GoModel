// Package deepseek provides DeepSeek API integration for the LLM gateway.
package deepseek

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
)

const defaultBaseURL = "https://api.deepseek.com"

// Registration provides factory registration for the DeepSeek provider.
var Registration = providers.Registration{
	Type: "deepseek",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for DeepSeek.
type Provider struct {
	client *llmclient.Client
	apiKey string
}

var _ core.Provider = (*Provider)(nil)

// New creates a new DeepSeek provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	p := &Provider{apiKey: cfg.APIKey}
	clientCfg := llmclient.Config{
		ProviderName:   "deepseek",
		BaseURL:        providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}
	p.client = llmclient.New(clientCfg, p.setHeaders)
	return p
}

// NewWithHTTPClient creates a new DeepSeek provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	p := &Provider{apiKey: apiKey}
	cfg := llmclient.DefaultConfig("deepseek", providers.ResolveBaseURL(baseURL, defaultBaseURL))
	cfg.Hooks = hooks
	p.client = llmclient.NewWithHTTPClient(httpClient, cfg, p.setHeaders)
	return p
}

// SetBaseURL allows configuring a custom base URL for the provider.
func (p *Provider) SetBaseURL(url string) {
	p.client.SetBaseURL(url)
}

func (p *Provider) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	if requestID := core.GetRequestID(req.Context()); requestID != "" {
		req.Header.Set("X-Request-Id", requestID)
	}
}

// adaptChatRequest rewrites GoModel's common reasoning shape into DeepSeek's
// OpenAI-compatible chat extension. DeepSeek accepts reasoning_effort as a
// top-level string, not "reasoning": {"effort": "..."}.
func adaptChatRequest(req *core.ChatRequest) (any, error) {
	if req == nil || req.Reasoning == nil || strings.TrimSpace(req.Reasoning.Effort) == "" {
		return req, nil
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to marshal deepseek request: "+err.Error(), err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, core.NewInvalidRequestError("failed to decode deepseek request payload: "+err.Error(), err)
	}

	effort, _ := json.Marshal(normalizeReasoningEffort(req.Reasoning.Effort))
	raw["reasoning_effort"] = effort
	delete(raw, "reasoning")
	return raw, nil
}

// normalizeReasoningEffort maps GoModel's OpenAI-style effort levels to the two
// levels DeepSeek V4 accepts ("high" and "max"). "low" and "medium" are mapped
// up to "high" because DeepSeek does not support lower levels; clients that
// want to disable reasoning should omit the field entirely. See
// docs/guides/deepseek.mdx for the user-facing table.
func normalizeReasoningEffort(effort string) string {
	normalized := strings.ToLower(strings.TrimSpace(effort))
	switch normalized {
	case "low", "medium":
		return "high"
	case "xhigh", "max":
		return "max"
	default:
		return normalized
	}
}

// ChatCompletion sends a chat completion request to DeepSeek.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("chat request is required", nil)
	}
	body, err := adaptChatRequest(req)
	if err != nil {
		return nil, err
	}
	var resp core.ChatResponse
	err = p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     body,
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return &resp, nil
}

// StreamChatCompletion returns a raw response body for streaming.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("chat request is required", nil)
	}
	streamReq := req.WithStreaming()
	body, err := adaptChatRequest(streamReq)
	if err != nil {
		return nil, err
	}
	return p.client.DoStream(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     body,
	})
}

// ListModels retrieves the list of available models from DeepSeek.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	var resp core.ModelsResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/models",
	}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// Responses sends a Responses API request to DeepSeek using chat-completions translation.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return providers.ResponsesViaChat(ctx, p, req)
}

// StreamResponses streams a Responses API request to DeepSeek using chat-completions translation.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return providers.StreamResponsesViaChat(ctx, p, req, "deepseek")
}

// Embeddings returns an error because DeepSeek does not expose an embeddings endpoint.
func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("deepseek does not support embeddings", nil)
}
