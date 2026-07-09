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
// Transport goes through the shared compatible provider; Bailian's only chat
// quirk is the max_tokens -> max_completion_tokens mapping (Bailian
// deprecated max_tokens in April 2026), applied via the AdaptChatRequest
// hook so every chat-derived path picks it up. Batch and file capabilities
// are exposed through the embedded facet surfaces; the remaining methods are
// delegated explicitly through the compatible provider rather than embedding
// it whole, because Bailian's upstream lacks parts of the full OpenAI
// surface (audio, native /responses) and embedding cannot subtract methods.
type Provider struct {
	*openai.BatchSurface
	*openai.FileSurface
	compatible *openai.CompatibleProvider
	keys       *providers.Keyring // retained to inject auth on the realtime websocket target
}

// New creates a new Bailian provider from a resolved ProviderConfig.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	compat := openai.NewCompatibleProvider(cfg.APIKey, opts, compatibleConfig(providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL)))
	return newProvider(compat, opts.Keyring(cfg.APIKey))
}

// NewWithHTTPClient creates a new Bailian provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	compat := openai.NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, compatibleConfig(defaultBaseURL))
	return newProvider(compat, providers.NewKeyring(apiKey))
}

func newProvider(compat *openai.CompatibleProvider, keys *providers.Keyring) *Provider {
	return &Provider{
		BatchSurface: openai.NewBatchSurface(compat),
		FileSurface:  openai.NewFileSurface(compat),
		compatible:   compat,
		keys:         keys,
	}
}

func compatibleConfig(baseURL string) openai.CompatibleProviderConfig {
	return openai.CompatibleProviderConfig{
		ProviderName:     "bailian",
		BaseURL:          baseURL,
		SetHeaders:       setHeaders,
		AdaptChatRequest: adaptChatRequest,
	}
}

func setHeaders(req *http.Request, apiKey string) {
	providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{AuthScheme: "Bearer "})
}

// SetBaseURL configures a custom base URL for the provider.
func (p *Provider) SetBaseURL(url string) {
	p.compatible.SetBaseURL(url)
}

// ChatCompletion sends a chat completion request to Bailian.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return p.compatible.ChatCompletion(ctx, req)
}

// StreamChatCompletion returns a raw response body for streaming.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return p.compatible.StreamChatCompletion(ctx, req)
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
// mirroring the adaptation done on typed chat requests.
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

// adaptChatRequest maps max_tokens -> max_completion_tokens in the request.
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
func adaptChatRequest(req *core.ChatRequest) (*core.ChatRequest, error) {
	if req == nil || req.MaxTokens == nil {
		return req, nil
	}
	// If the caller already set max_completion_tokens explicitly, respect it.
	if existing := req.ExtraFields.Lookup("max_completion_tokens"); existing != nil {
		cloned := *req
		cloned.MaxTokens = nil
		return &cloned, nil
	}
	maxTokensJSON, err := json.Marshal(*req.MaxTokens)
	if err != nil {
		slog.Warn("bailian: failed to marshal MaxTokens for adaptation, forwarding original request",
			"error", err)
		return req, nil
	}
	extra, err := core.MergeUnknownJSONFields(req.ExtraFields, map[string]json.RawMessage{
		"max_completion_tokens": maxTokensJSON,
	})
	if err != nil {
		slog.Warn("bailian: failed to merge ExtraFields for adaptation, forwarding original request",
			"error", err)
		return req, nil
	}
	cloned := *req
	cloned.ExtraFields = extra
	cloned.MaxTokens = nil
	return &cloned, nil
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
