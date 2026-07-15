// Package xai provides xAI (Grok) API integration for the LLM gateway.
package xai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

// Registration provides factory registration for the xAI provider.
var Registration = providers.Registration{
	Type: "xai",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

const (
	defaultBaseURL   = "https://api.x.ai/v1"
	grokConvIDHeader = "X-Grok-Conv-Id"
)

// Provider implements the core.Provider interface for xAI. The API is
// OpenAI-compatible, so transport goes through the shared compatible
// provider; xAI's only chat quirk is the conversation affinity header
// (X-Grok-Conv-Id), injected via the ChatRequestHeaders hook. Methods are
// delegated explicitly (and batch/files via facet surfaces) rather than
// embedding the full compatible provider, because xAI's upstream lacks
// parts of the full OpenAI surface (audio, passthrough, response
// lifecycle management) and embedding cannot subtract methods.
type Provider struct {
	*openai.BatchSurface
	*openai.FileSurface
	compat *openai.CompatibleProvider
	keys   *providers.Keyring // retained to inject auth on the realtime websocket target
}

// New creates a new xAI provider.
func New(providerCfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	compat := openai.NewCompatibleProvider(providerCfg.APIKey, opts, compatibleConfig(providers.ResolveBaseURL(providerCfg.BaseURL, defaultBaseURL)))
	return newProvider(compat, opts.Keyring(providerCfg.APIKey))
}

// NewWithHTTPClient creates a new xAI provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	compat := openai.NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, compatibleConfig(defaultBaseURL))
	return newProvider(compat, providers.NewKeyring(apiKey))
}

func newProvider(compat *openai.CompatibleProvider, keys *providers.Keyring) *Provider {
	return &Provider{
		BatchSurface: openai.NewBatchSurface(compat),
		FileSurface:  openai.NewFileSurface(compat),
		compat:       compat,
		keys:         keys,
	}
}

func compatibleConfig(baseURL string) openai.CompatibleProviderConfig {
	return openai.CompatibleProviderConfig{
		ProviderName:       "xai",
		BaseURL:            baseURL,
		SetHeaders:         setHeaders,
		AdaptChatRequest:   adaptChatRequest,
		ChatRequestHeaders: xGrokConversationHeaders,
	}
}

// adaptChatRequest rewrites GoModel's common reasoning shape into xAI's
// OpenAI-compatible chat extension. The xAI Chat Completions API accepts
// reasoning_effort as a top-level string (e.g. grok-4.5: low/medium/high,
// default high), not "reasoning": {"effort": "..."}; the nested shape is
// native only to xAI's Responses API, which passes through untouched.
func adaptChatRequest(req *core.ChatRequest) (*core.ChatRequest, error) {
	if req == nil || req.Reasoning == nil || strings.TrimSpace(req.Reasoning.Effort) == "" {
		return req, nil
	}
	return providers.AdaptReasoningEffortRequest(req, normalizeReasoningEffort(req.Model, req.Reasoning.Effort))
}

// normalizeReasoningEffort downgrades GoModel effort levels xAI does not
// accept to their nearest equivalent. Only the multi-agent Grok family
// accepts "xhigh" (it selects the agent count); every other model tops out
// at "high". Unknown values pass through for the upstream to judge. See
// docs/providers/xai.mdx for the user-facing table.
func normalizeReasoningEffort(model, effort string) string {
	normalized := strings.ToLower(strings.TrimSpace(effort))
	switch normalized {
	case "xhigh", "max":
		if strings.Contains(strings.ToLower(model), "multi-agent") {
			return "xhigh"
		}
		return "high"
	default:
		return normalized
	}
}

// SetBaseURL allows configuring a custom base URL for the provider
func (p *Provider) SetBaseURL(url string) {
	p.compat.SetBaseURL(url)
}

// setHeaders sets the required headers for xAI API requests
func setHeaders(req *http.Request, apiKey string) {
	providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{
		AuthScheme:      "Bearer ",
		RequestIDHeader: "X-Request-ID",
	})
}

type grokConversationAnchor struct {
	Model             string           `json:"model,omitempty"`
	Messages          []core.Message   `json:"messages,omitempty"`
	Tools             []map[string]any `json:"tools,omitempty"`
	ToolChoice        any              `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool            `json:"parallel_tool_calls,omitempty"`
	Reasoning         *core.Reasoning  `json:"reasoning,omitempty"`
	RequestID         string           `json:"request_id,omitempty"`
}

func xGrokConversationHeaders(ctx context.Context, req *core.ChatRequest) http.Header {
	convID := xGrokConversationID(ctx, req)
	if convID == "" {
		return nil
	}
	headers := make(http.Header, 1)
	headers.Set(grokConvIDHeader, convID)
	return headers
}

func xGrokConversationID(ctx context.Context, req *core.ChatRequest) string {
	if convID := xGrokConversationIDFromSnapshot(ctx); convID != "" {
		return convID
	}
	return generatedXGrokConversationID(ctx, req)
}

func xGrokConversationIDFromSnapshot(ctx context.Context) string {
	snapshot := core.GetRequestSnapshot(ctx)
	if snapshot == nil {
		return ""
	}
	for key, values := range snapshot.HeadersView() {
		if !strings.EqualFold(key, grokConvIDHeader) {
			continue
		}
		for _, value := range values {
			if convID := cleanXGrokConversationID(value); convID != "" {
				return convID
			}
		}
	}
	return ""
}

func generatedXGrokConversationID(ctx context.Context, req *core.ChatRequest) string {
	anchor := grokConversationAnchor{
		RequestID: strings.TrimSpace(core.GetRequestID(ctx)),
	}
	if req != nil {
		anchor.Model = req.Model
		anchor.Messages = xGrokAnchorMessages(req.Messages)
		anchor.Tools = req.Tools
		anchor.ToolChoice = req.ToolChoice
		anchor.ParallelToolCalls = req.ParallelToolCalls
		anchor.Reasoning = req.Reasoning
		anchor.RequestID = ""
	}
	if anchor.Model == "" && len(anchor.Messages) == 0 && anchor.RequestID == "" {
		return ""
	}
	body, err := json.Marshal(anchor)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(body)
	return "gomodel-" + hex.EncodeToString(sum[:16])
}

func xGrokAnchorMessages(messages []core.Message) []core.Message {
	if len(messages) == 0 {
		return nil
	}
	limit := min(len(messages), 2)
	anchor := make([]core.Message, limit)
	copy(anchor, messages[:limit])
	return anchor
}

func cleanXGrokConversationID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return ""
	}
	return value
}

// ChatCompletion sends a chat completion request to xAI
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return p.compat.ChatCompletion(ctx, req)
}

// StreamChatCompletion returns a raw response body for streaming (caller must close)
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return p.compat.StreamChatCompletion(ctx, req)
}

// ListModels retrieves the list of available models from xAI
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	return p.compat.ListModels(ctx)
}

// Responses sends a Responses API request to xAI's native /responses endpoint.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return p.compat.Responses(ctx, req)
}

// StreamResponses returns a normalized streaming Responses API body.
// The returned io.ReadCloser is wrapped by providers.EnsureResponsesDone, so
// callers must not assume it contains verbatim upstream bytes; the wrapper may
// synthesize a terminal `data: [DONE]` marker on completed streams. Callers
// remain responsible for closing the returned stream.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return p.compat.StreamResponses(ctx, req)
}

// Embeddings sends an embeddings request to xAI
func (p *Provider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return p.compat.Embeddings(ctx, req)
}
