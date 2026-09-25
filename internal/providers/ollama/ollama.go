// Package ollama provides Ollama API integration for the LLM gateway.
package ollama

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"reflect"
	"strings"
	"sync"

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
	// showCache remembers /api/show-derived metadata per model name (including
	// confirmed-empty results) so repeated listings don't re-probe upstream.
	showCache sync.Map // string → *core.ModelMetadata (nil when nothing was reported)
}

// showResponse is the subset of Ollama's /api/show reply the listing keeps:
// the capability list (completion, embedding, vision, tools, thinking), the
// model family, and the architecture-keyed model_info map that holds the
// trained context length under "<architecture>.context_length".
type showResponse struct {
	Capabilities []string `json:"capabilities"`
	Details      struct {
		Family string `json:"family"`
	} `json:"details"`
	ModelInfo map[string]any `json:"model_info"`
}

// discoveredMetadata maps a model's native /api/show reply onto gateway
// metadata: modes from the completion/embedding capabilities, catalog
// capability keys for vision, tools and thinking, the family, and the context
// length. Errors are swallowed and not cached, so a transient failure retries
// on the next listing while a server without the capabilities field (older
// Ollama, or an OpenAI-compatible impostor) caches an empty result. The
// cached value is cloned on every read so no two listings share metadata.
func (p *Provider) discoveredMetadata(ctx context.Context, model string) *core.ModelMetadata {
	if cached, ok := p.showCache.Load(model); ok {
		return cached.(*core.ModelMetadata).Clone()
	}
	var show showResponse
	err := p.nativeClient.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/api/show",
		Body:     map[string]string{"model": model},
	}, &show)
	if err != nil {
		return nil
	}
	metadata := show.metadata()
	p.showCache.Store(model, metadata)
	return metadata.Clone()
}

func (s showResponse) metadata() *core.ModelMetadata {
	metadata := &core.ModelMetadata{Family: strings.TrimSpace(s.Details.Family)}
	modes := make([]string, 0, 2)
	for _, capability := range s.Capabilities {
		switch strings.ToLower(strings.TrimSpace(capability)) {
		case "completion":
			modes = append(modes, "chat")
		case "embedding":
			modes = append(modes, "embedding")
		case "vision", "tools", "thinking":
			metadata.Capabilities = providers.SetCapability(metadata.Capabilities, capability, true)
		}
	}
	if len(modes) > 0 {
		metadata.Modes = modes
		metadata.Categories = core.CategoriesForModes(modes)
	}
	if contextLength := s.contextLength(); contextLength > 0 {
		metadata.ContextWindow = &contextLength
	}
	if metadata.Family == "" && len(modes) == 0 && metadata.Capabilities == nil && metadata.ContextWindow == nil {
		return nil
	}
	return metadata
}

// contextLength reads "<architecture>.context_length" from model_info, the
// context the model was trained for.
func (s showResponse) contextLength() int {
	architecture, _ := s.ModelInfo["general.architecture"].(string)
	architecture = strings.TrimSpace(architecture)
	if architecture == "" {
		return 0
	}
	value, ok := s.ModelInfo[architecture+".context_length"].(float64)
	if !ok || value <= 0 || value > float64(math.MaxInt32) {
		return 0
	}
	return int(value)
}

// New creates a new Ollama provider.
func New(providerCfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	p := &Provider{keys: opts.Keyring(providerCfg.APIKey)}
	p.compat = openai.NewCompatibleProvider(providerCfg.APIKey, opts, compatibleConfig(defaultBaseURL))

	nativeCfg := llmclient.Config{
		ProviderName:   opts.ClientName("ollama"),
		BaseURL:        defaultNativeBaseURL,
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}
	p.nativeClient = llmclient.NewWithOptionalHTTPClient(opts.HTTPClient, nativeCfg, p.setNativeHeaders)
	p.SetBaseURL(providers.ResolveBaseURL(providerCfg.BaseURL, defaultBaseURL))
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
// Makes a lightweight request to the models endpoint. The caller owns the
// probe deadline.
func (p *Provider) CheckAvailability(ctx context.Context) error {
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
	setHeaders(req, p.keys.NextForContext(req.Context()))
}

// ChatCompletion sends a chat completion request to Ollama
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return p.compat.ChatCompletion(ctx, req)
}

// StreamChatCompletion returns a raw response body for streaming (caller must close)
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	return p.compat.StreamChatCompletion(ctx, req)
}

// ListModels retrieves the list of available models from Ollama, stamping
// modes/categories, capabilities, family and context length from each model's
// native /api/show reply so local models are described without a
// remote-registry entry. Lookups are best-effort (older Ollama versions lack
// the capabilities field; a failed call just leaves the model unstamped for
// the ID heuristic) and cached per model name, so steady-state listings cost
// no extra requests.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	resp, err := p.compat.ListModels(ctx)
	if err != nil || resp == nil {
		return resp, err
	}
	for i := range resp.Data {
		if resp.Data[i].Metadata != nil {
			continue
		}
		resp.Data[i].Metadata = p.discoveredMetadata(ctx, resp.Data[i].ID)
	}
	return resp, nil
}

// Responses sends a Responses API request to Ollama (converted to chat format)
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return providers.ResponsesViaChat(ctx, p, req, "ollama")
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

// openAICompatHint points users who registered an OpenAI-compatible server
// (e.g. LM Studio) as "ollama" at the right provider type.
const openAICompatHint = `; if this endpoint is an OpenAI-compatible server (e.g. LM Studio), configure it as an "openai" or "vllm" provider instead of "ollama"`

// Embeddings sends an embeddings request to Ollama via its native /api/embed endpoint.
// Converts between OpenAI embedding format and Ollama's native format.
func (p *Provider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	ollamaReq := ollamaEmbedRequest{
		Model: req.Model,
		Input: req.Input,
	}

	var ollamaResp ollamaEmbedResponse
	err := p.nativeClient.Do(ctx, llmclient.Request{
		Method:    http.MethodPost,
		Endpoint:  "/api/embed",
		Operation: llmclient.OperationEmbeddings,
		Model:     req.Model,
		Body:      ollamaReq,
	}, &ollamaResp)
	if err != nil {
		// An error hidden behind HTTP 200 on the native path is the signature
		// of an OpenAI-compatible server registered as "ollama".
		var gatewayErr *core.GatewayError
		if errors.Is(err, core.ErrEmbeddedInSuccess) && errors.As(err, &gatewayErr) {
			gatewayErr.Message += openAICompatHint
		}
		return nil, err
	}

	// A request that carried input always yields at least one vector. Zero
	// vectors for a non-empty request means the upstream did not honor the
	// native /api/embed contract — e.g. an OpenAI-compatible server (LM Studio,
	// vLLM) that has no native Ollama API and answered with an OpenAI-shaped body.
	// Fail loudly instead of returning an empty, OpenAI-shaped list that
	// silently breaks the caller. An empty input batch legitimately returns no
	// vectors, so leave that case to pass through as an empty response.
	if len(ollamaResp.Embeddings) == 0 && !embeddingInputIsEmpty(req.Input) {
		return nil, core.NewProviderError("ollama", http.StatusBadGateway,
			"ollama embeddings returned no vectors"+openAICompatHint, nil)
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
