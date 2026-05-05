// Package gemini provides Google Gemini API integration for the LLM gateway.
package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"gomodel/internal/core"
	"gomodel/internal/httpclient"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
	"gomodel/internal/providers/googleauth"
)

// Registration provides factory registration for the Gemini provider.
var Registration = providers.Registration{
	Type: "gemini",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultOpenAICompatibleBaseURL,
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
)

// Provider implements the core.Provider interface for Google Gemini
type Provider struct {
	client       *llmclient.Client
	nativeClient *llmclient.Client
	modelsClient *llmclient.Client
	apiKey       string
	backend      string
	authType     string
	useNativeAPI bool
	modelsURL    string
	configErr    error
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
		apiKey:       providerCfg.APIKey,
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
		apiKey:       apiKey,
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
	if p.backend == geminiBackendVertex && !hasResolvedProviderValue(providerCfg.BaseURL) &&
		(!hasResolvedProviderValue(providerCfg.VertexProject) || !hasResolvedProviderValue(providerCfg.VertexLocation)) {
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
	source, err := googleauth.TokenSource(context.Background(), googleauth.Config{
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
	return googleauth.HTTPClient(base, source)
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

// setHeaders sets the required headers for Gemini API requests
func (p *Provider) setHeaders(req *http.Request) {
	if p.authType == geminiAuthTypeAPIKey {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	// Forward request ID if present in context for request tracing
	if requestID := core.GetRequestID(req.Context()); requestID != "" {
		req.Header.Set("X-Request-Id", requestID)
	}
}

// setNativeHeaders sets the required headers for Gemini native API requests.
func (p *Provider) setNativeHeaders(req *http.Request) {
	if p.authType == geminiAuthTypeAPIKey {
		req.Header.Set("x-goog-api-key", p.apiKey)
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

func hasResolvedProviderValue(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.Contains(value, "${")
}

func normalizeGeminiAuthType(backend string, cfg providers.ProviderConfig) string {
	authType := strings.ToLower(strings.TrimSpace(cfg.AuthType))
	switch authType {
	case "":
		if backend == geminiBackendVertex {
			return googleauth.NormalizeAuthType(authType, googleauth.HasServiceAccount(googleauth.Config{
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
	case "native", "gemini_native", "generate_content":
		return true
	case "openai", "openai_compatible", "openai-compatible", "compat", "compatible":
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
		return vertexBaseURLs(providerCfg)
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

func vertexBaseURLs(providerCfg providers.ProviderConfig) (openAICompatibleBaseURL, nativeBaseURL string) {
	baseURL := strings.TrimRight(strings.TrimSpace(providerCfg.BaseURL), "/")
	if baseURL == "" {
		project := strings.TrimSpace(providerCfg.VertexProject)
		location := strings.TrimSpace(providerCfg.VertexLocation)
		root := "https://aiplatform.googleapis.com/v1/projects/" + url.PathEscape(project) + "/locations/" + url.PathEscape(location)
		return root + "/endpoints/openapi", root + "/publishers/google"
	}
	if nativeBaseURL, ok := vertexNativeBaseURLFromOpenAICompatibleBaseURL(baseURL); ok {
		return baseURL, nativeBaseURL
	}
	if openAIBaseURL, ok := vertexOpenAICompatibleBaseURLFromNativeBaseURL(baseURL); ok {
		return openAIBaseURL, baseURL
	}
	return baseURL, baseURL
}

func vertexNativeBaseURLFromOpenAICompatibleBaseURL(baseURL string) (string, bool) {
	const suffix = "/endpoints/openapi"
	if !strings.HasSuffix(baseURL, suffix) {
		return "", false
	}
	root := strings.TrimRight(strings.TrimSuffix(baseURL, suffix), "/")
	if root == "" {
		return "", false
	}
	return root + "/publishers/google", true
}

func vertexOpenAICompatibleBaseURLFromNativeBaseURL(baseURL string) (string, bool) {
	const suffix = "/publishers/google"
	if !strings.HasSuffix(baseURL, suffix) {
		return "", false
	}
	root := strings.TrimRight(strings.TrimSuffix(baseURL, suffix), "/")
	if root == "" {
		return "", false
	}
	return root + "/endpoints/openapi", true
}

func vertexPublisherModelsBaseURL(nativeBaseURL string) (string, bool) {
	const projectsPath = "/v1/projects/"
	nativeBaseURL = strings.TrimRight(strings.TrimSpace(nativeBaseURL), "/")
	idx := strings.Index(nativeBaseURL, projectsPath)
	if idx < 0 {
		return "", false
	}
	root := strings.TrimRight(nativeBaseURL[:idx], "/")
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

// adaptChatRequest rewrites a ChatRequest for Gemini's OpenAI-compatible endpoint.
// Gemini uses "reasoning_effort" as a top-level string (e.g. "low", "medium", "high"),
// not the nested "reasoning": {"effort": "..."} format.
func adaptChatRequest(req *core.ChatRequest) (any, error) {
	if req.Reasoning == nil || req.Reasoning.Effort == "" {
		return req, nil
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to marshal gemini request: "+err.Error(), err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, core.NewInvalidRequestError("failed to decode gemini request payload: "+err.Error(), err)
	}

	effort, _ := json.Marshal(req.Reasoning.Effort)
	raw["reasoning_effort"] = effort
	delete(raw, "reasoning")
	return raw, nil
}

func rewriteRequestModel(body any, model string) (any, error) {
	if strings.TrimSpace(model) == "" {
		return body, nil
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to marshal gemini request: "+err.Error(), err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		return nil, core.NewInvalidRequestError("failed to decode gemini request payload: "+err.Error(), err)
	}
	modelJSON, _ := json.Marshal(model)
	raw["model"] = modelJSON
	return raw, nil
}

func (p *Provider) openAICompatibleChatBody(req *core.ChatRequest) (any, error) {
	body, err := adaptChatRequest(req)
	if err != nil {
		return nil, err
	}
	if p.backend != geminiBackendVertex {
		return body, nil
	}
	return rewriteRequestModel(body, vertexOpenAIModelID(req.Model))
}

func (p *Provider) openAICompatibleEmbeddingBody(req *core.EmbeddingRequest) (any, error) {
	if p.backend != geminiBackendVertex {
		return req, nil
	}
	return rewriteRequestModel(req, vertexOpenAIModelID(req.Model))
}

// ChatCompletion sends a chat completion request to Gemini
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, core.NewInvalidRequestError("chat request is required", nil)
	}
	if p.useNativeAPI {
		return p.nativeChatCompletion(ctx, req)
	}
	body, err := p.openAICompatibleChatBody(req)
	if err != nil {
		return nil, err
	}
	var resp core.ChatResponse
	err = p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     body,
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Model == "" {
		resp.Model = req.Model
	}
	if resp.Provider == "" {
		resp.Provider = p.responseProviderName()
	}
	return &resp, nil
}

func (p *Provider) nativeChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	body, err := convertChatRequestToGemini(req)
	if err != nil {
		return nil, err
	}
	var geminiResp geminiGenerateContentResponse
	err = p.nativeClient.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: nativeGenerateEndpoint(req.Model),
		Body:     body,
	}, &geminiResp)
	if err != nil {
		return nil, err
	}
	return nativeChatResponse(req, &geminiResp, p.responseProviderName())
}

// StreamChatCompletion returns a raw response body for streaming (caller must close)
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, core.NewInvalidRequestError("chat request is required", nil)
	}
	if p.useNativeAPI {
		return p.nativeStreamChatCompletion(ctx, req)
	}
	streamReq := req.WithStreaming()
	body, err := p.openAICompatibleChatBody(streamReq)
	if err != nil {
		return nil, err
	}
	stream, err := p.client.DoStream(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/chat/completions",
		Body:     body,
	})
	if err != nil {
		return nil, err
	}

	// Gemini's OpenAI-compatible endpoint returns OpenAI-format SSE, so we can pass it through directly
	return stream, nil
}

func (p *Provider) nativeStreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	streamReq := req.WithStreaming()
	body, err := convertChatRequestToGemini(streamReq)
	if err != nil {
		return nil, err
	}
	stream, err := p.nativeClient.DoStream(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: nativeStreamEndpoint(req.Model),
		Body:     body,
	})
	if err != nil {
		return nil, err
	}
	includeUsage := streamReq.StreamOptions != nil && streamReq.StreamOptions.IncludeUsage
	return newGeminiNativeStream(stream, req.Model, includeUsage, p.responseProviderName()), nil
}

// geminiModel represents a model in Gemini's native API response
type geminiModel struct {
	Name             string   `json:"name"`
	DisplayName      string   `json:"displayName"`
	Description      string   `json:"description"`
	SupportedMethods []string `json:"supportedGenerationMethods"`
	InputTokenLimit  int      `json:"inputTokenLimit"`
	OutputTokenLimit int      `json:"outputTokenLimit"`
	Temperature      *float64 `json:"temperature,omitempty"`
	TopP             *float64 `json:"topP,omitempty"`
	TopK             *int     `json:"topK,omitempty"`
}

// geminiModelsResponse represents the native Gemini models list response
type geminiModelsResponse struct {
	Models          []geminiModel `json:"models"`
	PublisherModels []geminiModel `json:"publisherModels"`
}

func geminiModelSupportedMethods(modelID string, methods []string) (supportsGenerate, supportsEmbed bool) {
	if len(methods) == 0 {
		normalized := normalizeGeminiModelID(modelID)
		return strings.HasPrefix(normalized, "gemini-"), strings.HasPrefix(normalized, "text-embedding-")
	}
	return slices.Contains(methods, "generateContent") || slices.Contains(methods, "streamGenerateContent"),
		slices.Contains(methods, "embedContent")
}

// ListModels retrieves the list of available models from Gemini
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	modelsClient := p.modelsClient
	if modelsClient == nil {
		modelsClient = p.nativeClient
	}
	rawResp, err := modelsClient.DoRaw(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/models",
	})
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()

	// Preferred path: native Gemini models response.
	// If the payload contains an explicit "models" field with an empty array,
	// return an empty list instead of falling through to fallback parsing.
	var nativeProbe struct {
		Models          json.RawMessage `json:"models"`
		PublisherModels json.RawMessage `json:"publisherModels"`
	}
	if err := json.Unmarshal(rawResp.Body, &nativeProbe); err == nil && (nativeProbe.Models != nil || nativeProbe.PublisherModels != nil) {
		var geminiResp geminiModelsResponse
		if err := json.Unmarshal(rawResp.Body, &geminiResp); err != nil {
			return nil, core.NewProviderError(p.responseProviderName(), http.StatusBadGateway, "failed to parse native Gemini models response", err)
		}
		modelEntries := append(geminiResp.Models, geminiResp.PublisherModels...)
		if len(modelEntries) == 0 {
			return &core.ModelsResponse{
				Object: "list",
				Data:   []core.Model{},
			}, nil
		}

		models := make([]core.Model, 0, len(modelEntries))

		for _, gm := range modelEntries {
			modelID := displayModelIDFromGemini(gm.Name, p.backend)

			// Only include models that support generateContent (chat/completion)
			supportsGenerate, supportsEmbed := geminiModelSupportedMethods(modelID, gm.SupportedMethods)

			isOpenAICompatModel := isGeminiExposedModel(modelID)
			if (supportsGenerate || supportsEmbed) && isOpenAICompatModel {
				models = append(models, core.Model{
					ID:      modelID,
					Object:  "model",
					OwnedBy: "google",
					Created: now,
				})
			}
		}

		return &core.ModelsResponse{
			Object: "list",
			Data:   models,
		}, nil
	}

	// Fallback path: OpenAI-compatible models list.
	var openAIResp core.ModelsResponse
	if err := json.Unmarshal(rawResp.Body, &openAIResp); err == nil && openAIResp.Object == "list" {
		models := make([]core.Model, 0, len(openAIResp.Data))
		for _, m := range openAIResp.Data {
			modelID := displayModelIDFromGemini(m.ID, p.backend)
			isOpenAICompatModel := isGeminiExposedModel(modelID)
			if !isOpenAICompatModel {
				continue
			}
			models = append(models, core.Model{
				ID:      modelID,
				Object:  "model",
				OwnedBy: "google",
				Created: now,
			})
		}
		return &core.ModelsResponse{
			Object: "list",
			Data:   models,
		}, nil
	}

	responsePreview := string(rawResp.Body)
	if len(responsePreview) > 512 {
		responsePreview = responsePreview[:512] + "...(truncated)"
	}
	return nil, core.NewProviderError(p.responseProviderName(), http.StatusBadGateway, "unexpected Gemini models response format", fmt.Errorf("models response body: %s", responsePreview))
}

// Responses sends a Responses API request to Gemini (converted to chat format)
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	return providers.ResponsesViaChat(ctx, p, req)
}

// Embeddings sends an embeddings request to Gemini via its OpenAI-compatible endpoint
func (p *Provider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, core.NewInvalidRequestError("embedding request is required", nil)
	}
	body, err := p.openAICompatibleEmbeddingBody(req)
	if err != nil {
		return nil, err
	}
	var resp core.EmbeddingResponse
	err = p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/embeddings",
		Body:     body,
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Model == "" {
		resp.Model = req.Model
	}
	if resp.Provider == "" {
		resp.Provider = p.responseProviderName()
	}
	return &resp, nil
}

// StreamResponses returns a raw response body for streaming Responses API (caller must close)
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	return providers.StreamResponsesViaChat(ctx, p, req, p.responseProviderName())
}

// CreateBatch creates a native Gemini batch job through its OpenAI-compatible endpoint.
func (p *Provider) CreateBatch(ctx context.Context, req *core.BatchRequest) (*core.BatchResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	var resp core.BatchResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/batches",
		Body:     req,
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.ProviderBatchID == "" {
		resp.ProviderBatchID = resp.ID
	}
	return &resp, nil
}

// GetBatch retrieves a native Gemini batch job.
func (p *Provider) GetBatch(ctx context.Context, id string) (*core.BatchResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	var resp core.BatchResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/batches/" + url.PathEscape(id),
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.ProviderBatchID == "" {
		resp.ProviderBatchID = resp.ID
	}
	return &resp, nil
}

// ListBatches lists native Gemini batch jobs.
func (p *Provider) ListBatches(ctx context.Context, limit int, after string) (*core.BatchListResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	values := url.Values{}
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	if after != "" {
		values.Set("after", after)
	}
	endpoint := "/batches"
	if encoded := values.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}

	var resp core.BatchListResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: endpoint,
	}, &resp)
	if err != nil {
		return nil, err
	}
	for i := range resp.Data {
		if resp.Data[i].ProviderBatchID == "" {
			resp.Data[i].ProviderBatchID = resp.Data[i].ID
		}
	}
	return &resp, nil
}

// CancelBatch cancels a native Gemini batch job.
func (p *Provider) CancelBatch(ctx context.Context, id string) (*core.BatchResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	var resp core.BatchResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/batches/" + url.PathEscape(id) + "/cancel",
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.ProviderBatchID == "" {
		resp.ProviderBatchID = resp.ID
	}
	return &resp, nil
}

// GetBatchResults fetches Gemini batch results via the output file API.
func (p *Provider) GetBatchResults(ctx context.Context, id string) (*core.BatchResultsResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	return providers.FetchBatchResultsFromOutputFile(ctx, p.client, p.responseProviderName(), id)
}

// CreateFile uploads a file through Gemini's OpenAI-compatible /files API.
func (p *Provider) CreateFile(ctx context.Context, req *core.FileCreateRequest) (*core.FileObject, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	resp, err := providers.CreateOpenAICompatibleFile(ctx, p.client, req)
	if err != nil {
		return nil, err
	}
	resp.Provider = p.responseProviderName()
	return resp, nil
}

// ListFiles lists files through Gemini's OpenAI-compatible /files API.
func (p *Provider) ListFiles(ctx context.Context, purpose string, limit int, after string) (*core.FileListResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	resp, err := providers.ListOpenAICompatibleFiles(ctx, p.client, purpose, limit, after)
	if err != nil {
		return nil, err
	}
	for i := range resp.Data {
		resp.Data[i].Provider = p.responseProviderName()
	}
	return resp, nil
}

// GetFile retrieves one file object through Gemini's OpenAI-compatible /files API.
func (p *Provider) GetFile(ctx context.Context, id string) (*core.FileObject, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	resp, err := providers.GetOpenAICompatibleFile(ctx, p.client, id)
	if err != nil {
		return nil, err
	}
	resp.Provider = p.responseProviderName()
	return resp, nil
}

// DeleteFile deletes a file object through Gemini's OpenAI-compatible /files API.
func (p *Provider) DeleteFile(ctx context.Context, id string) (*core.FileDeleteResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	return providers.DeleteOpenAICompatibleFile(ctx, p.client, id)
}

// GetFileContent fetches raw file bytes through Gemini's /files/{id}/content API.
func (p *Provider) GetFileContent(ctx context.Context, id string) (*core.FileContentResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	return providers.GetOpenAICompatibleFileContent(ctx, p.client, id)
}
