package aliases

import (
	"context"
	"io"
	"sort"
	"strings"

	"gomodel/internal/batchrewrite"
	"gomodel/internal/core"
)

// Provider wraps a routable provider and resolves aliases before delegating.
type Provider struct {
	inner   core.RoutableProvider
	service *Service
	options Options
}

type requestRewriteMode int

const (
	rewriteForRouting requestRewriteMode = iota
	rewriteForUpstream
)

// Options controls optional behavior of Provider.
type Options struct {
	// DisableTranslatedRequestProcessing lets explicit request planning own
	// translated-route selector resolution while this wrapper still exposes
	// alias inventory and batch preparation.
	DisableTranslatedRequestProcessing bool
	// DisableNativeBatchPreparation lets an explicit server-side batch
	// preparer own alias rewriting for native batch requests.
	DisableNativeBatchPreparation bool
}

// NewProvider creates an alias-aware provider wrapper that exposes alias
// inventory and native batch preparation without owning translated-route model
// resolution by default.
func NewProvider(inner core.RoutableProvider, service *Service) *Provider {
	return NewProviderWithOptions(inner, service, Options{
		DisableTranslatedRequestProcessing: true,
	})
}

// NewProviderWithOptions creates an alias-aware provider wrapper with explicit options.
func NewProviderWithOptions(inner core.RoutableProvider, service *Service, options Options) *Provider {
	return &Provider{inner: inner, service: service, options: options}
}

// ResolveModel resolves a requested selector through the alias table.
func (p *Provider) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	return resolveAliasModel(p.service, requested)
}

func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	if p.options.DisableTranslatedRequestProcessing {
		return p.inner.ChatCompletion(ctx, req)
	}
	forward, err := rewriteAliasChatRequest(p.service, p.inner, req, "", rewriteForRouting)
	if err != nil {
		return nil, err
	}
	return p.inner.ChatCompletion(ctx, forward)
}

func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	if p.options.DisableTranslatedRequestProcessing {
		return p.inner.StreamChatCompletion(ctx, req)
	}
	forward, err := rewriteAliasChatRequest(p.service, p.inner, req, "", rewriteForRouting)
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
	if p.options.DisableTranslatedRequestProcessing {
		return p.inner.Responses(ctx, req)
	}
	forward, err := rewriteAliasResponsesRequest(p.service, p.inner, req, "", rewriteForRouting)
	if err != nil {
		return nil, err
	}
	return p.inner.Responses(ctx, forward)
}

func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	if p.options.DisableTranslatedRequestProcessing {
		return p.inner.StreamResponses(ctx, req)
	}
	forward, err := rewriteAliasResponsesRequest(p.service, p.inner, req, "", rewriteForRouting)
	if err != nil {
		return nil, err
	}
	return p.inner.StreamResponses(ctx, forward)
}

func (p *Provider) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	if p.options.DisableTranslatedRequestProcessing {
		return p.inner.Embeddings(ctx, req)
	}
	forward, err := rewriteAliasEmbeddingRequest(p.service, p.inner, req, "", rewriteForRouting)
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

func (p *Provider) GetProviderName(model string) string {
	if named, ok := p.inner.(core.ProviderNameResolver); ok {
		return strings.TrimSpace(named.GetProviderName(model))
	}
	return ""
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
	if p.options.DisableNativeBatchPreparation {
		return native.CreateBatch(ctx, providerType, req)
	}
	result, err := rewriteAliasBatchSource(ctx, providerType, req, p.service, p.inner, p.batchFileTransport())
	if err != nil {
		return nil, err
	}
	batchrewrite.RecordPreparation(ctx, req, result.Request)
	resp, err := native.CreateBatch(ctx, providerType, result.Request)
	if err != nil {
		batchrewrite.CleanupFileFromRouter(ctx, p.nativeFileRouter, providerType, result.RewrittenInputFileID, "")
		return nil, err
	}
	batchrewrite.CleanupSupersededFileFromRouter(ctx, p.nativeFileRouter, providerType, result.RewrittenInputFileID, "")
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
	if p.options.DisableNativeBatchPreparation {
		return hinted.CreateBatchWithHints(ctx, providerType, req)
	}
	result, err := rewriteAliasBatchSource(ctx, providerType, req, p.service, p.inner, p.batchFileTransport())
	if err != nil {
		return nil, nil, err
	}
	batchrewrite.RecordPreparation(ctx, req, result.Request)
	resp, hints, err := hinted.CreateBatchWithHints(ctx, providerType, result.Request)
	if err != nil {
		batchrewrite.CleanupFileFromRouter(ctx, p.nativeFileRouter, providerType, result.RewrittenInputFileID, "")
		return nil, nil, err
	}
	batchrewrite.CleanupSupersededFileFromRouter(ctx, p.nativeFileRouter, providerType, result.RewrittenInputFileID, "")
	return resp, batchrewrite.MergeEndpointHints(result.RequestEndpointHints, hints), nil
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

// PrepareBatchRequest resolves aliases for batch subrequests without
// submitting the native batch to the wrapped provider.
func (p *Provider) PrepareBatchRequest(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchRewriteResult, error) {
	if p.options.DisableNativeBatchPreparation {
		return &core.BatchRewriteResult{Request: req}, nil
	}
	return rewriteAliasBatchSource(ctx, providerType, req, p.service, p.inner, p.batchFileTransport())
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

func (p *Provider) batchFileTransport() core.BatchFileTransport {
	files, err := p.nativeFileRouter()
	if err != nil {
		return nil
	}
	return files
}

func (p *Provider) passthroughRouter() (core.RoutablePassthrough, error) {
	passthrough, ok := p.inner.(core.RoutablePassthrough)
	if !ok {
		return nil, core.NewInvalidRequestError("passthrough routing is not supported by the current provider router", nil)
	}
	return passthrough, nil
}
