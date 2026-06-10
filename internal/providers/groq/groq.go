// Package groq provides Groq API integration for the LLM gateway.
package groq

import (
	"context"
	"io"
	"net/http"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
	"gomodel/internal/providers/openai"
)

// Registration provides factory registration for the Groq provider.
var Registration = providers.Registration{
	Type: "groq",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

const (
	defaultBaseURL = "https://api.groq.com/openai/v1"
)

// Provider implements the core.Provider interface for Groq. Groq's API is
// OpenAI-compatible, so all transport goes through the shared compatible
// provider; only the Responses API differs (translated via chat) because the
// gateway does not use Groq's native /responses endpoints.
type Provider struct {
	compat *openai.CompatibleProvider
}

// New creates a new Groq provider.
func New(providerCfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{
		compat: openai.NewCompatibleProvider(providerCfg.APIKey, opts, openai.CompatibleProviderConfig{
			ProviderName: "groq",
			BaseURL:      providers.ResolveBaseURL(providerCfg.BaseURL, defaultBaseURL),
			SetHeaders:   setHeaders,
		}),
	}
}

// NewWithHTTPClient creates a new Groq provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	return &Provider{
		compat: openai.NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, openai.CompatibleProviderConfig{
			ProviderName: "groq",
			BaseURL:      defaultBaseURL,
			SetHeaders:   setHeaders,
		}),
	}
}

// setHeaders sets the required headers for Groq API requests
func setHeaders(req *http.Request, apiKey string) {
	providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{
		AuthScheme:      "Bearer ",
		RequestIDHeader: "X-Request-ID",
	})
}

// SetBaseURL allows configuring a custom base URL for the provider
func (p *Provider) SetBaseURL(url string) {
	p.compat.SetBaseURL(url)
}

// ChatCompletion sends a chat completion request to Groq
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return p.compat.ChatCompletion(ctx, req)
}

// StreamChatCompletion returns a raw response body for streaming (caller must close)
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return p.compat.StreamChatCompletion(ctx, req)
}

// ListModels retrieves the list of available models from Groq
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	return p.compat.ListModels(ctx)
}

// Responses sends a Responses API request to Groq (converted to chat format)
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return providers.ResponsesViaChat(ctx, p, req)
}

// StreamResponses returns a raw response body for streaming Responses API (caller must close)
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return providers.StreamResponsesViaChat(ctx, p, req, "groq")
}

// Embeddings sends an embeddings request to Groq
func (p *Provider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return p.compat.Embeddings(ctx, req)
}

// CreateSpeech synthesizes speech through Groq's OpenAI-compatible /audio/speech API.
func (p *Provider) CreateSpeech(ctx context.Context, req *core.AudioSpeechRequest) (*core.AudioResponse, error) {
	return p.compat.CreateSpeech(ctx, req)
}

// CreateTranscription transcribes audio through Groq's OpenAI-compatible
// /audio/transcriptions API (whisper models).
func (p *Provider) CreateTranscription(ctx context.Context, req *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
	return p.compat.CreateTranscription(ctx, req)
}

// CreateBatch creates a native Groq batch job.
func (p *Provider) CreateBatch(ctx context.Context, req *core.BatchRequest) (*core.BatchResponse, error) {
	return p.compat.CreateBatch(ctx, req)
}

// GetBatch retrieves a native Groq batch job.
func (p *Provider) GetBatch(ctx context.Context, id string) (*core.BatchResponse, error) {
	return p.compat.GetBatch(ctx, id)
}

// ListBatches lists native Groq batch jobs.
func (p *Provider) ListBatches(ctx context.Context, limit int, after string) (*core.BatchListResponse, error) {
	return p.compat.ListBatches(ctx, limit, after)
}

// CancelBatch cancels a native Groq batch job.
func (p *Provider) CancelBatch(ctx context.Context, id string) (*core.BatchResponse, error) {
	return p.compat.CancelBatch(ctx, id)
}

// GetBatchResults fetches Groq batch results via the output file API.
func (p *Provider) GetBatchResults(ctx context.Context, id string) (*core.BatchResultsResponse, error) {
	return p.compat.GetBatchResults(ctx, id)
}

// CreateFile uploads a file through Groq's OpenAI-compatible /files API.
func (p *Provider) CreateFile(ctx context.Context, req *core.FileCreateRequest) (*core.FileObject, error) {
	return p.compat.CreateFile(ctx, req)
}

// ListFiles lists files through Groq's OpenAI-compatible /files API.
func (p *Provider) ListFiles(ctx context.Context, purpose string, limit int, after string) (*core.FileListResponse, error) {
	return p.compat.ListFiles(ctx, purpose, limit, after)
}

// GetFile retrieves one file object through Groq's OpenAI-compatible /files API.
func (p *Provider) GetFile(ctx context.Context, id string) (*core.FileObject, error) {
	return p.compat.GetFile(ctx, id)
}

// DeleteFile deletes a file object through Groq's OpenAI-compatible /files API.
func (p *Provider) DeleteFile(ctx context.Context, id string) (*core.FileDeleteResponse, error) {
	return p.compat.DeleteFile(ctx, id)
}

// GetFileContent fetches raw file bytes through Groq's /files/{id}/content API.
func (p *Provider) GetFileContent(ctx context.Context, id string) (*core.FileContentResponse, error) {
	return p.compat.GetFileContent(ctx, id)
}
