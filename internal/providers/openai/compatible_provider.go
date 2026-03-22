package openai

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

type RequestMutator func(*llmclient.Request)

type CompatibleProviderConfig struct {
	ProviderName   string
	DefaultBaseURL string
	SetHeaders     func(*http.Request, string)
	RequestMutator RequestMutator
}

type CompatibleProvider struct {
	client         *llmclient.Client
	apiKey         string
	providerName   string
	requestMutator RequestMutator
}

func NewCompatibleProvider(apiKey string, opts providers.ProviderOptions, cfg CompatibleProviderConfig) *CompatibleProvider {
	p := &CompatibleProvider{
		apiKey:         apiKey,
		providerName:   cfg.ProviderName,
		requestMutator: cfg.RequestMutator,
	}
	clientCfg := llmclient.Config{
		ProviderName:   cfg.ProviderName,
		BaseURL:        cfg.DefaultBaseURL,
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}
	p.client = llmclient.New(clientCfg, func(req *http.Request) {
		if cfg.SetHeaders != nil {
			cfg.SetHeaders(req, apiKey)
		}
	})
	return p
}

func NewCompatibleProviderWithHTTPClient(apiKey string, httpClient *http.Client, hooks llmclient.Hooks, cfg CompatibleProviderConfig) *CompatibleProvider {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	p := &CompatibleProvider{
		apiKey:         apiKey,
		providerName:   cfg.ProviderName,
		requestMutator: cfg.RequestMutator,
	}
	clientCfg := llmclient.DefaultConfig(cfg.ProviderName, cfg.DefaultBaseURL)
	clientCfg.Hooks = hooks
	p.client = llmclient.NewWithHTTPClient(httpClient, clientCfg, func(req *http.Request) {
		if cfg.SetHeaders != nil {
			cfg.SetHeaders(req, apiKey)
		}
	})
	return p
}

func (p *CompatibleProvider) SetBaseURL(url string) {
	p.client.SetBaseURL(url)
}

func (p *CompatibleProvider) SetRequestMutator(mutator RequestMutator) {
	p.requestMutator = mutator
}

func (p *CompatibleProvider) prepareRequest(req llmclient.Request) llmclient.Request {
	if p.requestMutator != nil {
		p.requestMutator(&req)
	}
	return req
}

func (p *CompatibleProvider) Do(ctx context.Context, req llmclient.Request, result interface{}) error {
	return p.client.Do(ctx, p.prepareRequest(req), result)
}

func (p *CompatibleProvider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("chat request is required", nil)
	}
	var resp core.ChatResponse
	body, err := chatRequestBody(req)
	if err != nil {
		return nil, err
	}
	err = p.Do(ctx, llmclient.Request{
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

func (p *CompatibleProvider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("chat request is required", nil)
	}
	streamReq := req.WithStreaming()
	body, err := chatRequestBody(streamReq)
	if err != nil {
		return nil, err
	}
	return p.client.DoStream(ctx, p.prepareRequest(llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     body,
	}))
}

func (p *CompatibleProvider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	var resp core.ModelsResponse
	err := p.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/models",
	}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (p *CompatibleProvider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("responses request is required", nil)
	}
	var resp core.ResponsesResponse
	err := p.Do(ctx, llmclient.Request{
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

func (p *CompatibleProvider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("responses request is required", nil)
	}
	stream, err := p.client.DoStream(ctx, p.prepareRequest(llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/responses",
		Body:     req.WithStreaming(),
	}))
	if err != nil {
		return nil, err
	}
	return providers.EnsureResponsesDone(stream), nil
}

func (p *CompatibleProvider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("embedding request is required", nil)
	}
	var resp core.EmbeddingResponse
	err := p.Do(ctx, llmclient.Request{
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

func (p *CompatibleProvider) Passthrough(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("passthrough request is required", nil)
	}

	resp, err := p.client.DoPassthrough(ctx, p.prepareRequest(llmclient.Request{
		Method:        req.Method,
		Endpoint:      providers.PassthroughEndpoint(req.Endpoint),
		RawBodyReader: req.Body,
		Headers:       req.Headers,
	}))
	if err != nil {
		return nil, err
	}

	return &core.PassthroughResponse{
		StatusCode: resp.StatusCode,
		Headers:    providers.CloneHTTPHeaders(resp.Header),
		Body:       resp.Body,
	}, nil
}

func (p *CompatibleProvider) CreateBatch(ctx context.Context, req *core.BatchRequest) (*core.BatchResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("batch request is required", nil)
	}
	var resp core.BatchResponse
	err := p.Do(ctx, llmclient.Request{
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

func (p *CompatibleProvider) GetBatch(ctx context.Context, id string) (*core.BatchResponse, error) {
	var resp core.BatchResponse
	err := p.Do(ctx, llmclient.Request{
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

func (p *CompatibleProvider) ListBatches(ctx context.Context, limit int, after string) (*core.BatchListResponse, error) {
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
	err := p.Do(ctx, llmclient.Request{
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

func (p *CompatibleProvider) CancelBatch(ctx context.Context, id string) (*core.BatchResponse, error) {
	var resp core.BatchResponse
	err := p.Do(ctx, llmclient.Request{
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

func (p *CompatibleProvider) GetBatchResults(ctx context.Context, id string) (*core.BatchResultsResponse, error) {
	return providers.FetchBatchResultsFromOutputFileWithPreparer(ctx, p.client, p.providerName, id, p.prepareRequest)
}

func (p *CompatibleProvider) CreateFile(ctx context.Context, req *core.FileCreateRequest) (*core.FileObject, error) {
	resp, err := providers.CreateOpenAICompatibleFileWithPreparer(ctx, p.client, req, p.prepareRequest)
	if err != nil {
		return nil, err
	}
	resp.Provider = p.providerName
	return resp, nil
}

func (p *CompatibleProvider) ListFiles(ctx context.Context, purpose string, limit int, after string) (*core.FileListResponse, error) {
	resp, err := providers.ListOpenAICompatibleFilesWithPreparer(ctx, p.client, purpose, limit, after, p.prepareRequest)
	if err != nil {
		return nil, err
	}
	for i := range resp.Data {
		resp.Data[i].Provider = p.providerName
	}
	return resp, nil
}

func (p *CompatibleProvider) GetFile(ctx context.Context, id string) (*core.FileObject, error) {
	resp, err := providers.GetOpenAICompatibleFileWithPreparer(ctx, p.client, id, p.prepareRequest)
	if err != nil {
		return nil, err
	}
	resp.Provider = p.providerName
	return resp, nil
}

func (p *CompatibleProvider) DeleteFile(ctx context.Context, id string) (*core.FileDeleteResponse, error) {
	return providers.DeleteOpenAICompatibleFileWithPreparer(ctx, p.client, id, p.prepareRequest)
}

func (p *CompatibleProvider) GetFileContent(ctx context.Context, id string) (*core.FileContentResponse, error) {
	return providers.GetOpenAICompatibleFileContentWithPreparer(ctx, p.client, id, p.prepareRequest)
}
