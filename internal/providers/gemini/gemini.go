// Package gemini provides Google Gemini API integration for the LLM gateway.
package gemini

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/httpclient"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/googlecommon"
)

// Registration provides factory registration for the Gemini provider.
var Registration = providers.Registration{
	Type: "gemini",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultOpenAICompatibleBaseURL,
		// An AI Studio Gemini takes a plain API key; pointing the same adapter
		// at Vertex swaps that for Google credentials, so the key is optional
		// and the Vertex fields stay out of the way until they are needed.
		CredentialFields: []providers.CredentialField{
			{Name: providers.CredentialFieldAPIKeys},
			{Name: providers.CredentialFieldBackend, Options: []string{geminiBackendAIStudio, geminiBackendVertex}},
			{Name: providers.CredentialFieldBaseURL, Advanced: true},
			{Name: providers.CredentialFieldAPIMode, Advanced: true, Options: []string{geminiAPIModeNative, geminiAPIModeOpenAICompatible}},
			{Name: providers.CredentialFieldAuthType, Advanced: true, Options: []string{geminiAuthTypeAPIKey, geminiAuthTypeGCPADC, geminiAuthTypeServiceKey}},
			{Name: providers.CredentialFieldVertexProject, Advanced: true},
			{Name: providers.CredentialFieldVertexLocation, Advanced: true},
			{Name: providers.CredentialFieldServiceAccountJSON, Advanced: true},
			{Name: providers.CredentialFieldServiceAccountFile, Advanced: true},
			{Name: providers.CredentialFieldServiceAccountJSONBase64, Advanced: true},
			{Name: providers.CredentialFieldGCPScope, Advanced: true},
		},
	},
}

const (
	// Gemini provides an OpenAI-compatible endpoint
	defaultOpenAICompatibleBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"
	// Native Gemini API endpoint for generateContent and models listing
	defaultModelsBaseURL     = "https://generativelanguage.googleapis.com/v1beta"
	useNativeAPIEnvVar       = "USE_GOOGLE_GEMINI_NATIVE_API"
	geminiBackendAIStudio    = "aistudio"
	geminiBackendVertex      = "vertex"
	geminiAuthTypeAPIKey     = "api_key"
	geminiAuthTypeGCPADC     = "gcp_adc"
	geminiAuthTypeServiceKey = "gcp_service_account"
	// The api_mode values the docs and config examples use; useNativeAPI
	// accepts further spellings of each.
	geminiAPIModeNative           = "native"
	geminiAPIModeOpenAICompatible = "openai_compatible"
	geminiCacheTTL                = 5 * time.Minute
	geminiCacheFreshness          = 15 * time.Second
	geminiCacheFailureBackoff     = 30 * time.Second
	geminiCacheCreateTimeout      = 30 * time.Second
	geminiCacheObjectLimit        = 1024
)

// Provider implements the core.Provider interface for Google Gemini
type Provider struct {
	client       *llmclient.Client
	nativeClient *llmclient.Client
	modelsClient *llmclient.Client
	keys         *providers.Keyring
	backend      string
	authType     string
	useNativeAPI bool
	modelsURL    string
	configErr    error
	cacheMu      sync.Mutex
	cacheObjects map[string]geminiCacheObject
	cacheFlight  singleflight.Group
}

// New creates a new Gemini provider.
func New(providerCfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return newProvider(providerCfg, opts, nil, false)
}

// NewVertexWithHTTPClient creates a Vertex-configured Gemini provider using an
// already-authenticated HTTP client. It is used by the vertex package so Vertex
// owns Google auth while reusing Gemini request/response translation.
func NewVertexWithHTTPClient(providerCfg providers.ProviderConfig, opts providers.ProviderOptions, httpClient *http.Client) *Provider {
	providerCfg.Backend = geminiBackendVertex
	return newProvider(providerCfg, opts, httpClient, true)
}

func newProvider(providerCfg providers.ProviderConfig, opts providers.ProviderOptions, httpClient *http.Client, preauthenticated bool) *Provider {
	backend := normalizeGeminiBackend(providerCfg)
	authType := normalizeGeminiAuthType(backend, providerCfg)
	baseURL, nativeBaseURL := geminiBaseURLs(providerCfg, backend)
	modelsURL := geminiModelsBaseURL(backend, nativeBaseURL)
	p := &Provider{
		keys:         opts.Keyring(providerCfg.APIKey),
		backend:      backend,
		authType:     authType,
		useNativeAPI: useNativeAPI(providerCfg.APIMode),
		modelsURL:    modelsURL,
	}
	p.validateConfig(providerCfg)
	if !preauthenticated {
		httpClient = p.authHTTPClient(providerCfg, httpClient)
	}

	clientProviderName := "gemini"
	if backend == geminiBackendVertex {
		clientProviderName = "vertex"
	}
	clientCfg := llmclient.Config{
		ProviderName:   clientProviderName,
		BaseURL:        baseURL,
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}
	nativeCfg := clientCfg
	nativeCfg.BaseURL = nativeBaseURL
	modelsCfg := clientCfg
	modelsCfg.BaseURL = modelsURL
	if httpClient != nil {
		p.client = llmclient.NewWithHTTPClient(httpClient, clientCfg, p.setHeaders)
		p.nativeClient = llmclient.NewWithHTTPClient(httpClient, nativeCfg, p.setNativeHeaders)
		p.modelsClient = llmclient.NewWithHTTPClient(httpClient, modelsCfg, p.setNativeHeaders)
		return p
	}
	p.client = llmclient.New(clientCfg, p.setHeaders)
	p.nativeClient = llmclient.New(nativeCfg, p.setNativeHeaders)
	p.modelsClient = llmclient.New(modelsCfg, p.setNativeHeaders)
	return p
}

// NewWithHTTPClient creates a new Gemini provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	providerCfg := providers.ProviderConfig{APIKey: apiKey}
	baseURL, nativeBaseURL := geminiBaseURLs(providerCfg, geminiBackendAIStudio)
	modelsURL := geminiModelsBaseURL(geminiBackendAIStudio, nativeBaseURL)
	p := &Provider{
		keys:         providers.NewKeyring(apiKey),
		backend:      geminiBackendAIStudio,
		authType:     geminiAuthTypeAPIKey,
		useNativeAPI: useNativeAPIFromEnv(),
		modelsURL:    modelsURL,
	}
	modelsCfg := llmclient.DefaultConfig("gemini", modelsURL)
	modelsCfg.Hooks = hooks
	cfg := llmclient.DefaultConfig("gemini", baseURL)
	cfg.Hooks = hooks
	nativeCfg := llmclient.DefaultConfig("gemini", nativeBaseURL)
	nativeCfg.Hooks = hooks
	p.client = llmclient.NewWithHTTPClient(httpClient, cfg, p.setHeaders)
	p.nativeClient = llmclient.NewWithHTTPClient(httpClient, nativeCfg, p.setNativeHeaders)
	p.modelsClient = llmclient.NewWithHTTPClient(httpClient, modelsCfg, p.setNativeHeaders)
	return p
}

// SetBaseURL allows configuring a custom base URL for the provider
func (p *Provider) SetBaseURL(url string) {
	baseURL, nativeBaseURL := geminiBaseURLs(providers.ProviderConfig{BaseURL: url}, p.backend)
	modelsURL := geminiModelsBaseURL(p.backend, nativeBaseURL)
	p.client.SetBaseURL(baseURL)
	p.modelsURL = modelsURL
	if p.nativeClient != nil {
		p.nativeClient.SetBaseURL(nativeBaseURL)
	}
	if p.modelsClient != nil {
		p.modelsClient.SetBaseURL(modelsURL)
	}
}

// SetModelsURL allows configuring a custom models API base URL.
// This is primarily useful for tests and local emulators.
func (p *Provider) SetModelsURL(url string) {
	p.modelsURL = url
	if p.nativeClient != nil {
		p.nativeClient.SetBaseURL(url)
	}
	if p.modelsClient != nil {
		p.modelsClient.SetBaseURL(url)
	}
}

func (p *Provider) validateConfig(providerCfg providers.ProviderConfig) {
	if p.backend == geminiBackendVertex && p.authType == geminiAuthTypeAPIKey {
		p.configErr = fmt.Errorf("vertex Gemini requires gcp_adc or gcp_service_account auth")
		return
	}
	if p.backend == geminiBackendVertex && !providers.HasResolvedProviderValue(providerCfg.BaseURL) &&
		(!providers.HasResolvedProviderValue(providerCfg.VertexProject) || !providers.HasResolvedProviderValue(providerCfg.VertexLocation)) {
		p.configErr = fmt.Errorf("vertex Gemini requires base_url or vertex_project and vertex_location")
		return
	}
	if p.backend == geminiBackendAIStudio && p.authType != geminiAuthTypeAPIKey {
		p.configErr = fmt.Errorf("ai studio backend does not support GCP auth; use Vertex backend or provide an API key")
		return
	}
	if p.backend == geminiBackendAIStudio && p.authType == geminiAuthTypeAPIKey && strings.TrimSpace(providerCfg.APIKey) == "" {
		p.configErr = fmt.Errorf("gemini API key is required")
	}
}

func (p *Provider) authHTTPClient(providerCfg providers.ProviderConfig, base *http.Client) *http.Client {
	if p.configErr != nil || p.authType == geminiAuthTypeAPIKey {
		return base
	}
	creds, err := googlecommon.FindCredentials(context.Background(), googlecommon.Config{
		AuthType:                 p.authType,
		ServiceAccountFile:       providerCfg.ServiceAccountFile,
		ServiceAccountJSON:       providerCfg.ServiceAccountJSON,
		ServiceAccountJSONBase64: providerCfg.ServiceAccountJSONBase64,
		Scope:                    providerCfg.GCPScope,
	})
	if err != nil {
		p.configErr = err
		return base
	}
	if base == nil {
		base = httpclient.NewDefaultHTTPClient()
	}
	quotaProject := creds.QuotaProjectID
	if strings.TrimSpace(quotaProject) == "" {
		quotaProject = strings.TrimSpace(providerCfg.VertexProject)
	}
	return googlecommon.HTTPClient(base, creds.TokenSource, quotaProject)
}

func (p *Provider) ready() error {
	if p.configErr == nil {
		return nil
	}
	return core.NewProviderError(p.responseProviderName(), http.StatusBadGateway, "invalid Gemini provider configuration: "+p.configErr.Error(), p.configErr)
}

func (p *Provider) responseProviderName() string {
	if p.backend == geminiBackendVertex {
		return "vertex"
	}
	return "gemini"
}

// setHeaders sets the required headers for Gemini API requests.
// Vertex backends authenticate through a token source on the HTTP client
// instead, so only the API-key path consumes the rotation.
func (p *Provider) setHeaders(req *http.Request) {
	if p.authType == geminiAuthTypeAPIKey {
		req.Header.Set("Authorization", "Bearer "+p.keys.NextForContext(req.Context()))
	}

	// Forward request ID if present in context for request tracing
	if requestID := core.GetRequestID(req.Context()); requestID != "" {
		req.Header.Set("X-Request-Id", requestID)
	}
}

// setNativeHeaders sets the required headers for Gemini native API requests.
func (p *Provider) setNativeHeaders(req *http.Request) {
	if p.authType == geminiAuthTypeAPIKey {
		req.Header.Set("x-goog-api-key", p.keys.NextForContext(req.Context()))
	}

	if requestID := core.GetRequestID(req.Context()); requestID != "" {
		req.Header.Set("X-Request-Id", requestID)
	}
}

func normalizeGeminiBackend(cfg providers.ProviderConfig) string {
	backend := strings.ToLower(strings.TrimSpace(cfg.Backend))
	switch backend {
	case geminiBackendVertex:
		return geminiBackendVertex
	case geminiBackendAIStudio, "ai_studio", "developer", "developer_api":
		return geminiBackendAIStudio
	}
	if strings.TrimSpace(cfg.VertexProject) != "" ||
		strings.TrimSpace(cfg.VertexLocation) != "" {
		return geminiBackendVertex
	}
	return geminiBackendAIStudio
}

func normalizeGeminiAuthType(backend string, cfg providers.ProviderConfig) string {
	authType := strings.ToLower(strings.TrimSpace(cfg.AuthType))
	switch authType {
	case "":
		if backend == geminiBackendVertex {
			return googlecommon.NormalizeAuthType(authType, googlecommon.HasServiceAccount(googlecommon.Config{
				ServiceAccountFile:       cfg.ServiceAccountFile,
				ServiceAccountJSON:       cfg.ServiceAccountJSON,
				ServiceAccountJSONBase64: cfg.ServiceAccountJSONBase64,
			}))
		}
		return geminiAuthTypeAPIKey
	case "api_key", "key":
		return geminiAuthTypeAPIKey
	case "gcp_adc", "adc", "google_adc":
		return geminiAuthTypeGCPADC
	case "gcp_service_account", "service_account":
		return geminiAuthTypeServiceKey
	default:
		return authType
	}
}

func useNativeAPI(apiMode string) bool {
	switch strings.ToLower(strings.TrimSpace(apiMode)) {
	case geminiAPIModeNative, "gemini_native", "generate_content":
		return true
	case geminiAPIModeOpenAICompatible, "openai", "openai-compatible", "compat", "compatible":
		return false
	case "":
		return useNativeAPIFromEnv()
	default:
		return useNativeAPIFromEnv()
	}
}

func useNativeAPIFromEnv() bool {
	value, ok := os.LookupEnv(useNativeAPIEnvVar)
	if !ok || strings.TrimSpace(value) == "" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func geminiBaseURLs(providerCfg providers.ProviderConfig, backend string) (openAICompatibleBaseURL, nativeBaseURL string) {
	if backend == geminiBackendVertex {
		return googlecommon.VertexBaseURLs(providerCfg.BaseURL, providerCfg.VertexProject, providerCfg.VertexLocation)
	}
	configuredBaseURL := providerCfg.BaseURL
	baseURL := strings.TrimRight(strings.TrimSpace(configuredBaseURL), "/")
	if baseURL == "" {
		return defaultOpenAICompatibleBaseURL, defaultModelsBaseURL
	}
	if baseURL == defaultOpenAICompatibleBaseURL {
		return defaultOpenAICompatibleBaseURL, defaultModelsBaseURL
	}
	if baseURL == defaultModelsBaseURL {
		return defaultOpenAICompatibleBaseURL, defaultModelsBaseURL
	}
	if nativeBaseURL, ok := nativeBaseURLFromOpenAICompatibleBaseURL(baseURL); ok {
		return baseURL, nativeBaseURL
	}
	return baseURL, baseURL
}

func geminiModelsBaseURL(backend, nativeBaseURL string) string {
	nativeBaseURL = strings.TrimRight(strings.TrimSpace(nativeBaseURL), "/")
	if backend != geminiBackendVertex {
		return nativeBaseURL
	}
	if modelsURL, ok := vertexPublisherModelsBaseURL(nativeBaseURL); ok {
		return modelsURL
	}
	return nativeBaseURL
}

func vertexPublisherModelsBaseURL(nativeBaseURL string) (string, bool) {
	const projectsPath = "/v1/projects/"
	nativeBaseURL = strings.TrimRight(strings.TrimSpace(nativeBaseURL), "/")
	before, _, ok := strings.Cut(nativeBaseURL, projectsPath)
	if !ok {
		return "", false
	}
	root := strings.TrimRight(before, "/")
	if root == "" {
		return "", false
	}
	return root + "/v1beta1/publishers/google", true
}

func nativeBaseURLFromOpenAICompatibleBaseURL(baseURL string) (string, bool) {
	const suffix = "/openai"
	if !strings.HasSuffix(baseURL, suffix) {
		return "", false
	}
	nativeBaseURL := strings.TrimRight(strings.TrimSuffix(baseURL, suffix), "/")
	if nativeBaseURL == "" {
		return "", false
	}
	return nativeBaseURL, true
}
