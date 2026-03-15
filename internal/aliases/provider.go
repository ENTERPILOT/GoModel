package aliases

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sort"
	"strings"

	"gomodel/internal/core"
)

// Provider wraps a routable provider and resolves aliases before delegating.
type Provider struct {
	inner   core.RoutableProvider
	service *Service
}

type requestRewriteMode int

const (
	rewriteForRouting requestRewriteMode = iota
	rewriteForUpstream
)

// NewProvider creates an alias-aware provider wrapper.
func NewProvider(inner core.RoutableProvider, service *Service) *Provider {
	return &Provider{inner: inner, service: service}
}

// ResolveModel resolves a model/provider pair through the alias table.
func (p *Provider) ResolveModel(model, provider string) (core.ModelSelector, bool, error) {
	if p.service == nil {
		selector, err := core.ParseModelSelector(model, provider)
		return selector, false, err
	}
	resolution, ok, err := p.service.Resolve(model, provider)
	if err != nil {
		return core.ModelSelector{}, false, err
	}
	return resolution.Resolved, ok, nil
}

func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	forward, err := p.rewriteChatRequest(req, rewriteForRouting)
	if err != nil {
		return nil, err
	}
	return p.inner.ChatCompletion(ctx, forward)
}

func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	forward, err := p.rewriteChatRequest(req, rewriteForRouting)
	if err != nil {
		return nil, err
	}
	return p.inner.StreamChatCompletion(ctx, forward)
}

func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	resp, err := p.inner.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		resp = &core.ModelsResponse{Object: "list", Data: []core.Model{}}
	}
	if p.service == nil {
		return resp, nil
	}

	dataByID := make(map[string]core.Model, len(resp.Data))
	for _, model := range resp.Data {
		dataByID[model.ID] = model
	}
	for _, aliasModel := range p.service.ExposedModels() {
		dataByID[aliasModel.ID] = aliasModel
	}
	data := make([]core.Model, 0, len(dataByID))
	for _, model := range dataByID {
		data = append(data, model)
	}
	sort.Slice(data, func(i, j int) bool { return data[i].ID < data[j].ID })

	cloned := *resp
	cloned.Data = data
	return &cloned, nil
}

func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	forward, err := p.rewriteResponsesRequest(req, rewriteForRouting)
	if err != nil {
		return nil, err
	}
	return p.inner.Responses(ctx, forward)
}

func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	forward, err := p.rewriteResponsesRequest(req, rewriteForRouting)
	if err != nil {
		return nil, err
	}
	return p.inner.StreamResponses(ctx, forward)
}

func (p *Provider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	forward, err := p.rewriteEmbeddingRequest(req, rewriteForRouting)
	if err != nil {
		return nil, err
	}
	return p.inner.Embeddings(ctx, forward)
}

func (p *Provider) Supports(model string) bool {
	if p.service != nil && p.service.Supports(model) {
		return true
	}
	return p.inner.Supports(model)
}

func (p *Provider) GetProviderType(model string) string {
	if p.service != nil {
		if providerType := p.service.GetProviderType(model); providerType != "" {
			return providerType
		}
	}
	return p.inner.GetProviderType(model)
}

func (p *Provider) ModelCount() int {
	if counted, ok := p.inner.(interface{ ModelCount() int }); ok {
		return counted.ModelCount()
	}
	return -1
}

// NativeFileProviderTypes delegates provider capability inventory to the inner
// provider when available.
func (p *Provider) NativeFileProviderTypes() []string {
	if typed, ok := p.inner.(core.NativeFileProviderTypeLister); ok {
		return typed.NativeFileProviderTypes()
	}
	return nil
}

func (p *Provider) CreateBatch(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchResponse, error) {
	native, err := p.nativeBatchRouter()
	if err != nil {
		return nil, err
	}
	result, err := p.rewriteBatchSource(ctx, providerType, req)
	if err != nil {
		return nil, err
	}
	p.recordBatchPreparation(ctx, req, result.Request)
	resp, err := native.CreateBatch(ctx, providerType, result.Request)
	if err != nil {
		p.cleanupBatchRewriteFile(ctx, providerType, result.RewrittenInputFileID)
		return nil, err
	}
	p.cleanupSupersededBatchRewriteFile(ctx, providerType, result.RewrittenInputFileID)
	return resp, nil
}

func (p *Provider) GetBatch(ctx context.Context, providerType, id string) (*core.BatchResponse, error) {
	native, err := p.nativeBatchRouter()
	if err != nil {
		return nil, err
	}
	return native.GetBatch(ctx, providerType, id)
}

func (p *Provider) ListBatches(ctx context.Context, providerType string, limit int, after string) (*core.BatchListResponse, error) {
	native, err := p.nativeBatchRouter()
	if err != nil {
		return nil, err
	}
	return native.ListBatches(ctx, providerType, limit, after)
}

func (p *Provider) CancelBatch(ctx context.Context, providerType, id string) (*core.BatchResponse, error) {
	native, err := p.nativeBatchRouter()
	if err != nil {
		return nil, err
	}
	return native.CancelBatch(ctx, providerType, id)
}

func (p *Provider) GetBatchResults(ctx context.Context, providerType, id string) (*core.BatchResultsResponse, error) {
	native, err := p.nativeBatchRouter()
	if err != nil {
		return nil, err
	}
	return native.GetBatchResults(ctx, providerType, id)
}

func (p *Provider) CreateBatchWithHints(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchResponse, map[string]string, error) {
	hinted, err := p.nativeBatchHintRouter()
	if err != nil {
		return nil, nil, err
	}
	result, err := p.rewriteBatchSource(ctx, providerType, req)
	if err != nil {
		return nil, nil, err
	}
	p.recordBatchPreparation(ctx, req, result.Request)
	resp, hints, err := hinted.CreateBatchWithHints(ctx, providerType, result.Request)
	if err != nil {
		p.cleanupBatchRewriteFile(ctx, providerType, result.RewrittenInputFileID)
		return nil, nil, err
	}
	p.cleanupSupersededBatchRewriteFile(ctx, providerType, result.RewrittenInputFileID)
	return resp, mergeBatchHints(result.RequestEndpointHints, hints), nil
}

func (p *Provider) GetBatchResultsWithHints(ctx context.Context, providerType, id string, endpointByCustomID map[string]string) (*core.BatchResultsResponse, error) {
	hinted, err := p.nativeBatchHintRouter()
	if err != nil {
		return nil, err
	}
	return hinted.GetBatchResultsWithHints(ctx, providerType, id, endpointByCustomID)
}

func (p *Provider) ClearBatchResultHints(providerType, batchID string) {
	hinted, err := p.nativeBatchHintRouter()
	if err != nil {
		return
	}
	hinted.ClearBatchResultHints(providerType, batchID)
}

func (p *Provider) CreateFile(ctx context.Context, providerType string, req *core.FileCreateRequest) (*core.FileObject, error) {
	files, err := p.nativeFileRouter()
	if err != nil {
		return nil, err
	}
	return files.CreateFile(ctx, providerType, req)
}

func (p *Provider) ListFiles(ctx context.Context, providerType, purpose string, limit int, after string) (*core.FileListResponse, error) {
	files, err := p.nativeFileRouter()
	if err != nil {
		return nil, err
	}
	return files.ListFiles(ctx, providerType, purpose, limit, after)
}

func (p *Provider) GetFile(ctx context.Context, providerType, id string) (*core.FileObject, error) {
	files, err := p.nativeFileRouter()
	if err != nil {
		return nil, err
	}
	return files.GetFile(ctx, providerType, id)
}

func (p *Provider) DeleteFile(ctx context.Context, providerType, id string) (*core.FileDeleteResponse, error) {
	files, err := p.nativeFileRouter()
	if err != nil {
		return nil, err
	}
	return files.DeleteFile(ctx, providerType, id)
}

func (p *Provider) GetFileContent(ctx context.Context, providerType, id string) (*core.FileContentResponse, error) {
	files, err := p.nativeFileRouter()
	if err != nil {
		return nil, err
	}
	return files.GetFileContent(ctx, providerType, id)
}

func (p *Provider) Passthrough(ctx context.Context, providerType string, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	passthrough, err := p.passthroughRouter()
	if err != nil {
		return nil, err
	}
	return passthrough.Passthrough(ctx, providerType, req)
}

func (p *Provider) rewriteChatRequest(req *core.ChatRequest, mode requestRewriteMode) (*core.ChatRequest, error) {
	if req == nil {
		return nil, nil
	}
	selector, err := p.resolveRequestSelector(req.Model, req.Provider)
	if err != nil {
		return nil, err
	}
	forward := *req
	forward.Model = selector.Model
	forward.Provider = providerValueForMode(selector, mode)
	return &forward, nil
}

func (p *Provider) rewriteResponsesRequest(req *core.ResponsesRequest, mode requestRewriteMode) (*core.ResponsesRequest, error) {
	if req == nil {
		return nil, nil
	}
	selector, err := p.resolveRequestSelector(req.Model, req.Provider)
	if err != nil {
		return nil, err
	}
	forward := *req
	forward.Model = selector.Model
	forward.Provider = providerValueForMode(selector, mode)
	return &forward, nil
}

func (p *Provider) rewriteEmbeddingRequest(req *core.EmbeddingRequest, mode requestRewriteMode) (*core.EmbeddingRequest, error) {
	if req == nil {
		return nil, nil
	}
	selector, err := p.resolveRequestSelector(req.Model, req.Provider)
	if err != nil {
		return nil, err
	}
	forward := *req
	forward.Model = selector.Model
	forward.Provider = providerValueForMode(selector, mode)
	return &forward, nil
}

func (p *Provider) rewriteBatchSource(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchRewriteResult, error) {
	files, err := p.nativeFileRouter()
	if err != nil {
		files = nil
	}
	return core.RewriteBatchSource(ctx, providerType, req, files, []string{"chat_completions", "responses", "embeddings"}, p.rewriteBatchDecodedItem)
}

func (p *Provider) rewriteBatchDecodedItem(_ context.Context, _ core.BatchRequestItem, decoded *core.DecodedBatchItemRequest) (json.RawMessage, error) {
	switch typed := decoded.Request.(type) {
	case *core.ChatRequest:
		modified, err := p.rewriteChatRequest(typed, rewriteForUpstream)
		if err != nil {
			return nil, err
		}
		body, err := json.Marshal(modified)
		if err != nil {
			return nil, core.NewInvalidRequestError("failed to encode batch item", err)
		}
		return body, nil
	case *core.ResponsesRequest:
		modified, err := p.rewriteResponsesRequest(typed, rewriteForUpstream)
		if err != nil {
			return nil, err
		}
		body, err := json.Marshal(modified)
		if err != nil {
			return nil, core.NewInvalidRequestError("failed to encode batch item", err)
		}
		return body, nil
	case *core.EmbeddingRequest:
		modified, err := p.rewriteEmbeddingRequest(typed, rewriteForUpstream)
		if err != nil {
			return nil, err
		}
		body, err := json.Marshal(modified)
		if err != nil {
			return nil, core.NewInvalidRequestError("failed to encode batch item", err)
		}
		return body, nil
	default:
		return nil, core.NewInvalidRequestError("unsupported batch item url: "+decoded.Endpoint, nil)
	}
}

func (p *Provider) recordBatchPreparation(ctx context.Context, original, rewritten *core.BatchRequest) {
	if ctx == nil || original == nil || rewritten == nil {
		return
	}
	metadata := core.GetBatchPreparationMetadata(ctx)
	if metadata == nil {
		return
	}
	metadata.RecordInputFileRewrite(original.InputFileID, rewritten.InputFileID)
}

func (p *Provider) cleanupSupersededBatchRewriteFile(ctx context.Context, providerType, localRewrittenFileID string) {
	localRewrittenFileID = strings.TrimSpace(localRewrittenFileID)
	if localRewrittenFileID == "" {
		return
	}
	metadata := core.GetBatchPreparationMetadata(ctx)
	if metadata == nil {
		return
	}
	if strings.TrimSpace(metadata.RewrittenInputFileID) == localRewrittenFileID {
		return
	}
	p.cleanupBatchRewriteFile(ctx, providerType, localRewrittenFileID)
}

func (p *Provider) cleanupBatchRewriteFile(ctx context.Context, providerType, fileID string) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return
	}
	files, err := p.nativeFileRouter()
	if err != nil {
		return
	}
	if _, err := files.DeleteFile(ctx, providerType, fileID); err != nil {
		slog.Warn("failed to delete rewritten batch input file", "provider", providerType, "file_id", fileID, "error", err)
	}
}

func mergeBatchHints(left, right map[string]string) map[string]string {
	if len(left) == 0 {
		if len(right) == 0 {
			return nil
		}
		merged := make(map[string]string, len(right))
		for key, value := range right {
			merged[key] = value
		}
		return merged
	}
	merged := make(map[string]string, len(left))
	for key, value := range left {
		merged[key] = value
	}
	for key, value := range right {
		merged[key] = value
	}
	return merged
}

func (p *Provider) resolveRequestSelector(model, provider string) (core.ModelSelector, error) {
	selector, changed, err := p.ResolveModel(model, provider)
	if err != nil {
		return core.ModelSelector{}, err
	}
	if changed {
		return selector, nil
	}
	return core.ParseModelSelector(model, provider)
}

func providerValueForMode(selector core.ModelSelector, mode requestRewriteMode) string {
	if mode == rewriteForUpstream {
		return ""
	}
	return selector.Provider
}

func (p *Provider) nativeBatchRouter() (core.NativeBatchRoutableProvider, error) {
	native, ok := p.inner.(core.NativeBatchRoutableProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("batch routing is not supported by the current provider router", nil)
	}
	return native, nil
}

func (p *Provider) nativeBatchHintRouter() (core.NativeBatchHintRoutableProvider, error) {
	hinted, ok := p.inner.(core.NativeBatchHintRoutableProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("batch hint routing is not supported by the current provider router", nil)
	}
	return hinted, nil
}

func (p *Provider) nativeFileRouter() (core.NativeFileRoutableProvider, error) {
	files, ok := p.inner.(core.NativeFileRoutableProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("file routing is not supported by the current provider router", nil)
	}
	return files, nil
}

func (p *Provider) passthroughRouter() (core.RoutablePassthrough, error) {
	passthrough, ok := p.inner.(core.RoutablePassthrough)
	if !ok {
		return nil, core.NewInvalidRequestError("passthrough routing is not supported by the current provider router", nil)
	}
	return passthrough, nil
}
