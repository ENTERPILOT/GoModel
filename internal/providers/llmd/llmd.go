// Package llmd provides integration with the llm-d Router's OpenAI-compatible API.
package llmd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const (
	canonicalObjectiveHeader = "X-Llm-D-Inference-Objective"
	legacyObjectiveHeader    = "X-Gateway-Inference-Objective"
	canonicalFairnessHeader  = "X-Llm-D-Inference-Fairness-Id"
	legacyFairnessHeader     = "X-Gateway-Inference-Fairness-Id"
	droppedReasonHeader      = "X-Llm-D-Request-Dropped-Reason"
)

// Registration provides factory registration for the llm-d provider. An
// llm-d deployment has no universal endpoint, so base_url is required. Router
// authentication is optional and depends on the Gateway in front of llm-d.
var Registration = providers.Registration{
	Type:                        "llmd",
	New:                         New,
	PassthroughSemanticEnricher: passthroughSemanticEnricher,
	Discovery: providers.DiscoveryConfig{
		RequireBaseURL:  true,
		AllowAPIKeyless: true,
	},
}

// ControlConfig contains trusted llm-d request-classification settings.
type ControlConfig struct {
	InferenceObjective   string
	FairnessFromUserPath bool
}

// Provider implements the OpenAI-compatible inference subset understood by
// llm-d's openai-parser. It intentionally does not advertise native files,
// batches, audio, or Responses lifecycle interfaces.
type Provider struct {
	compatible *openai.CompatibleProvider
	rootClient *llmclient.Client
	controls   ControlConfig
}

var (
	_ core.Provider            = (*Provider)(nil)
	_ core.PassthroughProvider = (*Provider)(nil)
)

// New creates an llm-d provider.
func New(cfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	return newProvider(cfg.APIKey, cfg.BaseURL, ControlConfig{
		InferenceObjective:   cfg.InferenceObjective,
		FairnessFromUserPath: cfg.FairnessFromUserPath,
	}, opts, nil)
}

// NewWithHTTPClient creates an llm-d provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey, baseURL string, controls ControlConfig, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	return newProvider(apiKey, baseURL, controls, providers.ProviderOptions{Hooks: hooks}, httpClient)
}

func newProvider(apiKey, baseURL string, controls ControlConfig, opts providers.ProviderOptions, httpClient *http.Client) *Provider {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	p := &Provider{controls: controls}
	keys := opts.Keyring(apiKey)
	opts.Keys = keys
	compatibleCfg := openai.CompatibleProviderConfig{
		ProviderName: "llmd",
		BaseURL:      baseURL,
		HTTPClient:   httpClient,
		SetHeaders:   p.setHeaders,
	}
	rootCfg := llmclient.Config{
		ProviderName:   "llmd",
		BaseURL:        passthroughBaseURL(baseURL),
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}
	p.compatible = openai.NewCompatibleProvider(apiKey, opts, compatibleCfg)
	setRootHeaders := func(req *http.Request) {
		p.setHeaders(req, keys.NextForContext(req.Context()))
	}
	if httpClient == nil {
		p.rootClient = llmclient.New(rootCfg, setRootHeaders)
		return p
	}

	p.rootClient = llmclient.NewWithHTTPClient(httpClient, rootCfg, setRootHeaders)
	return p
}

// SetBaseURL changes the llm-d Router endpoint.
func (p *Provider) SetBaseURL(baseURL string) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	p.compatible.SetBaseURL(baseURL)
	p.rootClient.SetBaseURL(passthroughBaseURL(baseURL))
}

// ChatCompletion sends a chat completion request to llm-d.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	resp, err := p.compatible.ChatCompletion(ctx, req)
	return resp, exposeDroppedReason(err)
}

// StreamChatCompletion streams a chat completion request through llm-d.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	resp, err := p.compatible.StreamChatCompletion(ctx, req)
	return resp, exposeDroppedReason(err)
}

// ListModels asks the llm-d route for its OpenAI-compatible model inventory.
// Operators should configure providers.<name>.models when their HTTPRoute does
// not forward GET /v1/models to a model server.
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	resp, err := p.compatible.ListModels(ctx)
	return resp, exposeDroppedReason(err)
}

// Responses sends a Responses API request to llm-d.
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	resp, err := p.compatible.Responses(ctx, req)
	return resp, exposeDroppedReason(err)
}

// StreamResponses streams a Responses API request through llm-d.
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	resp, err := p.compatible.StreamResponses(ctx, req)
	return resp, exposeDroppedReason(err)
}

// Embeddings sends an embeddings request to llm-d.
func (p *Provider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	resp, err := p.compatible.Embeddings(ctx, req)
	return resp, exposeDroppedReason(err)
}

// Passthrough routes an opaque request through llm-d after removing all
// client-supplied credentials and llm-d control headers.
func (p *Provider) Passthrough(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("passthrough request is required", nil)
	}
	clean := *req
	clean.Headers = cloneWithoutSensitiveHeaders(req.Headers)
	endpoint := providers.PassthroughEndpoint(clean.Endpoint)
	if usesV1PassthroughBase(endpoint) {
		resp, err := p.compatible.Passthrough(ctx, &clean)
		return resp, exposeDroppedReason(err)
	}

	resp, err := p.rootClient.DoPassthrough(ctx, llmclient.Request{
		Method:        clean.Method,
		Endpoint:      endpoint,
		RawBodyReader: clean.Body,
		Headers:       clean.Headers,
	})
	if err != nil {
		return nil, exposeDroppedReason(err)
	}
	return &core.PassthroughResponse{
		StatusCode: resp.StatusCode,
		Headers:    providers.CloneHTTPHeaders(resp.Header),
		Body:       resp.Body,
	}, nil
}

func (p *Provider) setHeaders(req *http.Request, apiKey string) {
	providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{
		AuthScheme:      "Bearer ",
		RequestIDHeader: "X-Request-Id",
		OptionalAPIKey:  true,
	})

	// Send both spellings during llm-d's header transition. Stable v0.8 uses
	// the x-gateway aliases; v0.9+ gives the canonical x-llm-d value priority.
	if objective := trustedValue(p.controls.InferenceObjective); objective != "" {
		req.Header.Set(canonicalObjectiveHeader, objective)
		req.Header.Set(legacyObjectiveHeader, objective)
	}
	if p.controls.FairnessFromUserPath {
		// Only an authenticated/authorized identity override is a safe fairness
		// key. The request snapshot may contain a client-asserted user-path
		// header, so do not use UserPathFromContext's snapshot fallback here.
		if fairnessID := trustedValue(core.GetEffectiveUserPath(req.Context())); fairnessID != "" {
			req.Header.Set(canonicalFairnessHeader, fairnessID)
			req.Header.Set(legacyFairnessHeader, fairnessID)
		}
	}
}

func trustedValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "\r\n") || strings.Contains(value, "${") {
		return ""
	}
	return value
}

func cloneWithoutSensitiveHeaders(src http.Header) http.Header {
	if len(src) == 0 {
		return nil
	}
	dst := make(http.Header, len(src))
	for key, values := range src {
		if core.IsCredentialHeader(key) || isControlHeader(key) {
			continue
		}
		dst[key] = append([]string(nil), values...)
	}
	if len(dst) == 0 {
		return nil
	}
	return dst
}

func isControlHeader(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	return strings.HasPrefix(key, "x-llm-d-") ||
		strings.HasPrefix(key, "x-gateway-inference-") ||
		key == "x-gateway-model-name-rewrite" ||
		strings.HasPrefix(key, "x-gateway-destination-endpoint") ||
		strings.HasPrefix(key, "x-slo-")
}

func passthroughBaseURL(baseURL string) string {
	if before, ok := strings.CutSuffix(strings.TrimRight(strings.TrimSpace(baseURL), "/"), "/v1"); ok {
		return before
	}
	return strings.TrimRight(strings.TrimSpace(baseURL), "/")
}

func usesV1PassthroughBase(endpoint string) bool {
	endpoint = providers.PassthroughEndpoint(endpoint)
	if strings.HasPrefix(endpoint, "/v1/") {
		return false
	}
	for _, prefix := range []string{
		"/models", "/chat/completions", "/responses", "/completions", "/embeddings", "/messages",
	} {
		if endpoint == prefix || strings.HasPrefix(endpoint, prefix+"/") {
			return true
		}
	}
	return false
}

func exposeDroppedReason(err error) error {
	if err == nil {
		return nil
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr == nil || gatewayErr.StatusCode != http.StatusTooManyRequests {
		return err
	}
	reason := trustedValue(gatewayErr.ResponseHeaders.Get(droppedReasonHeader))
	if reason == "" {
		return err
	}
	return &responseHeaderError{
		err:     err,
		headers: http.Header{droppedReasonHeader: []string{reason}},
	}
}

type responseHeaderError struct {
	err     error
	headers http.Header
}

func (e *responseHeaderError) Error() string                { return e.err.Error() }
func (e *responseHeaderError) Unwrap() error                { return e.err }
func (e *responseHeaderError) ResponseHeaders() http.Header { return e.headers.Clone() }
