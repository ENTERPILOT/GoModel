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

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
	"gomodel/internal/providers/openai"
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
// delegated explicitly rather than embedded because xAI's upstream lacks
// parts of the full OpenAI surface (audio, passthrough, response
// lifecycle management) and embedding cannot subtract methods.
type Provider struct {
	compat *openai.CompatibleProvider
	apiKey string // retained to inject auth on the realtime websocket target
}

// New creates a new xAI provider.
func New(providerCfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{
		compat: openai.NewCompatibleProvider(providerCfg.APIKey, opts, compatibleConfig(providers.ResolveBaseURL(providerCfg.BaseURL, defaultBaseURL))),
		apiKey: providerCfg.APIKey,
	}
}

// NewWithHTTPClient creates a new xAI provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	return &Provider{
		compat: openai.NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, compatibleConfig(defaultBaseURL)),
		apiKey: apiKey,
	}
}

func compatibleConfig(baseURL string) openai.CompatibleProviderConfig {
	return openai.CompatibleProviderConfig{
		ProviderName:       "xai",
		BaseURL:            baseURL,
		SetHeaders:         setHeaders,
		ChatRequestHeaders: xGrokConversationHeaders,
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
	limit := 2
	if len(messages) < limit {
		limit = len(messages)
	}
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

// CreateBatch creates a native xAI batch job.
func (p *Provider) CreateBatch(ctx context.Context, req *core.BatchRequest) (*core.BatchResponse, error) {
	return p.compat.CreateBatch(ctx, req)
}

// GetBatch retrieves a native xAI batch job.
func (p *Provider) GetBatch(ctx context.Context, id string) (*core.BatchResponse, error) {
	return p.compat.GetBatch(ctx, id)
}

// ListBatches lists native xAI batch jobs.
func (p *Provider) ListBatches(ctx context.Context, limit int, after string) (*core.BatchListResponse, error) {
	return p.compat.ListBatches(ctx, limit, after)
}

// CancelBatch cancels a native xAI batch job.
func (p *Provider) CancelBatch(ctx context.Context, id string) (*core.BatchResponse, error) {
	return p.compat.CancelBatch(ctx, id)
}

// GetBatchResults fetches xAI batch results via the output file API.
func (p *Provider) GetBatchResults(ctx context.Context, id string) (*core.BatchResultsResponse, error) {
	return p.compat.GetBatchResults(ctx, id)
}

// CreateFile uploads a file through xAI's OpenAI-compatible /files API.
func (p *Provider) CreateFile(ctx context.Context, req *core.FileCreateRequest) (*core.FileObject, error) {
	return p.compat.CreateFile(ctx, req)
}

// ListFiles lists files through xAI's OpenAI-compatible /files API.
func (p *Provider) ListFiles(ctx context.Context, purpose string, limit int, after string) (*core.FileListResponse, error) {
	return p.compat.ListFiles(ctx, purpose, limit, after)
}

// GetFile retrieves one file object through xAI's OpenAI-compatible /files API.
func (p *Provider) GetFile(ctx context.Context, id string) (*core.FileObject, error) {
	return p.compat.GetFile(ctx, id)
}

// DeleteFile deletes a file object through xAI's OpenAI-compatible /files API.
func (p *Provider) DeleteFile(ctx context.Context, id string) (*core.FileDeleteResponse, error) {
	return p.compat.DeleteFile(ctx, id)
}

// GetFileContent fetches raw file bytes through xAI's /files/{id}/content API.
func (p *Provider) GetFileContent(ctx context.Context, id string) (*core.FileContentResponse, error) {
	return p.compat.GetFileContent(ctx, id)
}
