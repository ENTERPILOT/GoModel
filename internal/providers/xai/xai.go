// Package xai provides xAI (Grok) API integration for the LLM gateway.
package xai

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
)

// Registration provides factory registration for the xAI provider.
var Registration = providers.Registration{
	Type: "xai",
	New:  New,
}

const (
	defaultBaseURL = "https://api.x.ai/v1"
)

// Provider implements the core.Provider interface for xAI
type Provider struct {
	client *llmclient.Client
	apiKey string
}

// New creates a new xAI provider.
func New(apiKey string, opts providers.ProviderOptions) core.Provider {
	p := &Provider{apiKey: apiKey}
	cfg := llmclient.Config{
		ProviderName:   "xai",
		BaseURL:        defaultBaseURL,
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}
	p.client = llmclient.New(cfg, p.setHeaders)
	return p
}

// NewWithHTTPClient creates a new xAI provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	p := &Provider{apiKey: apiKey}
	cfg := llmclient.DefaultConfig("xai", defaultBaseURL)
	cfg.Hooks = hooks
	p.client = llmclient.NewWithHTTPClient(httpClient, cfg, p.setHeaders)
	return p
}

// SetBaseURL allows configuring a custom base URL for the provider
func (p *Provider) SetBaseURL(url string) {
	p.client.SetBaseURL(url)
}

// setHeaders sets the required headers for xAI API requests
func (p *Provider) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	// Forward request ID if present in context
	if requestID := core.GetRequestID(req.Context()); requestID != "" {
		req.Header.Set("X-Request-ID", requestID)
	}
}

// ChatCompletion sends a chat completion request to xAI
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	var resp core.ChatResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     req,
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return &resp, nil
}

// StreamChatCompletion returns a raw response body for streaming (caller must close)
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return p.client.DoStream(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     req.WithStreaming(),
	})
}

// ListModels retrieves the list of available models from xAI
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

// Responses sends a Responses API request to xAI
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	var resp core.ResponsesResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/responses",
		Body:     req,
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return &resp, nil
}

// StreamResponses returns a raw response body for streaming Responses API (caller must close)
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	stream, err := p.client.DoStream(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/responses",
		Body:     req.WithStreaming(),
	})
	if err != nil {
		return nil, err
	}

	return providers.EnsureResponsesDone(stream), nil
}

// Embeddings sends an embeddings request to xAI
func (p *Provider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	var resp core.EmbeddingResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/embeddings",
		Body:     req,
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return &resp, nil
}

// CreateBatch creates a native xAI batch job.
func (p *Provider) CreateBatch(ctx context.Context, req *core.BatchRequest) (*core.BatchResponse, error) {
	var resp core.BatchResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/batches",
		Body:     req,
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.ProviderBatchID == "" {
		resp.ProviderBatchID = resp.ID
	}
	return &resp, nil
}

// GetBatch retrieves a native xAI batch job.
func (p *Provider) GetBatch(ctx context.Context, id string) (*core.BatchResponse, error) {
	var resp core.BatchResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/batches/" + url.PathEscape(id),
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.ProviderBatchID == "" {
		resp.ProviderBatchID = resp.ID
	}
	return &resp, nil
}

// ListBatches lists native xAI batch jobs.
func (p *Provider) ListBatches(ctx context.Context, limit int, after string) (*core.BatchListResponse, error) {
	values := url.Values{}
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	if after != "" {
		values.Set("after", after)
	}
	endpoint := "/batches"
	if encoded := values.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}

	var resp core.BatchListResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: endpoint,
	}, &resp)
	if err != nil {
		return nil, err
	}
	for i := range resp.Data {
		if resp.Data[i].ProviderBatchID == "" {
			resp.Data[i].ProviderBatchID = resp.Data[i].ID
		}
	}
	return &resp, nil
}

// CancelBatch cancels a native xAI batch job.
func (p *Provider) CancelBatch(ctx context.Context, id string) (*core.BatchResponse, error) {
	var resp core.BatchResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/batches/" + url.PathEscape(id) + "/cancel",
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.ProviderBatchID == "" {
		resp.ProviderBatchID = resp.ID
	}
	return &resp, nil
}

// GetBatchResults fetches xAI batch results via the output file API.
func (p *Provider) GetBatchResults(ctx context.Context, id string) (*core.BatchResultsResponse, error) {
	return providers.FetchBatchResultsFromOutputFile(ctx, p.client, "xai", id)
}

// CreateFile uploads a file through xAI's OpenAI-compatible /files API.
func (p *Provider) CreateFile(ctx context.Context, req *core.FileCreateRequest) (*core.FileObject, error) {
	resp, err := providers.CreateOpenAICompatibleFile(ctx, p.client, req)
	if err != nil {
		return nil, err
	}
	resp.Provider = "xai"
	return resp, nil
}

// ListFiles lists files through xAI's OpenAI-compatible /files API.
func (p *Provider) ListFiles(ctx context.Context, purpose string, limit int, after string) (*core.FileListResponse, error) {
	resp, err := providers.ListOpenAICompatibleFiles(ctx, p.client, purpose, limit, after)
	if err != nil {
		return nil, err
	}
	for i := range resp.Data {
		resp.Data[i].Provider = "xai"
	}
	return resp, nil
}

// GetFile retrieves one file object through xAI's OpenAI-compatible /files API.
func (p *Provider) GetFile(ctx context.Context, id string) (*core.FileObject, error) {
	resp, err := providers.GetOpenAICompatibleFile(ctx, p.client, id)
	if err != nil {
		return nil, err
	}
	resp.Provider = "xai"
	return resp, nil
}

// DeleteFile deletes a file object through xAI's OpenAI-compatible /files API.
func (p *Provider) DeleteFile(ctx context.Context, id string) (*core.FileDeleteResponse, error) {
	return providers.DeleteOpenAICompatibleFile(ctx, p.client, id)
}

// GetFileContent fetches raw file bytes through xAI's /files/{id}/content API.
func (p *Provider) GetFileContent(ctx context.Context, id string) (*core.FileContentResponse, error) {
	return providers.GetOpenAICompatibleFileContent(ctx, p.client, id)
}
