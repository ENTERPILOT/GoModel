// Package deepseek provides DeepSeek API integration for the LLM gateway.
package deepseek

import (
	"context"
	"net/http"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://api.deepseek.com"

// Registration provides factory registration for the DeepSeek provider.
var Registration = providers.Registration{
	Type:                        "deepseek",
	New:                         New,
	PassthroughSemanticEnricher: passthroughSemanticEnricher,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for DeepSeek. DeepSeek's
// API is OpenAI-compatible, so all transport goes through the shared
// chat-centric adapter. Its request hook handles reasoning-effort mapping and
// tool-call reasoning replay; DeepSeek does not expose an embeddings endpoint.
type Provider struct {
	*openai.ChatCompatible
}

var _ core.Provider = (*Provider)(nil)

// New creates a new DeepSeek provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{
		ChatCompatible: openai.NewChatCompatible(cfg.APIKey, opts, compatibleConfig(providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL))),
	}
}

// NewWithHTTPClient creates a new DeepSeek provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	return &Provider{
		ChatCompatible: openai.NewChatCompatibleWithHTTPClient(apiKey, httpClient, hooks, compatibleConfig(providers.ResolveBaseURL(baseURL, defaultBaseURL))),
	}
}

func compatibleConfig(baseURL string) openai.CompatibleProviderConfig {
	return openai.CompatibleProviderConfig{
		ProviderName:     "deepseek",
		BaseURL:          baseURL,
		SetHeaders:       setHeaders,
		AdaptChatRequest: adaptChatRequest,
	}
}

func setHeaders(req *http.Request, apiKey string) {
	providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{
		AuthScheme:      "Bearer ",
		RequestIDHeader: "X-Request-Id",
	})
}

// adaptChatRequest applies DeepSeek's OpenAI-compatible chat extensions.
func adaptChatRequest(req *core.ChatRequest) (*core.ChatRequest, error) {
	if req == nil {
		return req, nil
	}

	adapted := req
	var err error
	if effort := providers.ResolveReasoningEffort(req); effort != "" {
		adapted, err = providers.AdaptReasoningEffortRequest(req, normalizeReasoningEffort(effort))
		if err != nil {
			return nil, err
		}
	}

	return padMissingToolCallReasoningContent(adapted)
}

// padMissingToolCallReasoningContent satisfies DeepSeek's requirement that assistant
// tool-call messages replay reasoning_content. Virtual-model clients may not
// know that DeepSeek will serve the request, so a non-empty neutral value is
// added when they omit it.
func padMissingToolCallReasoningContent(req *core.ChatRequest) (*core.ChatRequest, error) {
	if len(req.Tools) == 0 {
		return req, nil
	}

	var adapted *core.ChatRequest
	for i, message := range req.Messages {
		if message.Role != "assistant" || len(message.ToolCalls) == 0 || message.ExtraFields.Lookup("reasoning_content") != nil {
			continue
		}

		extra, err := core.MergeUnknownJSONFields(message.ExtraFields, map[string]json.RawMessage{
			"reasoning_content": json.RawMessage(`" "`),
		})
		if err != nil {
			return nil, core.NewInvalidRequestError("failed to adapt DeepSeek tool-call message: "+err.Error(), err)
		}
		if adapted == nil {
			copy := *req
			copy.Messages = append([]core.Message(nil), req.Messages...)
			adapted = &copy
		}
		adapted.Messages[i].ExtraFields = extra
	}

	if adapted == nil {
		return req, nil
	}
	return adapted, nil
}

// normalizeReasoningEffort maps GoModel's OpenAI-style effort levels to the two
// levels DeepSeek V4 accepts ("high" and "max"). "low" and "medium" are mapped
// up to "high" because DeepSeek does not support lower levels; clients that
// want to disable reasoning should omit the field entirely. See
// docs/providers/deepseek.mdx for the user-facing table.
func normalizeReasoningEffort(effort string) string {
	normalized := strings.ToLower(strings.TrimSpace(effort))
	switch normalized {
	case "low", "medium":
		return "high"
	case "xhigh", "max":
		return "max"
	default:
		return normalized
	}
}

// Embeddings returns an error because DeepSeek does not expose an embeddings endpoint.
func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("deepseek does not support embeddings", nil)
}
