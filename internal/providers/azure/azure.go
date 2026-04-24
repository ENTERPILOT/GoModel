package azure

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
	"gomodel/internal/providers/openai"
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
}

func New(providerCfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	baseURL := providers.ResolveBaseURL(providerCfg.BaseURL, "https://example.invalid")
	apiVersion := providers.ResolveAPIVersion(providerCfg.APIVersion, defaultAPIVersion)
	p := &Provider{apiVersion: apiVersion}
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
	p := &Provider{apiVersion: defaultAPIVersion}
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
	if resp.ProviderBatchID == "" {
		resp.ProviderBatchID = resp.ID
	}
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
	if resp.ProviderBatchID == "" {
		resp.ProviderBatchID = resp.ID
	}
	return &resp, nil
}

func (p *Provider) ListBatches(ctx context.Context, limit int, after string) (*core.BatchListResponse, error) {
	values := url.Values{}
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	if after != "" {
		values.Set("after", after)
	}

	endpoint := "/openai/batches"
	if encoded := values.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}

	var resp core.BatchListResponse
	if err := p.resourceProvider.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: endpoint,
	}, &resp); err != nil {
		return nil, err
	}
	for i := range resp.Data {
		if resp.Data[i].ProviderBatchID == "" {
			resp.Data[i].ProviderBatchID = resp.Data[i].ID
		}
	}
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
	if resp.ProviderBatchID == "" {
		resp.ProviderBatchID = resp.ID
	}
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
	req.Header.Set("api-key", apiKey)
	if requestID := core.GetRequestID(req.Context()); requestID != "" && isValidClientRequestID(requestID) {
		req.Header.Set("X-Client-Request-Id", requestID)
	}
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
