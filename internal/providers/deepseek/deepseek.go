// Package deepseek provides DeepSeek API integration for the LLM gateway.
package deepseek

import (
	"context"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
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
// the DeepSeek compatibility rules in compat.go; DeepSeek does not expose an
// embeddings endpoint.
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

func compatibleConfig(baseURL string) openai.CompatibleProviderConfig {
	return openai.CompatibleProviderConfig{
		ProviderName:     "deepseek",
		BaseURL:          baseURL,
		SetHeaders:       setHeaders,
		AdaptChatRequest: adaptChatRequest(LoadJSONSchemaMode()),
	}
}

func setHeaders(req *http.Request, apiKey string) {
	providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{
		AuthScheme:      "Bearer ",
		RequestIDHeader: "X-Request-Id",
	})
}

// adaptChatRequest returns the hook applying DeepSeek's OpenAI-compatible chat
// extensions.
func adaptChatRequest(jsonSchemaMode JSONSchemaMode) func(*core.ChatRequest) (*core.ChatRequest, error) {
	return func(req *core.ChatRequest) (*core.ChatRequest, error) {
		if req == nil {
			return req, nil
		}

		adapted := req
		var err error
		if req.Reasoning != nil && strings.TrimSpace(req.Reasoning.Effort) != "" {
			adapted, err = providers.AdaptReasoningEffortRequest(req, normalizeReasoningEffort(req.Reasoning.Effort))
			if err != nil {
				return nil, err
			}
		}

		return AdaptCompatibility(adapted, "deepseek", jsonSchemaMode)
	}
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
