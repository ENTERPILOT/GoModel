// Package bailian provides the Alibaba Cloud Bailian (百炼 / DashScope) provider.
// Bailian is Alibaba Cloud's model-as-a-service platform for the Qwen family
// of models. It exposes an OpenAI-compatible API through the
// /compatible-mode/v1 endpoint.
package bailian

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"

	"github.com/goccy/go-json"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
	"gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"

// Registration provides factory registration for the Bailian provider.
var Registration = providers.Registration{
	Type: "bailian",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for Alibaba Cloud Bailian.
// It wraps openai.CompatibleProvider and maps max_tokens to
// max_completion_tokens for every request (Bailian deprecated max_tokens
// in April 2026).
type Provider struct {
	compatible *openai.CompatibleProvider
	apiKey     string // retained to inject auth on the realtime websocket target
}

// New creates a new Bailian provider from a resolved ProviderConfig.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	baseURL := providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL)
	return &Provider{
		compatible: openai.NewCompatibleProvider(cfg.APIKey, opts, openai.CompatibleProviderConfig{
			ProviderName: "bailian",
			BaseURL:      baseURL,
			SetHeaders:   setHeaders,
		}),
		apiKey: cfg.APIKey,
	}
}

// NewWithHTTPClient creates a new Bailian provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	return &Provider{
		compatible: openai.NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, openai.CompatibleProviderConfig{
			ProviderName: "bailian",
			BaseURL:      defaultBaseURL,
			SetHeaders:   setHeaders,
		}),
		apiKey: apiKey,
	}
}

func setHeaders(req *http.Request, apiKey string) {
	providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{AuthScheme: "Bearer "})
}

// SetBaseURL configures a custom base URL for the provider.
func (p *Provider) SetBaseURL(url string) {
	p.compatible.SetBaseURL(url)
}

// ChatCompletion maps max_tokens to max_completion_tokens for Bailian models.
// Bailian deprecated max_tokens in April 2026; all models now require max_completion_tokens.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return p.compatible.ChatCompletion(ctx, adaptBailianRequest(req))
}

// StreamChatCompletion maps max_tokens to max_completion_tokens for streaming requests.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return p.compatible.StreamChatCompletion(ctx, adaptBailianRequest(req))
}

// ListModels returns the list of available models from Bailian.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	return p.compatible.ListModels(ctx)
}

// Responses sends a Responses API request translated through chat completions.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return providers.ResponsesViaChat(ctx, p, req)
}

// StreamResponses streams a Responses API request translated through chat completions.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return providers.StreamResponsesViaChat(ctx, p, req, "bailian")
}

// Embeddings sends an embedding request to Bailian's compatible-mode API.
// Embedding models (text-embedding-v3, text-embedding-v4) must be configured
// via BAILIAN_MODELS as they are not auto-discovered from the upstream
// /v1/models endpoint.
func (p *Provider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return p.compatible.Embeddings(ctx, req)
}

// Passthrough routes an opaque provider-native request to Bailian.
// It also adapts max_tokens -> max_completion_tokens in the raw body,
// mirroring the adaptation done in ChatCompletion/StreamChatCompletion.
func (p *Provider) Passthrough(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("passthrough request is required", nil)
	}
	adapted, err := adaptPassthroughBody(req.Body)
	if err != nil {
		slog.Warn("bailian: passthrough body adaptation failed",
			"error", err)
		return nil, err
	}
	if adapted != nil {
		req.Body = adapted
	}
	return p.compatible.Passthrough(ctx, req)
}

// CreateBatch creates a native Bailian batch job.
func (p *Provider) CreateBatch(ctx context.Context, req *core.BatchRequest) (*core.BatchResponse, error) {
	return p.compatible.CreateBatch(ctx, req)
}

// GetBatch retrieves a Bailian batch job by ID.
func (p *Provider) GetBatch(ctx context.Context, id string) (*core.BatchResponse, error) {
	return p.compatible.GetBatch(ctx, id)
}

// ListBatches lists Bailian batch jobs with pagination.
func (p *Provider) ListBatches(ctx context.Context, limit int, after string) (*core.BatchListResponse, error) {
	return p.compatible.ListBatches(ctx, limit, after)
}

// CancelBatch cancels a pending Bailian batch job.
func (p *Provider) CancelBatch(ctx context.Context, id string) (*core.BatchResponse, error) {
	return p.compatible.CancelBatch(ctx, id)
}

// GetBatchResults fetches Bailian batch results via the output file API.
func (p *Provider) GetBatchResults(ctx context.Context, id string) (*core.BatchResultsResponse, error) {
	return p.compatible.GetBatchResults(ctx, id)
}

// CreateFile uploads a file through Bailian's OpenAI-compatible /files API.
func (p *Provider) CreateFile(ctx context.Context, req *core.FileCreateRequest) (*core.FileObject, error) {
	resp, err := p.compatible.CreateFile(ctx, req)
	if err != nil {
		return nil, err
	}
	resp.Provider = "bailian"
	return resp, nil
}

// ListFiles lists files through Bailian's OpenAI-compatible /files API.
func (p *Provider) ListFiles(ctx context.Context, purpose string, limit int, after string) (*core.FileListResponse, error) {
	resp, err := p.compatible.ListFiles(ctx, purpose, limit, after)
	if err != nil {
		return nil, err
	}
	for i := range resp.Data {
		resp.Data[i].Provider = "bailian"
	}
	return resp, nil
}

// GetFile retrieves a file object through Bailian's OpenAI-compatible /files API.
func (p *Provider) GetFile(ctx context.Context, id string) (*core.FileObject, error) {
	resp, err := p.compatible.GetFile(ctx, id)
	if err != nil {
		return nil, err
	}
	resp.Provider = "bailian"
	return resp, nil
}

// DeleteFile deletes a file through Bailian's OpenAI-compatible /files API.
func (p *Provider) DeleteFile(ctx context.Context, id string) (*core.FileDeleteResponse, error) {
	return p.compatible.DeleteFile(ctx, id)
}

// GetFileContent fetches raw file bytes through Bailian's /files/{id}/content API.
func (p *Provider) GetFileContent(ctx context.Context, id string) (*core.FileContentResponse, error) {
	return p.compatible.GetFileContent(ctx, id)
}

// adaptBailianRequest maps max_tokens -> max_completion_tokens in the request.
// Bailian deprecated max_tokens in April 2026.
// It moves MaxTokens into ExtraFields as max_completion_tokens so that when
// ChatRequest is serialized, max_tokens is omitted and max_completion_tokens
// appears instead.
//
// If the user already set max_completion_tokens explicitly (Bailian-native
// parameter), its value is preserved and max_tokens is used as a fallback
// only — the explicit value takes precedence.
//
// If either operation fails, the original request is returned unmodified
// and a warning is logged so operators can diagnose the issue.
func adaptBailianRequest(req *core.ChatRequest) *core.ChatRequest {
	if req == nil || req.MaxTokens == nil {
		return req
	}
	// If the caller already set max_completion_tokens explicitly, respect it.
	if existing := req.ExtraFields.Lookup("max_completion_tokens"); existing != nil {
		cloned := *req
		cloned.MaxTokens = nil
		return &cloned
	}
	maxTokensJSON, err := json.Marshal(*req.MaxTokens)
	if err != nil {
		slog.Warn("bailian: failed to marshal MaxTokens for adaptation, forwarding original request",
			"error", err)
		return req
	}
	extra, err := core.MergeUnknownJSONFields(req.ExtraFields, map[string]json.RawMessage{
		"max_completion_tokens": maxTokensJSON,
	})
	if err != nil {
		slog.Warn("bailian: failed to merge ExtraFields for adaptation, forwarding original request",
			"error", err)
		return req
	}
	cloned := *req
	cloned.ExtraFields = extra
	cloned.MaxTokens = nil
	return &cloned
}

// adaptPassthroughBody adapts max_tokens -> max_completion_tokens in a raw
// passthrough request body. It reads the body, parses it as JSON, swaps the
// field if needed, and returns a new io.ReadCloser with the adapted or original
// body. If parsing fails, the original bytes are forwarded unchanged.
func adaptPassthroughBody(body io.ReadCloser) (io.ReadCloser, error) {
	if body == nil {
		return nil, nil
	}
	raw, err := io.ReadAll(body)
	body.Close()
	if err != nil {
		return nil, err
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		// Not valid JSON — can't adapt, rewind original bytes.
		return io.NopCloser(bytes.NewReader(raw)), nil
	}

	// No max_tokens present — no adaptation needed, rewind original.
	if _, hasMaxTokens := obj["max_tokens"]; !hasMaxTokens {
		return io.NopCloser(bytes.NewReader(raw)), nil
	}

	// Caller already set max_completion_tokens — just remove max_tokens.
	if _, hasMCT := obj["max_completion_tokens"]; hasMCT {
		delete(obj, "max_tokens")
		adapted, err := json.Marshal(obj)
		if err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(adapted)), nil
	}

	// Swap: max_tokens -> max_completion_tokens, remove max_tokens.
	obj["max_completion_tokens"] = obj["max_tokens"]
	delete(obj, "max_tokens")
	adapted, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(adapted)), nil
}
