package azure

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

const defaultAPIVersion = "2024-10-21"

var Registration = providers.Registration{
	Type:                        "azure",
	New:                         New,
	PassthroughSemanticEnricher: openai.Registration.PassthroughSemanticEnricher,
	Discovery: providers.DiscoveryConfig{
		RequireBaseURL:     true,
		SupportsAPIVersion: true,
	},
}

type Provider struct {
	*openai.CompatibleProvider
	resourceProvider       *openai.CompatibleProvider
	openAIResourceProvider *openai.CompatibleProvider
	apiVersion             string             // configured api-version; empty selects the surface default
	resourcePath           string             // "/openai", or "/openai/v1" on the GA v1 surface
	keys                   *providers.Keyring // retained to inject the api-key header on the realtime target
}

func New(providerCfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	baseURL := providers.ResolveBaseURL(providerCfg.BaseURL, "https://example.invalid")
	// All three clients share opts.Keys, so the rotation is even across them.
	p := &Provider{apiVersion: strings.TrimSpace(providerCfg.APIVersion), keys: opts.Keyring(providerCfg.APIKey)}
	clientCfg := openai.CompatibleProviderConfig{
		ProviderName: "azure",
		BaseURL:      baseURL,
		SetHeaders:   setHeaders,
	}
	p.CompatibleProvider = openai.NewCompatibleProvider(providerCfg.APIKey, opts, clientCfg)
	p.resourceProvider = openai.NewCompatibleProvider(providerCfg.APIKey, opts, clientCfg)
	p.openAIResourceProvider = openai.NewCompatibleProvider(providerCfg.APIKey, opts, clientCfg)
	p.SetRequestMutator(p.mutateRequest)
	p.resourceProvider.SetRequestMutator(p.mutateRequest)
	p.openAIResourceProvider.SetRequestMutator(p.mutateRequest)
	p.SetBaseURL(baseURL)
	return p
}

// SetBaseURL accepts both Azure surfaces: the classic deployment URL
// (<resource>/openai/deployments/<deployment>) and the GA v1 URL
// (<resource>/openai/v1/) that Azure AI Foundry hands out.
func (p *Provider) SetBaseURL(baseURL string) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	resourceRoot := resourceRootBaseURL(baseURL)
	p.resourcePath = "/openai"
	if isV1BaseURL(baseURL) {
		p.resourcePath = "/openai/v1"
	}
	p.CompatibleProvider.SetBaseURL(baseURL)
	p.resourceProvider.SetBaseURL(resourceRoot)
	p.openAIResourceProvider.SetBaseURL(resourceRoot + p.resourcePath)
}

func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	var resp core.ModelsResponse
	if err := p.resourceProvider.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: p.resourcePath + "/models",
	}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CreateSpeech fills OpenAI's required voice, as Azure OpenAI requires one too.
func (p *Provider) CreateSpeech(ctx context.Context, req *core.AudioSpeechRequest) (*core.AudioResponse, error) {
	return p.CompatibleProvider.CreateSpeech(ctx, openai.WithDefaultSpeechVoice(req))
}

func (p *Provider) CreateBatch(ctx context.Context, req *core.BatchRequest) (*core.BatchResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("batch request is required", nil)
	}
	var resp core.BatchResponse
	if err := p.resourceProvider.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: p.resourcePath + "/batches",
		Body:     req,
	}, &resp); err != nil {
		return nil, err
	}
	providers.EnsureProviderBatchID(&resp)
	return &resp, nil
}

func (p *Provider) GetBatch(ctx context.Context, id string) (*core.BatchResponse, error) {
	var resp core.BatchResponse
	if err := p.resourceProvider.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: p.resourcePath + "/batches/" + url.PathEscape(id),
	}, &resp); err != nil {
		return nil, err
	}
	providers.EnsureProviderBatchID(&resp)
	return &resp, nil
}

func (p *Provider) ListBatches(ctx context.Context, limit int, after string) (*core.BatchListResponse, error) {
	endpoint := providers.PaginatedEndpoint(p.resourcePath+"/batches", limit, "after", after)

	var resp core.BatchListResponse
	if err := p.resourceProvider.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: endpoint,
	}, &resp); err != nil {
		return nil, err
	}
	providers.EnsureProviderBatchIDs(&resp)
	return &resp, nil
}

func (p *Provider) CancelBatch(ctx context.Context, id string) (*core.BatchResponse, error) {
	var resp core.BatchResponse
	if err := p.resourceProvider.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: p.resourcePath + "/batches/" + url.PathEscape(id) + "/cancel",
	}, &resp); err != nil {
		return nil, err
	}
	providers.EnsureProviderBatchID(&resp)
	return &resp, nil
}

func (p *Provider) GetBatchResults(ctx context.Context, id string) (*core.BatchResultsResponse, error) {
	return p.openAIResourceProvider.GetBatchResults(ctx, id)
}

func (p *Provider) SetAPIVersion(version string) {
	if version == "" {
		return
	}
	p.apiVersion = version
}

// requestAPIVersion returns the api-version query value, or "" to send none.
// The GA v1 surface is unversioned and rejects dated versions, so it only gets
// one when configured explicitly (e.g. "preview").
func (p *Provider) requestAPIVersion() string {
	if p.apiVersion != "" {
		return p.apiVersion
	}
	if p.resourcePath == "/openai/v1" {
		return ""
	}
	return defaultAPIVersion
}

// mutateRequest adds the api-version query parameter. Foundry's System One
// endpoint is unversioned, so it gets none; its body is buffered instead.
func (p *Provider) mutateRequest(req *llmclient.Request) {
	if strings.HasPrefix(req.Endpoint, systemOnePath) {
		bufferSystemOneBody(req)
		return
	}
	version := p.requestAPIVersion()
	if version == "" {
		return
	}
	endpoint, err := url.Parse(req.Endpoint)
	if err != nil {
		return
	}
	query := endpoint.Query()
	query.Set("api-version", version)
	endpoint.RawQuery = query.Encode()
	req.Endpoint = endpoint.String()
}

func setHeaders(req *http.Request, apiKey string) {
	providers.SetAuthHeaders(req, apiKey, providers.AuthHeaderConfig{
		AuthHeader:        "api-key",
		RequestIDHeader:   "X-Client-Request-Id",
		ValidateRequestID: providers.IsValidClientRequestID,
	})
}

func resourceRootBaseURL(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return strings.TrimRight(strings.TrimSpace(baseURL), "/")
	}

	path := strings.TrimRight(parsed.Path, "/")
	for _, marker := range []string{"/openai/deployments/", "/deployments/"} {
		if idx := strings.Index(path, marker); idx >= 0 {
			path = path[:idx]
			break
		}
	}
	path = strings.TrimSuffix(path, "/openai/v1")

	parsed.Path = path
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""

	return strings.TrimRight(parsed.String(), "/")
}

// isV1BaseURL reports whether baseURL targets Azure's GA v1 surface.
func isV1BaseURL(baseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/openai/v1")
}
