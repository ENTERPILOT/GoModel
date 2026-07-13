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
	apiVersion             string
	keys                   *providers.Keyring // retained to inject the api-key header on the realtime target
}

func New(providerCfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	baseURL := providers.ResolveBaseURL(providerCfg.BaseURL, "https://example.invalid")
	apiVersion := providers.ResolveAPIVersion(providerCfg.APIVersion, defaultAPIVersion)
	// All three clients share opts.Keys, so the rotation is even across them.
	p := &Provider{apiVersion: apiVersion, keys: opts.Keyring(providerCfg.APIKey)}
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

func NewWithHTTPClient(apiKey string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	p := &Provider{apiVersion: defaultAPIVersion, keys: providers.NewKeyring(apiKey)}
	cfg := openai.CompatibleProviderConfig{
		ProviderName: "azure",
		BaseURL:      "https://example.invalid",
		SetHeaders:   setHeaders,
	}
	p.CompatibleProvider = openai.NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, cfg)
	p.resourceProvider = openai.NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, cfg)
	p.openAIResourceProvider = openai.NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, cfg)
	p.SetRequestMutator(p.mutateRequest)
	p.resourceProvider.SetRequestMutator(p.mutateRequest)
	p.openAIResourceProvider.SetRequestMutator(p.mutateRequest)
	return p
}

func (p *Provider) SetBaseURL(baseURL string) {
	resourceRoot := resourceRootBaseURL(baseURL)
	p.CompatibleProvider.SetBaseURL(baseURL)
	p.resourceProvider.SetBaseURL(resourceRoot)
	p.openAIResourceProvider.SetBaseURL(resourceRoot + "/openai")
}

func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	var resp core.ModelsResponse
	if err := p.resourceProvider.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/openai/models",
	}, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (p *Provider) CreateBatch(ctx context.Context, req *core.BatchRequest) (*core.BatchResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("batch request is required", nil)
	}
	var resp core.BatchResponse
	if err := p.resourceProvider.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/openai/batches",
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
		Endpoint: "/openai/batches/" + url.PathEscape(id),
	}, &resp); err != nil {
		return nil, err
	}
	providers.EnsureProviderBatchID(&resp)
	return &resp, nil
}

func (p *Provider) ListBatches(ctx context.Context, limit int, after string) (*core.BatchListResponse, error) {
	endpoint := providers.PaginatedEndpoint("/openai/batches", limit, "after", after)

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
		Endpoint: "/openai/batches/" + url.PathEscape(id) + "/cancel",
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

func (p *Provider) mutateRequest(req *llmclient.Request) {
	endpoint, err := url.Parse(req.Endpoint)
	if err != nil {
		return
	}
	query := endpoint.Query()
	query.Set("api-version", p.apiVersion)
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

	parsed.Path = path
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""

	return strings.TrimRight(parsed.String(), "/")
}
