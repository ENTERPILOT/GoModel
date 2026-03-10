// Package openai provides OpenAI API integration for the LLM gateway.
package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
)

// Registration provides factory registration for the OpenAI provider.
var Registration = providers.Registration{
	Type: "openai",
	New:  New,
}

const (
	defaultBaseURL = "https://api.openai.com/v1"
)

// Provider implements the core.Provider interface for OpenAI
type Provider struct {
	client *llmclient.Client
	apiKey string
}

// New creates a new OpenAI provider.
func New(apiKey string, opts providers.ProviderOptions) core.Provider {
	p := &Provider{apiKey: apiKey}
	cfg := llmclient.Config{
		ProviderName:   "openai",
		BaseURL:        defaultBaseURL,
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}
	p.client = llmclient.New(cfg, p.setHeaders)
	return p
}

// NewWithHTTPClient creates a new OpenAI provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	p := &Provider{apiKey: apiKey}
	cfg := llmclient.DefaultConfig("openai", defaultBaseURL)
	cfg.Hooks = hooks
	p.client = llmclient.NewWithHTTPClient(httpClient, cfg, p.setHeaders)
	return p
}

// SetBaseURL allows configuring a custom base URL for the provider
func (p *Provider) SetBaseURL(url string) {
	p.client.SetBaseURL(url)
}

// setHeaders sets the required headers for OpenAI API requests
func (p *Provider) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	// Forward request ID if present in context using OpenAI's X-Client-Request-Id header.
	// OpenAI requires ASCII-only characters and max 512 bytes, otherwise returns 400.
	if requestID := core.GetRequestID(req.Context()); requestID != "" && isValidClientRequestID(requestID) {
		req.Header.Set("X-Client-Request-Id", requestID)
	}
}

// isValidClientRequestID checks if the request ID is valid for OpenAI's X-Client-Request-Id header.
// OpenAI requires: ASCII characters only, max 512 characters.
func isValidClientRequestID(id string) bool {
	if len(id) > 512 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] > 127 {
			return false
		}
	}
	return true
}

// isOSeriesModel reports whether the model is an OpenAI o-series model
// (o1, o3, o4) that requires max_completion_tokens instead of max_tokens
// and does not support the temperature parameter.
func isOSeriesModel(model string) bool {
	m := strings.ToLower(model)
	// Match o1, o3, o4 families (e.g. o3-mini, o4-mini, o3, o1-preview).
	// Non-reasoning models like gpt-4o start with "gpt-", not "o".
	return len(m) >= 2 && m[0] == 'o' && m[1] >= '0' && m[1] <= '9'
}

// adaptForOSeries rewrites a ChatRequest body for OpenAI o-series models,
// mapping max_tokens -> max_completion_tokens and dropping temperature while
// preserving all unknown top-level JSON fields.
func adaptForOSeries(req *core.ChatRequest) any {
	body, err := json.Marshal(req)
	if err != nil {
		return map[string]any{
			"model":                 req.Model,
			"messages":              req.Messages,
			"tools":                 req.Tools,
			"tool_choice":           req.ToolChoice,
			"parallel_tool_calls":   req.ParallelToolCalls,
			"stream":                req.Stream,
			"stream_options":        req.StreamOptions,
			"reasoning":             req.Reasoning,
			"max_completion_tokens": req.MaxTokens,
		}
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return req
	}
	if maxTokens, ok := raw["max_tokens"]; ok {
		raw["max_completion_tokens"] = maxTokens
		delete(raw, "max_tokens")
	}
	delete(raw, "temperature")
	return raw
}

// chatRequestBody returns the appropriate request body for the model.
// Reasoning models get parameter adaptation; others pass through as-is.
func chatRequestBody(req *core.ChatRequest) any {
	if isOSeriesModel(req.Model) {
		return adaptForOSeries(req)
	}
	return req
}

// ChatCompletion sends a chat completion request to OpenAI
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	var resp core.ChatResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     chatRequestBody(req),
	}, &resp)
	if err != nil {
		return nil, err
	}
	// OpenAI can return assistant tool_calls with finish_reason="stop" instead of
	// "tool_calls". Preserve the upstream finish_reason as-is for API parity.
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return &resp, nil
}

// StreamChatCompletion returns a raw response body for streaming (caller must close).
// OpenAI can emit tool_calls while the final chunk still carries finish_reason="stop";
// this provider forwards the upstream SSE stream unchanged for API parity.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	streamReq := req.WithStreaming()
	return p.client.DoStream(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     chatRequestBody(streamReq),
	})
}

// ListModels retrieves the list of available models from OpenAI
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

// Responses sends a Responses API request to OpenAI
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

// StreamResponses returns a normalized streaming Responses API body.
// The returned io.ReadCloser is wrapped by providers.EnsureResponsesDone, so
// callers must not assume it contains verbatim upstream bytes; the wrapper may
// synthesize a terminal `data: [DONE]` marker on completed streams. Callers
// remain responsible for closing the returned stream.
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

// Embeddings sends an embeddings request to OpenAI
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

// CreateBatch creates a native OpenAI batch job.
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

// GetBatch retrieves a native OpenAI batch job.
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

// ListBatches lists native OpenAI batch jobs.
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

// CancelBatch cancels a native OpenAI batch job.
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

// GetBatchResults fetches OpenAI batch results via the output file API.
func (p *Provider) GetBatchResults(ctx context.Context, id string) (*core.BatchResultsResponse, error) {
	return providers.FetchBatchResultsFromOutputFile(ctx, p.client, "openai", id)
}

// CreateFile uploads a file through OpenAI's /files API.
func (p *Provider) CreateFile(ctx context.Context, req *core.FileCreateRequest) (*core.FileObject, error) {
	resp, err := providers.CreateOpenAICompatibleFile(ctx, p.client, req)
	if err != nil {
		return nil, err
	}
	resp.Provider = "openai"
	return resp, nil
}

// ListFiles lists files through OpenAI's /files API.
func (p *Provider) ListFiles(ctx context.Context, purpose string, limit int, after string) (*core.FileListResponse, error) {
	resp, err := providers.ListOpenAICompatibleFiles(ctx, p.client, purpose, limit, after)
	if err != nil {
		return nil, err
	}
	for i := range resp.Data {
		resp.Data[i].Provider = "openai"
	}
	return resp, nil
}

// GetFile retrieves one file object through OpenAI's /files API.
func (p *Provider) GetFile(ctx context.Context, id string) (*core.FileObject, error) {
	resp, err := providers.GetOpenAICompatibleFile(ctx, p.client, id)
	if err != nil {
		return nil, err
	}
	resp.Provider = "openai"
	return resp, nil
}

// DeleteFile deletes a file object through OpenAI's /files API.
func (p *Provider) DeleteFile(ctx context.Context, id string) (*core.FileDeleteResponse, error) {
	return providers.DeleteOpenAICompatibleFile(ctx, p.client, id)
}

// GetFileContent fetches raw file bytes through OpenAI's /files/{id}/content API.
func (p *Provider) GetFileContent(ctx context.Context, id string) (*core.FileContentResponse, error) {
	return providers.GetOpenAICompatibleFileContent(ctx, p.client, id)
}
