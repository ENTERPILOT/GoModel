package gemini

import (
	"context"
	"net/http"
	"net/url"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

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
	providers.EnsureProviderBatchID(&resp)
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
	providers.EnsureProviderBatchID(&resp)
	return &resp, nil
}

// ListBatches lists native Gemini batch jobs.
func (p *Provider) ListBatches(ctx context.Context, limit int, after string) (*core.BatchListResponse, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	endpoint := providers.PaginatedEndpoint("/batches", limit, "after", after)

	var resp core.BatchListResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: endpoint,
	}, &resp)
	if err != nil {
		return nil, err
	}
	providers.EnsureProviderBatchIDs(&resp)
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
	providers.EnsureProviderBatchID(&resp)
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
