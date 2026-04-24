package openrouter

import (
	"net/http"
	"os"
	"strings"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
	"gomodel/internal/providers/openai"
)

const (
	defaultBaseURL = "https://openrouter.ai/api/v1"
	defaultSiteURL = "https://gomodel.enterpilot.io"
	defaultAppName = "GoModel"
)

var Registration = providers.Registration{
	Type: "openrouter",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

type Provider struct {
	*openai.CompatibleProvider
	siteURL string
	appName string
}

func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	baseURL := providers.ResolveBaseURL(cfg.BaseURL, defaultBaseURL)
	p := &Provider{
		siteURL: envOrDefault("OPENROUTER_SITE_URL", defaultSiteURL),
		appName: envOrDefault("OPENROUTER_APP_NAME", defaultAppName),
	}
	p.CompatibleProvider = openai.NewCompatibleProvider(cfg.APIKey, opts, openai.CompatibleProviderConfig{
		ProviderName: "openrouter",
		BaseURL:      baseURL,
		SetHeaders:   setHeaders,
	})
	p.SetRequestMutator(p.mutateRequest)
	return p
}

func NewWithHTTPClient(apiKey string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	p := &Provider{
		siteURL: envOrDefault("OPENROUTER_SITE_URL", defaultSiteURL),
		appName: envOrDefault("OPENROUTER_APP_NAME", defaultAppName),
	}
	p.CompatibleProvider = openai.NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, openai.CompatibleProviderConfig{
		ProviderName: "openrouter",
		BaseURL:      defaultBaseURL,
		SetHeaders:   setHeaders,
	})
	p.SetRequestMutator(p.mutateRequest)
	return p
}

func (p *Provider) mutateRequest(req *llmclient.Request) {
	if req.Headers == nil {
		req.Headers = make(http.Header)
	}
	if strings.TrimSpace(headerValue(req.Headers, "HTTP-Referer")) == "" && strings.TrimSpace(p.siteURL) != "" {
		req.Headers.Set("HTTP-Referer", p.siteURL)
	}
	if strings.TrimSpace(headerValue(req.Headers, "X-OpenRouter-Title")) == "" &&
		strings.TrimSpace(headerValue(req.Headers, "X-Title")) == "" &&
		strings.TrimSpace(p.appName) != "" {
		req.Headers.Set("X-OpenRouter-Title", p.appName)
	}
}

func setHeaders(req *http.Request, apiKey string) {
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if requestID := core.GetRequestID(req.Context()); requestID != "" && isValidClientRequestID(requestID) {
		req.Header.Set("X-Client-Request-Id", requestID)
	}
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func headerValue(headers http.Header, key string) string {
	for existingKey, values := range headers {
		if !strings.EqualFold(existingKey, key) || len(values) == 0 {
			continue
		}
		return values[0]
	}
	return ""
}

func isValidClientRequestID(id string) bool {
	if len(id) > 512 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] > 127 {
			return false
		}
	}
	return true
}
