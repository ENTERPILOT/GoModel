// Package openai provides OpenAI API integration for the LLM gateway.
package openai

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

// Registration provides factory registration for the OpenAI provider.
var Registration = providers.Registration{
	Type:                        "openai",
	New:                         New,
	PassthroughSemanticEnricher: passthroughSemanticEnricher,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

const (
	defaultBaseURL = "https://api.openai.com/v1"
)

// Provider implements the core.Provider interface for OpenAI.
// Credentials and the realtime base URL are both read live from the embedded
// CompatibleProvider, so SetBaseURL overrides and key rotation are honored on
// the realtime websocket dial target too (see realtime.go).
type Provider struct {
	*CompatibleProvider
}

// New creates a new OpenAI provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	baseURL := providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL)
	return &Provider{
		CompatibleProvider: NewCompatibleProvider(cfg.APIKey, opts, CompatibleProviderConfig{
			ProviderName:     "openai",
			BaseURL:          baseURL,
			SetHeaders:       setHeaders,
			AdaptChatRequest: adaptChatRequest,
		}),
	}
}

// NewWithHTTPClient creates a new OpenAI provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	return &Provider{
		CompatibleProvider: NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, CompatibleProviderConfig{
			ProviderName:     "openai",
			BaseURL:          defaultBaseURL,
			SetHeaders:       setHeaders,
			AdaptChatRequest: adaptChatRequest,
		}),
	}
}

// setHeaders sets the required headers for OpenAI API requests.
// OpenAI requires the request ID to be ASCII-only and at most 512 bytes,
// otherwise it returns 400, so forwarding is gated by
// providers.IsValidClientRequestID.
func setHeaders(req *http.Request, apiKey string) {
	providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{
		AuthScheme:        "Bearer ",
		RequestIDHeader:   "X-Client-Request-Id",
		ValidateRequestID: providers.IsValidClientRequestID,
	})
}

// isOSeriesModel reports whether the model is an OpenAI o-series model
// (o1, o3, o4) that requires max_completion_tokens instead of max_tokens
// and does not support the temperature parameter.
func isOSeriesModel(model string) bool {
	m := strings.ToLower(model)
	// Match o1, o3, o4 families (e.g. o3-mini, o4-mini, o3, o1-preview).
	// Non-reasoning models like gpt-4o start with "gpt-", not "o".
	return len(m) >= 2 && m[0] == 'o' && m[1] >= '0' && m[1] <= '9'
}

// isGPT5Model reports whether the model belongs to the GPT-5 family. Both the
// hyphenated variants (gpt-5-mini) and the dot-versioned releases
// (gpt-5.1, gpt-5.6-terra) follow the reasoning chat parameter rules, while
// unrelated names that merely start with the same bytes (gpt-50) do not.
func isGPT5Model(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	rest, ok := strings.CutPrefix(m, "gpt-5")
	if !ok {
		return false
	}
	return rest == "" || rest[0] == '-' || rest[0] == '.'
}

// isReasoningChatModel reports whether the model follows OpenAI's reasoning
// chat parameter rules for max_completion_tokens and temperature handling.
func isReasoningChatModel(model string) bool {
	return isOSeriesModel(model) || isGPT5Model(model)
}

// adaptForReasoningChat rewrites a ChatRequest body for OpenAI reasoning chat
// models, mapping max_tokens -> max_completion_tokens and dropping temperature
// while preserving all unknown top-level JSON fields. It works on the typed
// request directly so the body is marshaled only once, by the HTTP client.
func adaptForReasoningChat(req *core.ChatRequest) (any, error) {
	adapted := *req
	adapted.Temperature = nil
	if req.MaxTokens != nil {
		adapted.MaxTokens = nil
		extra, err := core.MergeUnknownJSONFields(req.ExtraFields, map[string]json.RawMessage{
			"max_completion_tokens": json.RawMessage(strconv.Itoa(*req.MaxTokens)),
		})
		if err != nil {
			return nil, core.NewInvalidRequestError("failed to adapt reasoning request: "+err.Error(), err)
		}
		adapted.ExtraFields = extra
	}
	return &adapted, nil
}

// isNonReasoningChatModel reports whether the model belongs to an OpenAI
// family that rejects reasoning_effort on Chat Completions (gpt-3.5, gpt-4*,
// chatgpt-4o). Unknown models are not included: custom OpenAI-compatible
// endpoints serve arbitrary names and may accept the field.
func isNonReasoningChatModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(m, "gpt-3.5") || strings.HasPrefix(m, "gpt-4") || strings.HasPrefix(m, "chatgpt-")
}

// adaptChatRequest maps GoModel's nested reasoning shape (set by the Messages
// API's thinking and by clients sending reasoning.effort) onto the flat
// reasoning_effort field: OpenAI Chat Completions rejects "reasoning".
// Models that cannot reason reject reasoning_effort too, so it is dropped.
func adaptChatRequest(req *core.ChatRequest) (*core.ChatRequest, error) {
	if req == nil || req.Reasoning == nil {
		return req, nil
	}
	effort := strings.TrimSpace(req.Reasoning.Effort)
	if effort == "" || isNonReasoningChatModel(req.Model) {
		return providers.DropReasoning(req), nil
	}
	return providers.AdaptReasoningEffortRequest(req, effort)
}

// chatRequestBody returns the appropriate request body for the model.
// Reasoning models get parameter adaptation; others pass through as-is.
func chatRequestBody(req *core.ChatRequest) (any, error) {
	if isReasoningChatModel(req.Model) {
		return adaptForReasoningChat(req)
	}
	return req, nil
}
