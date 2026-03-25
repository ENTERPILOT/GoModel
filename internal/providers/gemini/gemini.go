// Package gemini provides Google Gemini API integration for the LLM gateway.
package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
)

// Registration provides factory registration for the Gemini provider.
var Registration = providers.Registration{
	Type: "gemini",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultOpenAICompatibleBaseURL,
	},
}

const (
	// Gemini provides an OpenAI-compatible endpoint
	defaultOpenAICompatibleBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"
	// Native Gemini API endpoint for models listing
	defaultModelsBaseURL = "https://generativelanguage.googleapis.com/v1beta"
)

// Provider implements the core.Provider interface for Google Gemini
type Provider struct {
	client           *llmclient.Client
	httpClient       *http.Client
	hooks            llmclient.Hooks
	apiKey           string
	modelsURL        string
	modelsClientConf llmclient.Config
}

// New creates a new Gemini provider.
func New(providerCfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	baseURL := providers.ResolveBaseURL(providerCfg.BaseURL, defaultOpenAICompatibleBaseURL)
	p := &Provider{
		httpClient: nil,
		apiKey:     providerCfg.APIKey,
		hooks:      opts.Hooks,
		modelsURL:  defaultModelsBaseURL,
		modelsClientConf: llmclient.Config{
			ProviderName:   "gemini",
			BaseURL:        defaultModelsBaseURL,
			Retry:          opts.Resilience.Retry,
			Hooks:          opts.Hooks,
			CircuitBreaker: opts.Resilience.CircuitBreaker,
		},
	}
	clientCfg := llmclient.Config{
		ProviderName:   "gemini",
		BaseURL:        baseURL,
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}
	p.client = llmclient.New(clientCfg, p.setHeaders)
	return p
}

// NewWithHTTPClient creates a new Gemini provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	p := &Provider{
		httpClient: httpClient,
		apiKey:     apiKey,
		hooks:      hooks,
		modelsURL:  defaultModelsBaseURL,
	}
	modelsCfg := llmclient.DefaultConfig("gemini", defaultModelsBaseURL)
	modelsCfg.Hooks = hooks
	p.modelsClientConf = modelsCfg
	cfg := llmclient.DefaultConfig("gemini", defaultOpenAICompatibleBaseURL)
	cfg.Hooks = hooks
	p.client = llmclient.NewWithHTTPClient(httpClient, cfg, p.setHeaders)
	return p
}

// SetBaseURL allows configuring a custom base URL for the provider
func (p *Provider) SetBaseURL(url string) {
	p.client.SetBaseURL(url)
}

// SetModelsURL allows configuring a custom models API base URL.
// This is primarily useful for tests and local emulators.
func (p *Provider) SetModelsURL(url string) {
	p.modelsURL = url
	p.modelsClientConf.BaseURL = url
}

// setHeaders sets the required headers for Gemini API requests
func (p *Provider) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	// Forward request ID if present in context for request tracing
	if requestID := core.GetRequestID(req.Context()); requestID != "" {
		req.Header.Set("X-Request-Id", requestID)
	}
}

// adaptChatRequest rewrites a ChatRequest for Gemini's OpenAI-compatible endpoint.
// Gemini uses "reasoning_effort" as a top-level string (e.g. "low", "medium", "high"),
// not the nested "reasoning": {"effort": "..."} format.
func adaptChatRequest(req *core.ChatRequest) (any, error) {
	if req.Reasoning == nil || req.Reasoning.Effort == "" {
		return req, nil
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to marshal gemini request: "+err.Error(), err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, core.NewInvalidRequestError("failed to decode gemini request payload: "+err.Error(), err)
	}

	effort, _ := json.Marshal(req.Reasoning.Effort)
	raw["reasoning_effort"] = effort
	delete(raw, "reasoning")
	return raw, nil
}

// ChatCompletion sends a chat completion request to Gemini
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
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

// StreamChatCompletion returns a raw response body for streaming (caller must close)
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	streamReq := req.WithStreaming()
	body, err := adaptChatRequest(streamReq)
	if err != nil {
		return nil, err
	}
	stream, err := p.client.DoStream(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     body,
	})
	if err != nil {
		return nil, err
	}

	// Gemini's OpenAI-compatible endpoint returns OpenAI-format SSE, so we can pass it through directly
	return stream, nil
}

// geminiModel represents a model in Gemini's native API response
type geminiModel struct {
	Name             string   `json:"name"`
	DisplayName      string   `json:"displayName"`
	Description      string   `json:"description"`
	SupportedMethods []string `json:"supportedGenerationMethods"`
	InputTokenLimit  int      `json:"inputTokenLimit"`
	OutputTokenLimit int      `json:"outputTokenLimit"`
	Temperature      *float64 `json:"temperature,omitempty"`
	TopP             *float64 `json:"topP,omitempty"`
	TopK             *int     `json:"topK,omitempty"`
}

// geminiModelsResponse represents the native Gemini models list response
type geminiModelsResponse struct {
	Models []geminiModel `json:"models"`
}

// ListModels retrieves the list of available models from Gemini
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	// Use the native Gemini API to list models
	// We need to create a separate client for the models endpoint since it uses a different URL
	modelsCfg := p.modelsClientConf
	modelsCfg.BaseURL = p.modelsURL
	modelsCfg.Hooks = p.hooks
	headers := func(req *http.Request) {
		// Use header-based API key auth for models requests.
		req.Header.Set("x-goog-api-key", p.apiKey)

		// Preserve request tracing across list-models requests.
		requestID := req.Header.Get("X-Request-Id")
		if requestID == "" {
			requestID = core.GetRequestID(req.Context())
		}
		if requestID != "" {
			req.Header.Set("X-Request-Id", requestID)
		}
	}

	var modelsClient *llmclient.Client
	if p.httpClient != nil {
		modelsClient = llmclient.NewWithHTTPClient(p.httpClient, modelsCfg, headers)
	} else {
		modelsClient = llmclient.New(modelsCfg, headers)
	}

	rawResp, err := modelsClient.DoRaw(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/models",
	})
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()

	// Preferred path: native Gemini models response.
	// If the payload contains an explicit "models" field with an empty array,
	// return an empty list instead of falling through to fallback parsing.
	var nativeProbe struct {
		Models json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(rawResp.Body, &nativeProbe); err == nil && nativeProbe.Models != nil {
		var geminiResp geminiModelsResponse
		if err := json.Unmarshal(rawResp.Body, &geminiResp); err != nil {
			return nil, core.NewProviderError("gemini", http.StatusBadGateway, "failed to parse native Gemini models response", err)
		}
		if len(geminiResp.Models) == 0 {
			return &core.ModelsResponse{
				Object: "list",
				Data:   []core.Model{},
			}, nil
		}

		models := make([]core.Model, 0, len(geminiResp.Models))

		for _, gm := range geminiResp.Models {
			// Extract model ID from name (format: "models/gemini-...")
			modelID := strings.TrimPrefix(gm.Name, "models/")

			// Only include models that support generateContent (chat/completion)
			supportsGenerate := false
			for _, method := range gm.SupportedMethods {
				if method == "generateContent" || method == "streamGenerateContent" {
					supportsGenerate = true
					break
				}
			}

			supportsEmbed := slices.Contains(gm.SupportedMethods, "embedContent")

			isOpenAICompatModel := strings.HasPrefix(modelID, "gemini-") || strings.HasPrefix(modelID, "text-embedding-")
			if (supportsGenerate || supportsEmbed) && isOpenAICompatModel {
				models = append(models, core.Model{
					ID:      modelID,
					Object:  "model",
					OwnedBy: "google",
					Created: now,
				})
			}
		}

		return &core.ModelsResponse{
			Object: "list",
			Data:   models,
		}, nil
	}

	// Fallback path: OpenAI-compatible models list.
	var openAIResp core.ModelsResponse
	if err := json.Unmarshal(rawResp.Body, &openAIResp); err == nil && openAIResp.Object == "list" {
		models := make([]core.Model, 0, len(openAIResp.Data))
		for _, m := range openAIResp.Data {
			modelID := strings.TrimPrefix(m.ID, "models/")
			isOpenAICompatModel := strings.HasPrefix(modelID, "gemini-") || strings.HasPrefix(modelID, "text-embedding-")
			if !isOpenAICompatModel {
				continue
			}
			models = append(models, core.Model{
				ID:      modelID,
				Object:  "model",
				OwnedBy: "google",
				Created: now,
			})
		}
		return &core.ModelsResponse{
			Object: "list",
			Data:   models,
		}, nil
	}

	responsePreview := string(rawResp.Body)
	if len(responsePreview) > 512 {
		responsePreview = responsePreview[:512] + "...(truncated)"
	}
	return nil, core.NewProviderError("gemini", http.StatusBadGateway, "unexpected Gemini models response format", fmt.Errorf("models response body: %s", responsePreview))
}

// Responses sends a Responses API request to Gemini (converted to chat format)
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return providers.ResponsesViaChat(ctx, p, req)
}

// Embeddings sends an embeddings request to Gemini via its OpenAI-compatible endpoint
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

// StreamResponses returns a raw response body for streaming Responses API (caller must close)
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return providers.StreamResponsesViaChat(ctx, p, req, "gemini")
}

// CreateBatch creates a native Gemini batch job through its OpenAI-compatible endpoint.
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

// GetBatch retrieves a native Gemini batch job.
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

// ListBatches lists native Gemini batch jobs.
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

// CancelBatch cancels a native Gemini batch job.
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

// GetBatchResults fetches Gemini batch results via the output file API.
func (p *Provider) GetBatchResults(ctx context.Context, id string) (*core.BatchResultsResponse, error) {
	return providers.FetchBatchResultsFromOutputFile(ctx, p.client, "gemini", id)
}

// CreateFile uploads a file through Gemini's OpenAI-compatible /files API.
func (p *Provider) CreateFile(ctx context.Context, req *core.FileCreateRequest) (*core.FileObject, error) {
	resp, err := providers.CreateOpenAICompatibleFile(ctx, p.client, req)
	if err != nil {
		return nil, err
	}
	resp.Provider = "gemini"
	return resp, nil
}

// ListFiles lists files through Gemini's OpenAI-compatible /files API.
func (p *Provider) ListFiles(ctx context.Context, purpose string, limit int, after string) (*core.FileListResponse, error) {
	resp, err := providers.ListOpenAICompatibleFiles(ctx, p.client, purpose, limit, after)
	if err != nil {
		return nil, err
	}
	for i := range resp.Data {
		resp.Data[i].Provider = "gemini"
	}
	return resp, nil
}

// GetFile retrieves one file object through Gemini's OpenAI-compatible /files API.
func (p *Provider) GetFile(ctx context.Context, id string) (*core.FileObject, error) {
	resp, err := providers.GetOpenAICompatibleFile(ctx, p.client, id)
	if err != nil {
		return nil, err
	}
	resp.Provider = "gemini"
	return resp, nil
}

// DeleteFile deletes a file object through Gemini's OpenAI-compatible /files API.
func (p *Provider) DeleteFile(ctx context.Context, id string) (*core.FileDeleteResponse, error) {
	return providers.DeleteOpenAICompatibleFile(ctx, p.client, id)
}

// GetFileContent fetches raw file bytes through Gemini's /files/{id}/content API.
func (p *Provider) GetFileContent(ctx context.Context, id string) (*core.FileContentResponse, error) {
	return providers.GetOpenAICompatibleFileContent(ctx, p.client, id)
}
