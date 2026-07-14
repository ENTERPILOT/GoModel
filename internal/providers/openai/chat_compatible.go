package openai

import (
	"context"
	"io"
	"net/http"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

// ChatCompatible is an embeddable adapter for providers that expose the
// chat-centric subset of an OpenAI-compatible API: chat completions, model
// listing, embeddings, and passthrough, with the Responses API translated
// through chat completions. When CompatibleProviderConfig.SetHeaders is nil,
// standard "Authorization: Bearer" headers are applied.
//
// Providers embed *ChatCompatible and override only the methods where their
// API deviates. Responses and StreamResponses dispatch through this type's
// own ChatCompletion, so a provider that overrides ChatCompletion or
// StreamChatCompletion must also override Responses and StreamResponses
// (re-calling providers.ResponsesViaChat with itself) for the translation to
// pick up the override.
type ChatCompatible struct {
	compatible   *CompatibleProvider
	providerName string
}

// NewChatCompatible creates a chat-centric adapter using provider options
// from the factory (resilience, hooks).
func NewChatCompatible(apiKey string, opts providers.ProviderOptions, cfg CompatibleProviderConfig) *ChatCompatible {
	applyChatCompatibleDefaults(&cfg)
	return &ChatCompatible{
		compatible:   NewCompatibleProvider(apiKey, opts, cfg),
		providerName: cfg.ProviderName,
	}
}

// NewChatCompatibleWithHTTPClient creates a chat-centric adapter with a
// custom HTTP client. If httpClient is nil, http.DefaultClient is used.
func NewChatCompatibleWithHTTPClient(apiKey string, httpClient *http.Client, hooks llmclient.Hooks, cfg CompatibleProviderConfig) *ChatCompatible {
	applyChatCompatibleDefaults(&cfg)
	return &ChatCompatible{
		compatible:   NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, cfg),
		providerName: cfg.ProviderName,
	}
}

func applyChatCompatibleDefaults(cfg *CompatibleProviderConfig) {
	if cfg.SetHeaders == nil {
		cfg.SetHeaders = bearerHeaders
	}
}

func bearerHeaders(req *http.Request, apiKey string) {
	providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{AuthScheme: "Bearer "})
}

// SetBaseURL allows configuring a custom base URL for the provider.
func (c *ChatCompatible) SetBaseURL(url string) {
	c.compatible.SetBaseURL(url)
}

// GetBaseURL returns the provider's current base URL (reads live from the client,
// so it reflects SetBaseURL overrides). Used to derive realtime websocket targets.
func (c *ChatCompatible) GetBaseURL() string {
	return c.compatible.GetBaseURL()
}

// ChatCompletion sends a chat completion request to the provider.
func (c *ChatCompatible) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return c.compatible.ChatCompletion(ctx, req)
}

// StreamChatCompletion returns a raw response body for streaming.
func (c *ChatCompatible) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return c.compatible.StreamChatCompletion(ctx, req)
}

// ListModels retrieves the list of available models from the provider.
func (c *ChatCompatible) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	return c.compatible.ListModels(ctx)
}

// Responses sends a Responses API request using chat-completions translation.
func (c *ChatCompatible) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return providers.ResponsesViaChat(ctx, c, req)
}

// StreamResponses streams a Responses API request using chat-completions translation.
func (c *ChatCompatible) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return providers.StreamResponsesViaChat(ctx, c, req, c.providerName)
}

// Embeddings sends an embeddings request to the provider.
func (c *ChatCompatible) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return c.compatible.Embeddings(ctx, req)
}

// Passthrough routes an opaque provider-native request to the provider.
func (c *ChatCompatible) Passthrough(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	return c.compatible.Passthrough(ctx, req)
}
