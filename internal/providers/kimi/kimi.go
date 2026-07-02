// Package kimi integrates Kimi's OpenAI-compatible API as a thin wrapper over
// the shared openai.CompatibleProvider with standard Bearer auth.
package kimi

import (
	"context"
	"io"
	"net/http"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
	"gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://api.kimi.com/coding/v1"

func bearerSetHeaders(req *http.Request, apiKey string) {
	providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{AuthScheme: "Bearer "})
}

// Registration provides factory registration for the Kimi provider.
var Registration = providers.Registration{
	Type: "kimi",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

type Provider struct {
	compat *openai.CompatibleProvider
}

var _ core.Provider = (*Provider)(nil)

// New creates a new Kimi provider.
func New(providerCfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{
		compat: openai.NewCompatibleProvider(providerCfg.APIKey, opts, openai.CompatibleProviderConfig{
			ProviderName:               "kimi",
			BaseURL:                    providers.ResolveBaseURL(providerCfg.BaseURL, defaultBaseURL),
			SetHeaders:                 bearerSetHeaders,
			CustomUpstreamHeaders:      providerCfg.CustomUpstreamHeaders,
			PassthroughUserHeaders:     providerCfg.PassthroughUserHeaders,
			PassthroughUserHeadersSkip: providerCfg.PassthroughUserHeadersSkip,
		}),
	}
}

// NewWithHTTPClient creates a new Kimi provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	return &Provider{
		compat: openai.NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, openai.CompatibleProviderConfig{
			ProviderName: "kimi",
			BaseURL:      defaultBaseURL,
			SetHeaders:   bearerSetHeaders,
		}),
	}
}

// SetBaseURL allows configuring a custom base URL for the provider.
func (p *Provider) SetBaseURL(url string) {
	p.compat.SetBaseURL(url)
}

// GetBaseURL returns the provider's current base URL. It reads from the
// underlying client so it always reflects SetBaseURL overrides.
func (p *Provider) GetBaseURL() string {
	return p.compat.GetBaseURL()
}

// ChatCompletion sends a chat completion request to Kimi.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return p.compat.ChatCompletion(ctx, req)
}

// StreamChatCompletion returns a raw response body for streaming (caller must close).
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return p.compat.StreamChatCompletion(ctx, req)
}

// ListModels retrieves the list of available models from Kimi.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	return p.compat.ListModels(ctx)
}

// Responses sends a Responses API request to Kimi.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return p.compat.Responses(ctx, req)
}

// StreamResponses returns a raw response body for streaming Responses API (caller must close).
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return p.compat.StreamResponses(ctx, req)
}

// GetResponse retrieves a stored Response by id.
func (p *Provider) GetResponse(ctx context.Context, id string, params core.ResponseRetrieveParams) (*core.ResponsesResponse, error) {
	return p.compat.GetResponse(ctx, id, params)
}

// ListResponseInputItems lists the input items of a stored Response.
func (p *Provider) ListResponseInputItems(ctx context.Context, id string, params core.ResponseInputItemsParams) (*core.ResponseInputItemListResponse, error) {
	return p.compat.ListResponseInputItems(ctx, id, params)
}

// Embeddings sends an embeddings request to Kimi.
func (p *Provider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return p.compat.Embeddings(ctx, req)
}

// Passthrough routes an opaque provider-native request to Kimi.
func (p *Provider) Passthrough(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	return p.compat.Passthrough(ctx, req)
}

// CreateBatch creates a native Kimi batch job.
func (p *Provider) CreateBatch(ctx context.Context, req *core.BatchRequest) (*core.BatchResponse, error) {
	return p.compat.CreateBatch(ctx, req)
}

// GetBatch retrieves a native Kimi batch job.
func (p *Provider) GetBatch(ctx context.Context, id string) (*core.BatchResponse, error) {
	return p.compat.GetBatch(ctx, id)
}

// ListBatches lists native Kimi batch jobs.
func (p *Provider) ListBatches(ctx context.Context, limit int, after string) (*core.BatchListResponse, error) {
	return p.compat.ListBatches(ctx, limit, after)
}

// CancelBatch cancels a native Kimi batch job.
func (p *Provider) CancelBatch(ctx context.Context, id string) (*core.BatchResponse, error) {
	return p.compat.CancelBatch(ctx, id)
}

// GetBatchResults fetches Kimi batch results via the output file API.
func (p *Provider) GetBatchResults(ctx context.Context, id string) (*core.BatchResultsResponse, error) {
	return p.compat.GetBatchResults(ctx, id)
}

// CreateFile uploads a file through Kimi's OpenAI-compatible /files API.
func (p *Provider) CreateFile(ctx context.Context, req *core.FileCreateRequest) (*core.FileObject, error) {
	return p.compat.CreateFile(ctx, req)
}

// ListFiles lists files through Kimi's OpenAI-compatible /files API.
func (p *Provider) ListFiles(ctx context.Context, purpose string, limit int, after string) (*core.FileListResponse, error) {
	return p.compat.ListFiles(ctx, purpose, limit, after)
}

// GetFile retrieves one file object through Kimi's OpenAI-compatible /files API.
func (p *Provider) GetFile(ctx context.Context, id string) (*core.FileObject, error) {
	return p.compat.GetFile(ctx, id)
}

// DeleteFile deletes a file object through Kimi's OpenAI-compatible /files API.
func (p *Provider) DeleteFile(ctx context.Context, id string) (*core.FileDeleteResponse, error) {
	return p.compat.DeleteFile(ctx, id)
}

// GetFileContent fetches raw file bytes through Kimi's /files/{id}/content API.
func (p *Provider) GetFileContent(ctx context.Context, id string) (*core.FileContentResponse, error) {
	return p.compat.GetFileContent(ctx, id)
}

// CreateTranscription transcribes audio through Kimi's OpenAI-compatible
// /audio/transcriptions API.
func (p *Provider) CreateTranscription(ctx context.Context, req *core.AudioTranscriptionRequest) (*core.AudioResponse, error) {
	return p.compat.CreateTranscription(ctx, req)
}

// CreateSpeech synthesizes speech through Kimi's OpenAI-compatible /audio/speech API.
func (p *Provider) CreateSpeech(ctx context.Context, req *core.AudioSpeechRequest) (*core.AudioResponse, error) {
	return p.compat.CreateSpeech(ctx, req)
}
