// Package orcarouter provides OrcaRouter integration for the LLM gateway.
package orcarouter

import (
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const defaultBaseURL = "https://api.orcarouter.ai/v1"

// Registration provides factory registration for the OrcaRouter provider.
var Registration = providers.Registration{
	Type:                        "orcarouter",
	New:                         New,
	PassthroughSemanticEnricher: passthroughSemanticEnricher,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

// Provider implements the core.Provider interface for OrcaRouter.
// OrcaRouter is an OpenAI-compatible AI gateway that routes to ~190 upstream
// models under provider-scoped IDs (e.g. "openai/gpt-4o-mini"); those IDs pass
// through unchanged. Model listing, chat completions, and responses all follow
// the OpenAI surface, so the shared OpenAI-compatible transport is embedded
// wholesale and only authentication is specialized.
type Provider struct {
	*openai.CompatibleProvider
}

var _ core.Provider = (*Provider)(nil)

// New creates a new OrcaRouter provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return &Provider{openai.NewCompatibleProvider(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName: "orcarouter",
		BaseURL:      providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL),
		SetHeaders:   setHeaders,
	})}
}

// NewWithHTTPClient creates a new OrcaRouter provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	return &Provider{openai.NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, openai.CompatibleProviderConfig{
		ProviderName: "orcarouter",
		BaseURL:      defaultBaseURL,
		SetHeaders:   setHeaders,
	})}
}

// setHeaders applies OrcaRouter's bearer-token authentication and forwards the
// gateway's request ID and session ID so routed traffic keeps conversation
// affinity end to end.
func setHeaders(req *http.Request, apiKey string) {
	providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{
		AuthScheme:        "Bearer ",
		RequestIDHeader:   "X-Client-Request-Id",
		ValidateRequestID: providers.IsValidClientRequestID,
	})
	// The session ID keeps a conversation on the same resolved model endpoint
	// across requests, maximizing upstream prompt-cache reuse. GoModel's
	// session detector already scopes user-supplied IDs before placing them
	// in context.
	if sessionID := strings.TrimSpace(core.SessionIDFromContext(req.Context())); sessionID != "" {
		req.Header.Set("X-Session-Id", sessionID)
	}
}
