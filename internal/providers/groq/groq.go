// Package groq provides Groq API integration for the LLM gateway.
package groq

import (
	"context"
	"io"
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
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
// provider; the Responses API is translated via chat because the gateway
// does not use Groq's native /responses endpoints. Methods are delegated
// explicitly (and batch/files via facet surfaces) rather than embedding the
// full compatible provider, because Groq's upstream lacks passthrough and
// native response lifecycle management and embedding cannot subtract
// methods.
type Provider struct {
	*openai.BatchSurface
	*openai.FileSurface
	compat *openai.CompatibleProvider
}

// New creates a new Groq provider.
func New(providerCfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return newProvider(openai.NewCompatibleProvider(providerCfg.APIKey, opts, compatibleConfig(providers.ResolveBaseURL(providerCfg.BaseURL, defaultBaseURL))))
}

// NewWithHTTPClient creates a new Groq provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	return newProvider(openai.NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, compatibleConfig(defaultBaseURL)))
}

func newProvider(compat *openai.CompatibleProvider) *Provider {
	return &Provider{
		BatchSurface: openai.NewBatchSurface(compat),
		FileSurface:  openai.NewFileSurface(compat),
		compat:       compat,
	}
}

func compatibleConfig(baseURL string) openai.CompatibleProviderConfig {
	return openai.CompatibleProviderConfig{
		ProviderName: "groq",
		BaseURL:      baseURL,
		SetHeaders:   setHeaders,
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
