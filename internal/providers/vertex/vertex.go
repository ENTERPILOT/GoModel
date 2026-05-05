// Package vertex provides Google Vertex AI Gemini integration.
package vertex

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"

	"gomodel/internal/core"
	"gomodel/internal/httpclient"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
	"gomodel/internal/providers/gemini"
	"gomodel/internal/providers/googleauth"
)

// Registration provides factory registration for the Vertex AI provider.
var Registration = providers.Registration{
	Type: "vertex",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		NameSeparator: "_",
	},
}

const (
	authTypeGCPADC         = "gcp_adc"
	authTypeServiceAccount = "gcp_service_account"
)

// Provider implements Vertex AI through Gemini chat helpers plus Vertex-native
// embedding prediction.
type Provider struct {
	gemini       *gemini.Provider
	nativeClient *llmclient.Client
	authType     string
	configErr    error
}

// New creates a new Vertex AI provider.
func New(providerCfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return newProvider(providerCfg, opts, nil)
}

func newProvider(providerCfg providers.ProviderConfig, opts providers.ProviderOptions, baseHTTPClient *http.Client) *Provider {
	providerCfg.Backend = "vertex"
	p := &Provider{
		authType: normalizeAuthType(providerCfg),
	}
	p.validateConfig(providerCfg)

	authClient := baseHTTPClient
	if authClient == nil {
		authClient = p.authHTTPClient(providerCfg, nil)
	}
	p.gemini = gemini.NewVertexWithHTTPClient(providerCfg, opts, authClient)
	nativeBaseURL := vertexNativeBaseURL(providerCfg)
	nativeCfg := llmclient.Config{
		ProviderName:   "vertex",
		BaseURL:        nativeBaseURL,
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}
	if authClient != nil {
		p.nativeClient = llmclient.NewWithHTTPClient(authClient, nativeCfg, p.setHeaders)
	} else {
		p.nativeClient = llmclient.New(nativeCfg, p.setHeaders)
	}
	return p
}

func (p *Provider) validateConfig(providerCfg providers.ProviderConfig) {
	if !hasResolvedProviderValue(providerCfg.BaseURL) &&
		(!hasResolvedProviderValue(providerCfg.VertexProject) || !hasResolvedProviderValue(providerCfg.VertexLocation)) {
		p.configErr = fmt.Errorf("vertex AI requires base_url or vertex_project and vertex_location")
		return
	}
	if !validAuthType(providerCfg.AuthType) {
		p.configErr = fmt.Errorf("unsupported vertex AI auth type %q", providerCfg.AuthType)
		return
	}
	switch p.authType {
	case authTypeGCPADC:
		return
	case authTypeServiceAccount:
		if googleauth.HasServiceAccount(buildGoogleAuthConfig(providerCfg)) {
			return
		}
		p.configErr = fmt.Errorf("vertex AI service account auth requires service_account_file, service_account_json, or service_account_json_base64")
	default:
		p.configErr = fmt.Errorf("unsupported normalized vertex AI auth type %q", p.authType)
	}
}

func validAuthType(authType string) bool {
	switch strings.ToLower(strings.TrimSpace(authType)) {
	case "", "gcp_adc", "adc", "google_adc", "gcp_service_account", "service_account":
		return true
	default:
		return false
	}
}

func hasResolvedProviderValue(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.Contains(value, "${")
}

func normalizeAuthType(providerCfg providers.ProviderConfig) string {
	return googleauth.NormalizeAuthType(providerCfg.AuthType, googleauth.HasServiceAccount(buildGoogleAuthConfig(providerCfg)))
}

func (p *Provider) authHTTPClient(providerCfg providers.ProviderConfig, base *http.Client) *http.Client {
	if p.configErr != nil {
		return base
	}
	authCfg := buildGoogleAuthConfig(providerCfg)
	authCfg.AuthType = p.authType
	source, err := googleauth.TokenSource(context.Background(), authCfg)
	if err != nil {
		p.configErr = err
		return base
	}
	if base == nil {
		base = httpclient.NewDefaultHTTPClient()
	}
	return googleauth.HTTPClient(base, source)
}

func buildGoogleAuthConfig(providerCfg providers.ProviderConfig) googleauth.Config {
	return googleauth.Config{
		AuthType:                 providerCfg.AuthType,
		ServiceAccountFile:       providerCfg.ServiceAccountFile,
		ServiceAccountJSON:       providerCfg.ServiceAccountJSON,
		ServiceAccountJSONBase64: providerCfg.ServiceAccountJSONBase64,
		Scope:                    providerCfg.GCPScope,
	}
}

func (p *Provider) ready() error {
	if p.configErr == nil {
		return nil
	}
	return core.NewProviderError("vertex", http.StatusBadGateway, "invalid Vertex AI provider configuration: "+p.configErr.Error(), p.configErr)
}

func (p *Provider) setHeaders(req *http.Request) {
	if requestID := core.GetRequestID(req.Context()); requestID != "" {
		req.Header.Set("X-Request-Id", requestID)
	}
}

// ChatCompletion sends a chat completion request to Vertex AI Gemini.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	resp, err := p.gemini.ChatCompletion(ctx, req)
	if resp != nil {
		resp.Provider = "vertex"
	}
	return resp, err
}

// StreamChatCompletion returns a raw response body for streaming.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	return p.gemini.StreamChatCompletion(ctx, req)
}

// ListModels retrieves Vertex AI publisher models.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	return p.gemini.ListModels(ctx)
}

// Responses sends a Responses API request to Vertex AI Gemini.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	return providers.ResponsesViaChat(ctx, p, req)
}

// StreamResponses returns a raw response body for streaming Responses API.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	return providers.StreamResponsesViaChat(ctx, p, req, "vertex")
}

// Embeddings sends an embedding request through Vertex AI native prediction.
func (p *Provider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	if req == nil {
		return nil, core.NewInvalidRequestError("embedding request is required", nil)
	}
	inputs, err := embeddingInputs(req.Input)
	if err != nil {
		return nil, err
	}

	body := vertexEmbeddingPredictRequest{
		Instances: make([]vertexEmbeddingInstance, 0, len(inputs)),
		Parameters: map[string]any{
			"autoTruncate": true,
		},
	}
	if req.Dimensions != nil && *req.Dimensions > 0 {
		body.Parameters["outputDimensionality"] = *req.Dimensions
	}
	for _, input := range inputs {
		body.Instances = append(body.Instances, vertexEmbeddingInstance{Content: input})
	}

	var resp vertexEmbeddingPredictResponse
	err = p.nativeClient.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: vertexPredictEndpoint(req.Model),
		Body:     body,
	}, &resp)
	if err != nil {
		return nil, err
	}
	return openAIEmbeddingResponse(req, &resp)
}

type vertexEmbeddingPredictRequest struct {
	Instances  []vertexEmbeddingInstance `json:"instances"`
	Parameters map[string]any            `json:"parameters,omitempty"`
}

type vertexEmbeddingInstance struct {
	Content string `json:"content"`
}

type vertexEmbeddingPredictResponse struct {
	Predictions []vertexEmbeddingPrediction `json:"predictions"`
}

type vertexEmbeddingPrediction struct {
	Embeddings vertexEmbeddingValues `json:"embeddings"`
	Values     []float64             `json:"values,omitempty"`
}

type vertexEmbeddingValues struct {
	Values     []float64                 `json:"values"`
	Statistics vertexEmbeddingStatistics `json:"statistics"`
}

type vertexEmbeddingStatistics struct {
	TokenCount int  `json:"token_count"`
	Truncated  bool `json:"truncated"`
}

func embeddingInputs(input any) ([]string, error) {
	switch v := input.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, core.NewInvalidRequestError("embedding input is required", nil)
		}
		return []string{v}, nil
	case []string:
		return nonEmptyEmbeddingInputs(v)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			text, ok := item.(string)
			if !ok {
				return nil, core.NewInvalidRequestError("vertex AI embeddings support string inputs", nil)
			}
			out = append(out, text)
		}
		return nonEmptyEmbeddingInputs(out)
	default:
		return nil, core.NewInvalidRequestError("vertex AI embeddings support string inputs", nil)
	}
}

func nonEmptyEmbeddingInputs(inputs []string) ([]string, error) {
	for _, input := range inputs {
		if strings.TrimSpace(input) == "" {
			return nil, core.NewInvalidRequestError("embedding input must not be empty", nil)
		}
	}
	if len(inputs) == 0 {
		return nil, core.NewInvalidRequestError("embedding input is required", nil)
	}
	return inputs, nil
}

func openAIEmbeddingResponse(req *core.EmbeddingRequest, resp *vertexEmbeddingPredictResponse) (*core.EmbeddingResponse, error) {
	out := &core.EmbeddingResponse{
		Object:   "list",
		Data:     make([]core.EmbeddingData, 0, len(resp.Predictions)),
		Model:    req.Model,
		Provider: "vertex",
	}
	for i, prediction := range resp.Predictions {
		values := prediction.Embeddings.Values
		if len(values) == 0 {
			values = prediction.Values
		}
		embedding, err := encodeEmbedding(values, req.EncodingFormat)
		if err != nil {
			return nil, core.NewProviderError("vertex", http.StatusBadGateway, "failed to encode Vertex AI embedding response", err)
		}
		out.Data = append(out.Data, core.EmbeddingData{
			Object:    "embedding",
			Embedding: embedding,
			Index:     i,
		})
		out.Usage.PromptTokens += prediction.Embeddings.Statistics.TokenCount
	}
	out.Usage.TotalTokens = out.Usage.PromptTokens
	return out, nil
}

func encodeEmbedding(values []float64, encodingFormat string) (json.RawMessage, error) {
	if strings.EqualFold(strings.TrimSpace(encodingFormat), "base64") {
		buf := make([]byte, len(values)*4)
		for i, value := range values {
			binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(float32(value)))
		}
		return json.Marshal(base64.StdEncoding.EncodeToString(buf))
	}
	return json.Marshal(values)
}

func vertexNativeBaseURL(providerCfg providers.ProviderConfig) string {
	_, nativeBaseURL := vertexBaseURLs(providerCfg)
	return nativeBaseURL
}

// TODO: Share Vertex URL derivation with the Gemini Vertex path if this logic
// changes again. It is intentionally duplicated today to keep provider package
// boundaries simple.
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

func vertexPredictEndpoint(model string) string {
	model = normalizeVertexModelID(model)
	return "/models/" + url.PathEscape(model) + ":predict"
}

func normalizeVertexModelID(model string) string {
	model = strings.TrimSpace(model)
	if idx := strings.LastIndex(model, "/models/"); idx >= 0 {
		model = model[idx+len("/models/"):]
	}
	model = strings.TrimPrefix(model, "models/")
	model = strings.TrimPrefix(model, "google/")
	return model
}

var _ core.Provider = (*Provider)(nil)
