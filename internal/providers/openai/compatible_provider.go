package openai

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

type RequestMutator func(*llmclient.Request)

type CompatibleProviderConfig struct {
	ProviderName string
	BaseURL      string
	// HTTPClient overrides the shared default client while retaining the
	// factory-provided retry, circuit-breaker, and observability settings.
	// Providers use this for transport-level concerns such as request signing.
	HTTPClient     *http.Client
	SetHeaders     func(*http.Request, string)
	RequestMutator RequestMutator
	// AdaptChatRequest rewrites the typed chat request before provider
	// dispatch on ChatCompletion and StreamChatCompletion. Providers use it
	// for parameter quirks (e.g. remapping reasoning effort levels) instead
	// of overriding the chat methods, so Responses-via-chat translation picks
	// the adaptation up automatically. It must not mutate its argument;
	// return a shallow copy when changes are needed.
	AdaptChatRequest func(*core.ChatRequest) (*core.ChatRequest, error)
	// ChatRequestHeaders returns extra per-request HTTP headers for
	// ChatCompletion and StreamChatCompletion, derived from the request
	// context and body (e.g. conversation affinity headers). Nil results are
	// ignored.
	ChatRequestHeaders func(context.Context, *core.ChatRequest) http.Header
}

// CompatibleProvider is the single transport engine for every
// OpenAI-compatible upstream. Provider packages must not hand-roll
// llmclient calls for OpenAI-shaped endpoints; they wrap this type in one
// of three ways, chosen by how much of the OpenAI surface the upstream
// actually implements:
//
//   - Embed *ChatCompatible for chat-centric upstreams (chat, models,
//     embeddings, passthrough; Responses translated via chat) — e.g.
//     xiaomi, zai, fireworks, deepseek.
//   - Embed *CompatibleProvider only when the upstream implements the
//     full surface, including audio, files, batches, and native response
//     lifecycle management — e.g. openrouter, azure.
//   - Compose an unexported *CompatibleProvider field and delegate the
//     supported methods explicitly when the upstream implements a partial
//     surface — e.g. groq (no passthrough), xai (no audio/passthrough),
//     ollama (native embeddings, no passthrough). Go embedding cannot
//     subtract methods, and an accidentally inherited method advertises a
//     capability the upstream lacks (the router discovers capabilities by
//     interface assertion).
//
// Provider quirks belong in CompatibleProviderConfig hooks (SetHeaders,
// AdaptChatRequest, ChatRequestHeaders, RequestMutator), not in copies of
// the transport methods.
type CompatibleProvider struct {
	client *llmclient.Client
	// keys resolves the credential for each outbound request. Providers in this
	// package read it directly (see realtime.go) so a websocket dial picks up
	// the same rotation as the HTTP endpoints.
	keys               *providers.Keyring
	providerName       string
	requestMutator     RequestMutator
	adaptChatRequest   func(*core.ChatRequest) (*core.ChatRequest, error)
	chatRequestHeaders func(context.Context, *core.ChatRequest) http.Header
}

func NewCompatibleProvider(apiKey string, opts providers.ProviderOptions, cfg CompatibleProviderConfig) *CompatibleProvider {
	p := &CompatibleProvider{
		keys:               opts.Keyring(apiKey),
		providerName:       cfg.ProviderName,
		requestMutator:     cfg.RequestMutator,
		adaptChatRequest:   cfg.AdaptChatRequest,
		chatRequestHeaders: cfg.ChatRequestHeaders,
	}
	clientCfg := llmclient.Config{
		ProviderName:   cfg.ProviderName,
		BaseURL:        cfg.BaseURL,
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}
	// Resolved per request, not captured: with several keys configured this is
	// what spreads successive calls across them.
	headerSetter := func(req *http.Request) {
		if cfg.SetHeaders != nil {
			cfg.SetHeaders(req, p.keys.Next())
		}
	}
	if cfg.HTTPClient != nil {
		p.client = llmclient.NewWithHTTPClient(cfg.HTTPClient, clientCfg, headerSetter)
	} else {
		p.client = llmclient.New(clientCfg, headerSetter)
	}
	return p
}

func NewCompatibleProviderWithHTTPClient(apiKey string, httpClient *http.Client, hooks llmclient.Hooks, cfg CompatibleProviderConfig) *CompatibleProvider {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	p := &CompatibleProvider{
		keys:               providers.NewKeyring(apiKey),
		providerName:       cfg.ProviderName,
		requestMutator:     cfg.RequestMutator,
		adaptChatRequest:   cfg.AdaptChatRequest,
		chatRequestHeaders: cfg.ChatRequestHeaders,
	}
	clientCfg := llmclient.DefaultConfig(cfg.ProviderName, cfg.BaseURL)
	clientCfg.Hooks = hooks
	p.client = llmclient.NewWithHTTPClient(httpClient, clientCfg, func(req *http.Request) {
		if cfg.SetHeaders != nil {
			cfg.SetHeaders(req, p.keys.Next())
		}
	})
	return p
}

func (p *CompatibleProvider) SetBaseURL(url string) {
	p.client.SetBaseURL(url)
}

// GetBaseURL returns the provider's current base URL. It reads from the client so
// it always reflects SetBaseURL overrides (used to derive the realtime websocket
// target without a separately retained, staleable copy).
func (p *CompatibleProvider) GetBaseURL() string {
	return p.client.BaseURL()
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

func (p *CompatibleProvider) Do(ctx context.Context, req llmclient.Request, result any) error {
	return p.client.Do(ctx, p.prepareRequest(req), result)
}

func (p *CompatibleProvider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("chat request is required", nil)
	}
	adapted, err := p.adaptedChatRequest(req)
	if err != nil {
		return nil, err
	}
	var resp core.ChatResponse
	body, err := chatRequestBody(adapted)
	if err != nil {
		return nil, err
	}
	err = p.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     body,
		Headers:  p.chatHeaders(ctx, adapted),
	}, &resp)
	if err != nil {
		return nil, err
	}
	core.EnsureModel(&resp.Model, req.Model)
	return &resp, nil
}

func (p *CompatibleProvider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("chat request is required", nil)
	}
	adapted, err := p.adaptedChatRequest(req)
	if err != nil {
		return nil, err
	}
	streamReq := adapted.WithStreaming()
	body, err := chatRequestBody(streamReq)
	if err != nil {
		return nil, err
	}
	stream, err := p.client.DoStream(ctx, p.prepareRequest(llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     body,
		Headers:  p.chatHeaders(ctx, streamReq),
	}))
	if err != nil {
		return nil, err
	}
	return providers.EnsureChatCompletionSSE(stream), nil
}

func (p *CompatibleProvider) adaptedChatRequest(req *core.ChatRequest) (*core.ChatRequest, error) {
	if p.adaptChatRequest == nil {
		return req, nil
	}
	return p.adaptChatRequest(req)
}

func (p *CompatibleProvider) chatHeaders(ctx context.Context, req *core.ChatRequest) http.Header {
	if p.chatRequestHeaders == nil {
		return nil
	}
	return p.chatRequestHeaders(ctx, req)
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
	normalizeModelsResponse(&resp)
	return &resp, nil
}

func normalizeModelsResponse(resp *core.ModelsResponse) {
	if resp == nil {
		return
	}
	if strings.TrimSpace(resp.Object) == "" {
		resp.Object = "list"
	}
	for i := range resp.Data {
		if strings.TrimSpace(resp.Data[i].Object) == "" {
			resp.Data[i].Object = "model"
		}
	}
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
	core.EnsureModel(&resp.Model, req.Model)
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

func (p *CompatibleProvider) GetResponse(ctx context.Context, id string, params core.ResponseRetrieveParams) (*core.ResponsesResponse, error) {
	var resp core.ResponsesResponse
	err := p.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: responseRetrieveEndpoint(id, params),
	}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (p *CompatibleProvider) ListResponseInputItems(ctx context.Context, id string, params core.ResponseInputItemsParams) (*core.ResponseInputItemListResponse, error) {
	var resp core.ResponseInputItemListResponse
	err := p.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: responseInputItemsEndpoint(id, params),
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Object == "" {
		resp.Object = "list"
	}
	return &resp, nil
}

func (p *CompatibleProvider) CancelResponse(ctx context.Context, id string) (*core.ResponsesResponse, error) {
	var resp core.ResponsesResponse
	err := p.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/responses/" + url.PathEscape(id) + "/cancel",
	}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (p *CompatibleProvider) DeleteResponse(ctx context.Context, id string) (*core.ResponseDeleteResponse, error) {
	var resp core.ResponseDeleteResponse
	err := p.Do(ctx, llmclient.Request{
		Method:   http.MethodDelete,
		Endpoint: "/responses/" + url.PathEscape(id),
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Object == "" {
		resp.Object = "response"
	}
	return &resp, nil
}

func (p *CompatibleProvider) CountResponseInputTokens(ctx context.Context, req *core.ResponsesRequest) (*core.ResponseInputTokensResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("responses input token request is required", nil)
	}
	var resp core.ResponseInputTokensResponse
	err := p.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/responses/input_tokens",
		Body:     responseInputTokensRequestFromResponses(req),
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Object == "" {
		resp.Object = "response.input_tokens"
	}
	return &resp, nil
}

func (p *CompatibleProvider) CompactResponse(ctx context.Context, req *core.ResponsesRequest) (*core.ResponseCompactResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("responses compact request is required", nil)
	}
	var resp core.ResponseCompactResponse
	err := p.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/responses/compact",
		Body:     responseCompactRequestFromResponses(req),
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Object == "" {
		resp.Object = "response.compaction"
	}
	return &resp, nil
}

func responseInputTokensRequestFromResponses(req *core.ResponsesRequest) *core.ResponseInputTokensRequest {
	utility := req.InputTokensRequest()
	if utility != nil {
		// The provider field is a gateway routing hint, never forwarded upstream.
		utility.Provider = ""
	}
	return utility
}

func responseCompactRequestFromResponses(req *core.ResponsesRequest) *core.ResponseCompactRequest {
	compact := req.CompactRequest()
	if compact != nil {
		compact.Provider = ""
	}
	return compact
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
	core.EnsureModel(&resp.Model, req.Model)
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
	// The OpenAI batch API only accepts file-backed input. Inline requests (the
	// gateway extension used by the Anthropic Message Batches dialect) are
	// materialized into a JSONL input file first, so inline batches route to
	// OpenAI-compatible providers the same as everywhere else. The file has the
	// same lifecycle as a caller-uploaded batch input file.
	uploadedInputFileID := ""
	if req.InputFileID == "" && len(req.Requests) > 0 {
		fileID, err := p.uploadInlineBatchInput(ctx, req.Requests)
		if err != nil {
			return nil, err
		}
		uploadedInputFileID = fileID
		fileBacked := *req
		fileBacked.InputFileID = fileID
		fileBacked.Requests = nil
		req = &fileBacked
	}
	var resp core.BatchResponse
	err := p.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/batches",
		Body:     req,
	}, &resp)
	if err != nil {
		// Best-effort cleanup: the gateway created the input file, so a failed
		// batch creation must not leave the caller's request content orphaned
		// on the provider.
		if uploadedInputFileID != "" {
			if _, cleanupErr := p.DeleteFile(ctx, uploadedInputFileID); cleanupErr != nil {
				slog.Warn("failed to clean up inline batch input file after batch create failure",
					"provider", p.providerName, "file_id", uploadedInputFileID, "error", cleanupErr)
			}
		}
		return nil, err
	}
	providers.EnsureProviderBatchID(&resp)
	return &resp, nil
}

// uploadInlineBatchInput renders inline batch items as the OpenAI batch input
// JSONL and uploads it with purpose "batch".
func (p *CompatibleProvider) uploadInlineBatchInput(ctx context.Context, items []core.BatchRequestItem) (string, error) {
	var buf bytes.Buffer
	for i, item := range items {
		line, err := json.Marshal(item)
		if err != nil {
			return "", core.NewInvalidRequestError(fmt.Sprintf("requests[%d]: %v", i, err), err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	file, err := p.CreateFile(ctx, &core.FileCreateRequest{
		Purpose:  "batch",
		Filename: "inline-batch-input.jsonl",
		Content:  buf.Bytes(),
	})
	if err != nil {
		return "", err
	}
	if file == nil || file.ID == "" {
		return "", core.NewProviderError(p.providerName, http.StatusBadGateway, "batch input file upload returned no file id", nil)
	}
	return file.ID, nil
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
	providers.EnsureProviderBatchID(&resp)
	return &resp, nil
}

func (p *CompatibleProvider) ListBatches(ctx context.Context, limit int, after string) (*core.BatchListResponse, error) {
	endpoint := providers.PaginatedEndpoint("/batches", limit, "after", after)

	var resp core.BatchListResponse
	err := p.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: endpoint,
	}, &resp)
	if err != nil {
		return nil, err
	}
	providers.EnsureProviderBatchIDs(&resp)
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
	providers.EnsureProviderBatchID(&resp)
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

func responseRetrieveEndpoint(id string, params core.ResponseRetrieveParams) string {
	values := url.Values{}
	for _, include := range params.Include {
		if include != "" {
			values.Add("include[]", include)
		}
	}
	if params.IncludeObfuscation != nil {
		values.Set("include_obfuscation", strconv.FormatBool(*params.IncludeObfuscation))
	}
	if params.StartingAfter != nil {
		values.Set("starting_after", strconv.Itoa(*params.StartingAfter))
	}
	endpoint := "/responses/" + url.PathEscape(id)
	if encoded := values.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	return endpoint
}

func responseInputItemsEndpoint(id string, params core.ResponseInputItemsParams) string {
	values := url.Values{}
	for _, include := range params.Include {
		if include != "" {
			values.Add("include[]", include)
		}
	}
	if params.After != "" {
		values.Set("after", params.After)
	}
	if params.Limit > 0 {
		values.Set("limit", strconv.Itoa(params.Limit))
	}
	if params.Order != "" {
		values.Set("order", params.Order)
	}
	endpoint := "/responses/" + url.PathEscape(id) + "/input_items"
	if encoded := values.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	return endpoint
}
