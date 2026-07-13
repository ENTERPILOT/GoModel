// Package opencodego provides OpenCode Zen (Go subscription) integration for
// the LLM gateway.
package opencodego

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/anthropic"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

// defaultBaseURL is the OpenCode Zen "Go" endpoint. Its /chat/completions and
// /models routes are OpenAI-compatible (Bearer auth); its /messages route is
// Anthropic-native (x-api-key auth). Override with OPENCODE_GO_BASE_URL.
const defaultBaseURL = "https://opencode.ai/zen/go/v1"

// messagesModelsEnvVar names a comma-separated override list of model IDs that
// must be routed to the Anthropic-native /messages endpoint.
const messagesModelsEnvVar = "OPENCODE_GO_MESSAGES_MODELS"

// defaultMessagesModels lists the model IDs OpenCode Zen serves ONLY on its
// Anthropic-native /messages endpoint — /chat/completions rejects them with
// "not supported for format oa-compat". This is a temporary hardcoded set;
// upstream does not yet expose per-model endpoint metadata, so the split is
// maintained here and can be overridden with OPENCODE_GO_MESSAGES_MODELS until
// metadata-driven routing replaces it.
var defaultMessagesModels = []string{"qwen3.7-max"}

// Registration provides factory registration for the OpenCode Go provider.
// The "opencode_go" type derives OPENCODE_GO_API_KEY, OPENCODE_GO_BASE_URL, and
// OPENCODE_GO_MODELS by convention.
var Registration = providers.Registration{
	Type: "opencode_go",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// messagesProvider is the subset of the Anthropic provider OpenCode Go reuses
// to translate canonical chat requests onto the Anthropic-native /messages
// endpoint.
type messagesProvider interface {
	ChatCompletion(context.Context, *core.ChatRequest) (*core.ChatResponse, error)
	StreamChatCompletion(context.Context, *core.ChatRequest) (io.ReadCloser, error)
}

// Provider implements the core.Provider interface for OpenCode Go.
//
// OpenCode Zen exposes two upstream dialects behind one base URL: OpenAI-style
// /chat/completions (most models) and Anthropic-style /messages (a few models
// rejected by /chat/completions). The embedded ChatCompatible handles the
// former; chat requests for models in messagesModels are delegated to an
// Anthropic provider pinned to the same base URL, mirroring how GoModel already
// serves /v1/messages for native Anthropic. Both paths normalize to the
// canonical OpenAI-shaped response, so callers see one consistent surface.
type Provider struct {
	*openai.ChatCompatible
	messages       messagesProvider
	messagesModels map[string]struct{}
}

var _ core.Provider = (*Provider)(nil)

// New creates a new OpenCode Go provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	baseURL := providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL)
	chat := openai.NewChatCompatible(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName: "opencode_go",
		BaseURL:      baseURL,
	})
	// opts carries the shared keyring, so the /messages client rotates in step
	// with the chat client above rather than pinning the primary key.
	messages := anthropic.New(providers.ProviderConfig{APIKey: cfg.APIKey, APIKeys: cfg.APIKeys, BaseURL: baseURL}, opts)
	return &Provider{
		ChatCompatible: chat,
		messages:       messages,
		messagesModels: loadMessagesModels(),
	}
}

// NewWithHTTPClient creates a new OpenCode Go provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	resolved := providers.ResolveBaseURL(baseURL, defaultBaseURL)
	chat := openai.NewChatCompatibleWithHTTPClient(apiKey, httpClient, hooks, openai.CompatibleProviderConfig{
		ProviderName: "opencode_go",
		BaseURL:      resolved,
	})
	messages := anthropic.NewWithHTTPClient(apiKey, httpClient, hooks)
	messages.SetBaseURL(resolved)
	return &Provider{
		ChatCompatible: chat,
		messages:       messages,
		messagesModels: loadMessagesModels(),
	}
}

// loadMessagesModels returns the set of model IDs routed to /messages, using the
// OPENCODE_GO_MESSAGES_MODELS override when present.
func loadMessagesModels() map[string]struct{} {
	ids := defaultMessagesModels
	if override := strings.TrimSpace(os.Getenv(messagesModelsEnvVar)); override != "" {
		ids = strings.Split(override, ",")
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			set[id] = struct{}{}
		}
	}
	return set
}

// usesMessages reports whether a model must be served through the Anthropic
// /messages endpoint. It matches on the bare model ID, tolerating an
// "opencode_go/" provider prefix.
func (p *Provider) usesMessages(model string) bool {
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	_, ok := p.messagesModels[model]
	return ok
}

// ChatCompletion routes Anthropic-only models to /messages and everything else
// to /chat/completions.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	if req != nil && p.usesMessages(req.Model) {
		return p.messages.ChatCompletion(ctx, req)
	}
	return p.ChatCompatible.ChatCompletion(ctx, req)
}

// StreamChatCompletion mirrors ChatCompletion's per-model endpoint routing.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	if req != nil && p.usesMessages(req.Model) {
		return p.messages.StreamChatCompletion(ctx, req)
	}
	return p.ChatCompatible.StreamChatCompletion(ctx, req)
}

// Responses dispatches through this provider's ChatCompletion so /v1/responses
// honors the per-model /messages routing.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return providers.ResponsesViaChat(ctx, p, req)
}

// StreamResponses dispatches through this provider's streaming ChatCompletion so
// /v1/responses honors the per-model /messages routing.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return providers.StreamResponsesViaChat(ctx, p, req, "opencode_go")
}

// Embeddings returns an error because OpenCode Go does not expose an embeddings endpoint.
func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("opencode_go does not support embeddings", nil)
}
