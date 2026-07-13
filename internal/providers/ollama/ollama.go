// Package ollama provides Ollama API integration for the LLM gateway.
package ollama

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

// Registration provides factory registration for the Ollama provider.
var Registration = providers.Registration{
	Type: "ollama",
	New:  New,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL:  defaultBaseURL,
		AllowAPIKeyless: true,
	},
}

const (
	defaultRootURL       = "http://localhost:11434"
	defaultBaseURL       = defaultRootURL + "/v1"
	defaultNativeBaseURL = defaultRootURL
)

// Provider implements the core.Provider interface for Ollama. The /v1
// OpenAI-compatible surface goes through the shared compatible provider;
// embeddings use Ollama's native /api/embed endpoint via a second client
// rooted at the server root. Methods are delegated explicitly rather than
// embedded because Ollama's upstream lacks parts of the full OpenAI
// surface (passthrough, audio) and embedding cannot subtract methods.
type Provider struct {
	compat       *openai.CompatibleProvider
	nativeClient *llmclient.Client
	keys         *providers.Keyring // Optional; Ollama accepts a bearer token but does not require one
}

// New creates a new Ollama provider.
func New(providerCfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	p := &Provider{keys: opts.Keyring(providerCfg.APIKey)}
	p.compat = openai.NewCompatibleProvider(providerCfg.APIKey, opts, compatibleConfig(defaultBaseURL))

	nativeCfg := llmclient.Config{
		ProviderName:   "ollama",
		BaseURL:        defaultNativeBaseURL,
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}
	p.nativeClient = llmclient.New(nativeCfg, p.setNativeHeaders)
	p.SetBaseURL(providers.ResolveBaseURL(providerCfg.BaseURL, defaultBaseURL))
	return p
}

// NewWithHTTPClient creates a new Ollama provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	p := &Provider{keys: providers.NewKeyring(apiKey)}
	p.compat = openai.NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, compatibleConfig(defaultBaseURL))

	nativeCfg := llmclient.DefaultConfig("ollama", defaultNativeBaseURL)
	nativeCfg.Hooks = hooks
	p.nativeClient = llmclient.NewWithHTTPClient(httpClient, nativeCfg, p.setNativeHeaders)
	return p
}

func compatibleConfig(baseURL string) openai.CompatibleProviderConfig {
	return openai.CompatibleProviderConfig{
		ProviderName: "ollama",
		BaseURL:      baseURL,
		SetHeaders:   setHeaders,
	}
}

// SetBaseURL allows configuring a custom base URL for the provider.
// Also updates the native client by deriving the root URL (stripping /v1 suffix).
func (p *Provider) SetBaseURL(url string) {
	p.compat.SetBaseURL(url)
	normalized := strings.TrimRight(url, "/")
	normalized = strings.TrimSuffix(normalized, "/v1")
	p.nativeClient.SetBaseURL(normalized)
}

// CheckAvailability verifies that Ollama is running and accessible.
// Makes a lightweight request to the models endpoint.
func (p *Provider) CheckAvailability(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, err := p.ListModels(ctx)
	return err
}

// setHeaders sets the required headers for Ollama API requests.
// Ollama doesn't require authentication, but accepts a Bearer token if provided.
func setHeaders(req *http.Request, apiKey string) {
	providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{
		AuthScheme:      "Bearer ",
		RequestIDHeader: "X-Request-ID",
		OptionalAPIKey:  true,
	})
}

// setNativeHeaders applies the same header policy on the native /api client.
func (p *Provider) setNativeHeaders(req *http.Request) {
	setHeaders(req, p.keys.Next())
}

// ChatCompletion sends a chat completion request to Ollama
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return p.compat.ChatCompletion(ctx, req)
}

// StreamChatCompletion returns a raw response body for streaming (caller must close)
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return p.compat.StreamChatCompletion(ctx, req)
}

// ListModels retrieves the list of available models from Ollama
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	return p.compat.ListModels(ctx)
}

// Responses sends a Responses API request to Ollama (converted to chat format)
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return providers.ResponsesViaChat(ctx, p, req)
}

// StreamResponses returns a raw response body for streaming Responses API (caller must close)
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	return providers.StreamResponsesViaChat(ctx, p, req, "ollama")
}

type ollamaEmbedRequest struct {
	Model string `json:"model"`
	Input any    `json:"input"`
}

type ollamaEmbedResponse struct {
	Model           string      `json:"model"`
	Embeddings      [][]float64 `json:"embeddings"`
	PromptEvalCount int         `json:"prompt_eval_count"`
}

// Embeddings sends an embeddings request to Ollama via its native /api/embed endpoint.
// Converts between OpenAI embedding format and Ollama's native format.
func (p *Provider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	ollamaReq := ollamaEmbedRequest{
		Model: req.Model,
		Input: req.Input,
	}

	var ollamaResp ollamaEmbedResponse
	err := p.nativeClient.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/api/embed",
		Body:     ollamaReq,
	}, &ollamaResp)
	if err != nil {
		return nil, err
	}

	// A request that carried input always yields at least one vector. Zero
	// vectors for a non-empty request means the upstream did not honor the
	// native /api/embed contract — e.g. an OpenAI-compatible server (LM Studio,
	// vLLM) that has no native Ollama API and answers 200 with an error body.
	// Fail loudly instead of returning an empty, OpenAI-shaped list that
	// silently breaks the caller. An empty input batch legitimately returns no
	// vectors, so leave that case to pass through as an empty response.
	if len(ollamaResp.Embeddings) == 0 && !embeddingInputIsEmpty(req.Input) {
		return nil, core.NewProviderError("ollama", http.StatusBadGateway,
			"ollama embeddings returned no vectors; if this endpoint is an OpenAI-compatible server (e.g. LM Studio), configure it as an \"openai\" or \"vllm\" provider instead of \"ollama\"", nil)
	}

	data := make([]core.EmbeddingData, len(ollamaResp.Embeddings))
	for i, emb := range ollamaResp.Embeddings {
		raw, err := json.Marshal(emb)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal embedding at index %d: %w", i, err)
		}
		data[i] = core.EmbeddingData{
			Object:    "embedding",
			Embedding: raw,
			Index:     i,
		}
	}

	model := ollamaResp.Model
	if model == "" {
		model = req.Model
	}

	return &core.EmbeddingResponse{
		Object: "list",
		Data:   data,
		Model:  model,
		Usage: core.EmbeddingUsage{
			PromptTokens: ollamaResp.PromptEvalCount,
			TotalTokens:  ollamaResp.PromptEvalCount,
		},
	}, nil
}

// embeddingInputIsEmpty reports whether an embeddings request carries an empty
// batch — an empty array/slice — for which zero returned vectors is a legitimate
// result. Input is decoded from JSON (so a batch is []any), but the reflection
// fallback also covers a directly-constructed typed slice such as []string{}.
//
// Scalar ("") and nil inputs are deliberately NOT treated as empty batches: if
// they come back with zero vectors it more likely means the upstream rejected
// the request (e.g. an OpenAI-compatible server misconfigured as ollama
// answering 200 with an error body), which must stay on the loud-error path
// rather than silently returning an empty list.
func embeddingInputIsEmpty(input any) bool {
	switch v := input.(type) {
	case []any:
		return len(v) == 0
	default:
		if rv := reflect.ValueOf(input); rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
			return rv.Len() == 0
		}
		return false
	}
}

// errBatchUnsupported is returned by every batch endpoint because Ollama has
// no native discounted batch API.
func errBatchUnsupported() error {
	return core.NewInvalidRequestError("ollama does not support native discounted batch processing", nil)
}

// CreateBatch returns unsupported because Ollama has no native discounted batch API.
func (p *Provider) CreateBatch(_ context.Context, _ *core.BatchRequest) (*core.BatchResponse, error) {
	return nil, errBatchUnsupported()
}

// GetBatch returns unsupported because Ollama has no native discounted batch API.
func (p *Provider) GetBatch(_ context.Context, _ string) (*core.BatchResponse, error) {
	return nil, errBatchUnsupported()
}

// ListBatches returns unsupported because Ollama has no native discounted batch API.
func (p *Provider) ListBatches(_ context.Context, _ int, _ string) (*core.BatchListResponse, error) {
	return nil, errBatchUnsupported()
}

// CancelBatch returns unsupported because Ollama has no native discounted batch API.
func (p *Provider) CancelBatch(_ context.Context, _ string) (*core.BatchResponse, error) {
	return nil, errBatchUnsupported()
}

// GetBatchResults returns unsupported because Ollama has no native discounted batch API.
func (p *Provider) GetBatchResults(_ context.Context, _ string) (*core.BatchResultsResponse, error) {
	return nil, errBatchUnsupported()
}
