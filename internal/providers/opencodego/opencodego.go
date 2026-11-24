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
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/anthropic"
	"github.com/enterpilot/gomodel/internal/providers/deepseek"
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
// OpenCode Zen exposes three upstream dialects behind one base URL:
// OpenAI-style /chat/completions (most models), OpenAI-style /responses (the
// models Zen publishes as Responses-native), and Anthropic-style /messages (a
// few models /chat/completions rejects). GET /models names each model's native
// dialect in its "api" member (see routes.go), and the provider routes every
// request — chat or Responses — to it: the embedded ChatCompatible handles chat
// and translates Responses to chat, while the Responses route forwards natively
// for the models Zen serves there and chat requests to a Responses-only model
// are translated the other way. The /messages models are delegated to an
// Anthropic provider pinned to the same base URL, mirroring how GoModel already
// serves /v1/messages for native Anthropic. Every path normalizes to the
// canonical OpenAI-shaped response, so callers see one consistent surface.
type Provider struct {
	*openai.ChatCompatible
	messages       messagesProvider
	messagesModels map[string]struct{}
	// routes holds the API each model is served on, as GET /models publishes
	// it.
	routes *modelRoutes
	// adaptChat is the same hook ChatCompatible applies to chat requests. The
	// Responses-only route bypasses CompatibleProvider.ChatCompletion, so it
	// must apply the hook itself: the upstream rejects requests that carry no
	// reasoning parameter and needs the flat reasoning_effort shape.
	adaptChat func(*core.ChatRequest) (*core.ChatRequest, error)
	// adaptResponses is the same reasoning policy for a request forwarded
	// natively to /responses, which reaches neither AdaptChatRequest nor the
	// chat translation (see reasoning.go).
	adaptResponses func(*core.ResponsesRequest) *core.ResponsesRequest
	// headers yields the session/client identification headers (session.go).
	// Typed requests receive them through the ChatCompatible and Anthropic
	// hooks; Passthrough merges them under the caller's opaque headers.
	headers func(context.Context) http.Header
}

var _ core.Provider = (*Provider)(nil)

// New creates a new OpenCode Go provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	baseURL := providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL)
	headers := requestHeaders(loadSessionHeaderEnabled())
	defaultEffort := loadDefaultReasoningEffort()
	adaptChat := chatRequestAdapter(defaultEffort, deepseek.LoadJSONSchemaMode())
	chat := openai.NewChatCompatible(cfg.APIKey, opts, compatibleConfig(baseURL, headers, adaptChat))
	// opts carries the shared keyring, so the /messages client rotates in step
	// with the chat client above rather than pinning the primary key.
	messages := anthropic.New(providers.ProviderConfig{APIKey: cfg.APIKey, APIKeys: cfg.APIKeys, BaseURL: baseURL}, opts)
	if native, ok := messages.(*anthropic.Provider); ok {
		native.SetRequestHeaders(headers)
	}
	return &Provider{
		ChatCompatible: chat,
		messages:       messages,
		messagesModels: loadMessagesModels(),
		routes:         &modelRoutes{},
		adaptChat:      adaptChat,
		adaptResponses: adaptResponsesRequest(defaultEffort),
		headers:        headers,
	}
}

// compatibleConfig describes the OpenAI-compatible half of the provider, which
// serves /chat/completions and /responses. The AdaptChatRequest hook carries
// OpenCode Zen's reasoning quirk (see reasoning.go) plus DeepSeek's rules for
// DeepSeek models (see deepseek.go); ChatRequestHeaders and
// ResponsesRequestHeaders carry the session/client identification headers (see
// session.go) on their own dialect, so a model reaches either endpoint with the
// same identification.
func compatibleConfig(baseURL string, headers func(context.Context) http.Header, adaptChat func(*core.ChatRequest) (*core.ChatRequest, error)) openai.CompatibleProviderConfig {
	return openai.CompatibleProviderConfig{
		ProviderName:            "opencode_go",
		BaseURL:                 baseURL,
		AdaptChatRequest:        adaptChat,
		ChatRequestHeaders:      chatRequestHeaders(headers),
		ResponsesRequestHeaders: responsesRequestHeaders(headers),
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
	_, ok := p.messagesModels[bareModel(model)]
	return ok
}

// ChatCompletion routes each model to the one API OpenCode Zen serves it on:
// /messages for the Anthropic-only set, /responses for a Responses-only model,
// and /chat/completions for everything else.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	if req != nil {
		if p.usesMessages(req.Model) {
			return p.messages.ChatCompletion(ctx, req)
		}
		if p.responsesOnly(ctx, req.Model) {
			return p.chatCompletionViaResponses(ctx, req)
		}
	}
	return p.ChatCompatible.ChatCompletion(ctx, req)
}

// chatCompletionViaResponses serves a chat request for a model OpenCode Zen
// answers only on /responses, rendering the result back as a chat completion.
func (p *Provider) chatCompletionViaResponses(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	adapted, err := p.chatRequestForResponses(req)
	if err != nil {
		return nil, err
	}
	return openai.ChatViaResponses(ctx, p.Compatible(), adapted, p.ProviderName())
}

// chatRequestForResponses prepares a chat request for ChatViaResponses, which
// bypasses CompatibleProvider.ChatCompletion: it applies the chat adapter, then
// narrows an allowed_tools choice the way that path does, because the Responses
// translation rejects every choice that is not a plain function.
func (p *Provider) chatRequestForResponses(req *core.ChatRequest) (*core.ChatRequest, error) {
	adapted, err := p.adaptChat(req)
	if err != nil {
		return nil, err
	}
	adapted = withClientReasoningSummary(req, adapted)
	if _, _, ok := core.AllowedToolsChoice(adapted.ToolChoice); !ok {
		return adapted, nil
	}
	tools, choice, err := core.NarrowToAllowedTools(adapted.Tools, adapted.ToolChoice)
	if err != nil {
		return nil, err
	}
	narrowed := *adapted
	narrowed.Tools = tools
	narrowed.ToolChoice = choice
	return &narrowed, nil
}

// withClientReasoningSummary restores the reasoning summary the chat adapter
// drops. The chat dialect carries the effort as a flat reasoning_effort and has
// no nested reasoning object, so adaptChatRequest clears it; the Responses
// translation reads the summary from that object and would otherwise replace an
// explicit one with "auto". The caller's summary rides on the nested object only,
// leaving the flat effort authoritative for the translation.
func withClientReasoningSummary(original, adapted *core.ChatRequest) *core.ChatRequest {
	if original == nil || adapted == nil || original.Reasoning == nil {
		return adapted
	}
	summary := strings.TrimSpace(original.Reasoning.Summary)
	if summary == "" {
		return adapted
	}
	reasoning := core.Reasoning{}
	if adapted.Reasoning != nil {
		reasoning = *adapted.Reasoning
	}
	if reasoning.Summary == summary {
		return adapted
	}
	reasoning.Summary = summary
	restored := *adapted
	restored.Reasoning = &reasoning
	return &restored
}

// StreamChatCompletion mirrors ChatCompletion's per-model endpoint routing.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	if req != nil {
		if p.usesMessages(req.Model) {
			return p.messages.StreamChatCompletion(ctx, req)
		}
		if p.responsesOnly(ctx, req.Model) {
			adapted, err := p.chatRequestForResponses(req)
			if err != nil {
				return nil, err
			}
			return openai.StreamChatViaResponses(ctx, p.Compatible(), adapted, p.ProviderName())
		}
	}
	return p.ChatCompatible.StreamChatCompletion(ctx, req)
}

// Responses forwards natively for a model OpenCode Zen serves on /responses,
// and translates to the model's own chat dialect otherwise, so /v1/responses
// honors the per-model routing in both directions.
//
// The manual /messages set wins over the listing: OPENCODE_GO_MESSAGES_MODELS is
// the override that keeps a model on the Anthropic-native endpoint even when Zen
// publishes it on /responses, mirroring ChatCompletion.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	if req != nil && !p.usesMessages(req.Model) && p.servesResponses(ctx, req.Model) {
		return p.Compatible().Responses(ctx, p.adaptResponses(req))
	}
	return providers.ResponsesViaChat(ctx, p, req, p.ProviderName())
}

// StreamResponses is Responses for streaming requests.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	if req != nil && !p.usesMessages(req.Model) && p.servesResponses(ctx, req.Model) {
		return p.Compatible().StreamResponses(ctx, p.adaptResponses(req))
	}
	return providers.StreamResponsesViaChat(ctx, p, req, p.ProviderName())
}

// Passthrough forwards an opaque request with the identification headers
// filled in wherever the caller did not send its own. Passthrough is opaque
// by contract, so a caller-provided x-opencode-session, x-opencode-client, or
// User-Agent always wins over the gateway's value.
func (p *Provider) Passthrough(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	if req == nil || p.headers == nil {
		return p.ChatCompatible.Passthrough(ctx, req)
	}
	merged := *req
	merged.Headers = withDefaultHeaders(req.Headers, p.headers(ctx))
	return p.ChatCompatible.Passthrough(ctx, &merged)
}

// withDefaultHeaders returns a copy of headers with every entry of defaults
// added that headers does not already carry. Names are compared
// case-insensitively so a non-canonical caller key still counts as present.
func withDefaultHeaders(headers, defaults http.Header) http.Header {
	merged := headers.Clone()
	if merged == nil {
		merged = make(http.Header, len(defaults))
	}
	for name, values := range defaults {
		if hasHeader(merged, name) {
			continue
		}
		merged[http.CanonicalHeaderKey(name)] = values
	}
	return merged
}

// hasHeader reports whether headers carries a non-empty entry for name under
// any casing of the key.
func hasHeader(headers http.Header, name string) bool {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return true
		}
	}
	return false
}

// Embeddings returns an error because OpenCode Go does not expose an embeddings endpoint.
func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("opencode_go does not support embeddings", nil)
}
