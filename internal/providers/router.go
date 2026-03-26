// Package providers provides a router for multiple LLM providers.
package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"gomodel/internal/core"
)

// ErrRegistryNotInitialized is returned when the router is used before the registry has any models.
var ErrRegistryNotInitialized = fmt.Errorf("model registry has no models: ensure Initialize() or LoadFromCache() is called before using the router")

// Router routes requests to the appropriate provider based on the model lookup.
// It uses a dynamic model-to-provider mapping that is populated at startup
// by fetching available models from each provider's /models endpoint.
type Router struct {
	lookup core.ModelLookup
}

type providerTypeRegistry interface {
	ProviderByType(providerType string) core.Provider
}

type initializedLookup interface {
	IsInitialized() bool
}

type providerTypeLister interface {
	ProviderTypes() []string
}

type publicModelLister interface {
	ListPublicModels() []core.Model
}

func registryUnavailableError(err error) error {
	return core.NewProviderError("", http.StatusServiceUnavailable, err.Error(), err)
}

// NewRouter creates a new provider router with a model lookup.
// The lookup must be initialized (via Initialize() or LoadFromCache()) before using the router.
// Returns an error if the lookup is nil.
func NewRouter(lookup core.ModelLookup) (*Router, error) {
	if lookup == nil {
		return nil, fmt.Errorf("lookup cannot be nil")
	}
	return &Router{
		lookup: lookup,
	}, nil
}

// checkReady verifies the lookup has models available.
// Returns ErrRegistryNotInitialized if no models are loaded.
func (r *Router) checkReady() error {
	if r.lookup.ModelCount() == 0 {
		return ErrRegistryNotInitialized
	}
	return nil
}

// resolveProvider validates readiness, parses the model selector, and finds the target provider.
func (r *Router) resolveProvider(model, providerHint string) (core.Provider, core.ModelSelector, error) {
	if err := r.checkReady(); err != nil {
		return nil, core.ModelSelector{}, registryUnavailableError(err)
	}
	selector, err := core.ParseModelSelector(model, providerHint)
	if err != nil {
		return nil, core.ModelSelector{}, core.NewInvalidRequestError(err.Error(), err)
	}
	lookupModel := selector.QualifiedModel()
	p := r.lookup.GetProvider(lookupModel)
	if p == nil {
		return nil, core.ModelSelector{}, core.NewNotFoundError("model not found: " + lookupModel)
	}
	return p, selector, nil
}

func (r *Router) resolveProviderType(providerType string) (core.Provider, error) {
	if initialized, ok := r.lookup.(initializedLookup); ok {
		if !initialized.IsInitialized() {
			if err := r.checkReady(); err != nil {
				if errors.Is(err, ErrRegistryNotInitialized) {
					return nil, registryUnavailableError(err)
				}
				return nil, err
			}
		}
	} else if err := r.checkReady(); err != nil {
		if errors.Is(err, ErrRegistryNotInitialized) {
			return nil, registryUnavailableError(err)
		}
		return nil, err
	}
	if providerType == "" {
		return nil, core.NewInvalidRequestError("provider type is required", nil)
	}
	provider := r.providerByTypeRegistry(providerType)
	if provider == nil {
		return nil, core.NewInvalidRequestError(fmt.Sprintf("no provider found for provider type: %s", providerType), nil)
	}
	return provider, nil
}

func (r *Router) resolveNativeBatchProvider(providerType string) (core.NativeBatchProvider, error) {
	provider, err := r.resolveProviderType(providerType)
	if err != nil {
		return nil, err
	}
	bp, ok := provider.(core.NativeBatchProvider)
	if !ok {
		return nil, core.NewInvalidRequestError(fmt.Sprintf("%s does not support native batch processing", providerType), nil)
	}
	return bp, nil
}

func (r *Router) resolveNativeFileProvider(providerType string) (core.NativeFileProvider, error) {
	provider, err := r.resolveProviderType(providerType)
	if err != nil {
		return nil, err
	}
	fp, ok := provider.(core.NativeFileProvider)
	if !ok {
		return nil, core.NewInvalidRequestError(fmt.Sprintf("%s does not support native file operations", providerType), nil)
	}
	return fp, nil
}

func (r *Router) resolvePassthroughProvider(providerType string) (core.PassthroughProvider, error) {
	provider, err := r.resolveProviderType(providerType)
	if err != nil {
		return nil, err
	}
	pp, ok := provider.(core.PassthroughProvider)
	if !ok {
		return nil, core.NewInvalidRequestError(fmt.Sprintf("%s does not support provider passthrough", providerType), nil)
	}
	return pp, nil
}

func routeResolvedModelCall[Req any, Resp any](
	r *Router,
	ctx context.Context,
	model string,
	providerHint string,
	buildForward func(core.ModelSelector) Req,
	call func(context.Context, core.Provider, Req) (Resp, error),
) (Resp, string, error) {
	p, selector, err := r.resolveProvider(model, providerHint)
	if err != nil {
		var zero Resp
		return zero, "", err
	}

	resp, err := call(ctx, p, buildForward(selector))
	return resp, r.GetProviderType(selector.QualifiedModel()), err
}

func routeStampedModelResponse[Req any, Resp any](
	r *Router,
	ctx context.Context,
	model string,
	providerHint string,
	buildForward func(core.ModelSelector) Req,
	call func(context.Context, core.Provider, Req) (Resp, error),
) (Resp, error) {
	resp, providerType, err := routeResolvedModelCall(r, ctx, model, providerHint, buildForward, call)
	if err != nil {
		var zero Resp
		return zero, err
	}
	return stampProvider(resp, providerType), nil
}

func routeNativeBatchCall[T any](r *Router, ctx context.Context, providerType string, call func(context.Context, core.NativeBatchProvider) (T, error)) (T, error) {
	bp, err := r.resolveNativeBatchProvider(providerType)
	if err != nil {
		var zero T
		return zero, err
	}
	return call(ctx, bp)
}

func routeNativeFileCall[T any](r *Router, ctx context.Context, providerType string, call func(context.Context, core.NativeFileProvider) (T, error)) (T, error) {
	fp, err := r.resolveNativeFileProvider(providerType)
	if err != nil {
		var zero T
		return zero, err
	}
	return call(ctx, fp)
}

func stampProvider[T any](resp T, providerType string) T {
	switch typed := any(resp).(type) {
	case *core.ChatResponse:
		if typed != nil {
			typed.Provider = providerType
		}
	case *core.ResponsesResponse:
		if typed != nil {
			typed.Provider = providerType
		}
	case *core.EmbeddingResponse:
		if typed != nil {
			typed.Provider = providerType
		}
	case *core.BatchResponse:
		if typed != nil {
			typed.Provider = providerType
		}
	case *core.FileObject:
		if typed != nil {
			typed.Provider = providerType
		}
	}
	return resp
}

// Provider is gateway routing metadata on OpenAI-compatible request structs and
// must be removed before dispatching to an upstream provider implementation.
func forwardChatRequest(req *core.ChatRequest, selector core.ModelSelector) *core.ChatRequest {
	forwardReq := *req
	forwardReq.Model = selector.Model
	forwardReq.Provider = ""
	return &forwardReq
}

func forwardResponsesRequest(req *core.ResponsesRequest, selector core.ModelSelector) *core.ResponsesRequest {
	forwardReq := *req
	forwardReq.Model = selector.Model
	forwardReq.Provider = ""
	return &forwardReq
}

func forwardEmbeddingRequest(req *core.EmbeddingRequest, selector core.ModelSelector) *core.EmbeddingRequest {
	forwardReq := *req
	forwardReq.Model = selector.Model
	forwardReq.Provider = ""
	return &forwardReq
}

func callChatCompletion(ctx context.Context, provider core.Provider, req *core.ChatRequest) (*core.ChatResponse, error) {
	return provider.ChatCompletion(ctx, req)
}

func callResponses(ctx context.Context, provider core.Provider, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return provider.Responses(ctx, req)
}

func callEmbeddings(ctx context.Context, provider core.Provider, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return provider.Embeddings(ctx, req)
}

// Supports returns true if any provider supports the given model.
// Returns false if the lookup has no models loaded.
func (r *Router) Supports(model string) bool {
	if r.lookup.ModelCount() == 0 {
		return false
	}
	return r.lookup.Supports(model)
}

// ModelCount returns the number of models currently loaded into the router lookup.
func (r *Router) ModelCount() int {
	if r == nil || r.lookup == nil {
		return 0
	}
	return r.lookup.ModelCount()
}

// ChatCompletion routes the request to the appropriate provider.
// Returns ErrRegistryNotInitialized if the lookup has no models loaded.
func (r *Router) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	return routeStampedModelResponse(
		r,
		ctx,
		req.Model,
		req.Provider,
		func(selector core.ModelSelector) *core.ChatRequest {
			return forwardChatRequest(req, selector)
		},
		callChatCompletion,
	)
}

// StreamChatCompletion routes the streaming request to the appropriate provider.
// Returns ErrRegistryNotInitialized if the lookup has no models loaded.
func (r *Router) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	stream, _, err := routeResolvedModelCall(
		r,
		ctx,
		req.Model,
		req.Provider,
		func(selector core.ModelSelector) *core.ChatRequest {
			return forwardChatRequest(req, selector)
		},
		func(ctx context.Context, provider core.Provider, forwardReq *core.ChatRequest) (io.ReadCloser, error) {
			return provider.StreamChatCompletion(ctx, forwardReq)
		},
	)
	return stream, err
}

// ListModels returns all models from the lookup.
// Returns ErrRegistryNotInitialized if the lookup has no models loaded.
func (r *Router) ListModels(_ context.Context) (*core.ModelsResponse, error) {
	if err := r.checkReady(); err != nil {
		return nil, registryUnavailableError(err)
	}
	var models []core.Model
	if public, ok := r.lookup.(publicModelLister); ok {
		models = public.ListPublicModels()
	} else {
		models = r.lookup.ListModels()
	}
	return &core.ModelsResponse{
		Object: "list",
		Data:   models,
	}, nil
}

// Responses routes the Responses API request to the appropriate provider.
// Returns ErrRegistryNotInitialized if the lookup has no models loaded.
func (r *Router) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return routeStampedModelResponse(
		r,
		ctx,
		req.Model,
		req.Provider,
		func(selector core.ModelSelector) *core.ResponsesRequest {
			return forwardResponsesRequest(req, selector)
		},
		callResponses,
	)
}

// StreamResponses routes the streaming Responses API request to the appropriate provider.
// Returns ErrRegistryNotInitialized if the lookup has no models loaded.
func (r *Router) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	stream, _, err := routeResolvedModelCall(
		r,
		ctx,
		req.Model,
		req.Provider,
		func(selector core.ModelSelector) *core.ResponsesRequest {
			return forwardResponsesRequest(req, selector)
		},
		func(ctx context.Context, provider core.Provider, forwardReq *core.ResponsesRequest) (io.ReadCloser, error) {
			return provider.StreamResponses(ctx, forwardReq)
		},
	)
	return stream, err
}

// Embeddings routes the embeddings request to the appropriate provider.
func (r *Router) Embeddings(ctx context.Context, req *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return routeStampedModelResponse(
		r,
		ctx,
		req.Model,
		req.Provider,
		func(selector core.ModelSelector) *core.EmbeddingRequest {
			return forwardEmbeddingRequest(req, selector)
		},
		callEmbeddings,
	)
}

// GetProviderType returns the provider type string for the given model.
// Returns empty string if the model is not found.
func (r *Router) GetProviderType(model string) string {
	return r.lookup.GetProviderType(model)
}

func (r *Router) providerByType(providerType string) core.Provider {
	models := r.lookup.ListModels()
	for _, model := range models {
		if r.lookup.GetProviderType(model.ID) != providerType {
			continue
		}
		p := r.lookup.GetProvider(model.ID)
		if p != nil {
			return p
		}
	}
	return nil
}

func (r *Router) providerByTypeRegistry(providerType string) core.Provider {
	if registry, ok := r.lookup.(providerTypeRegistry); ok {
		if provider := registry.ProviderByType(providerType); provider != nil {
			return provider
		}
	}
	return r.providerByType(providerType)
}

func (r *Router) providerTypes() []string {
	if typed, ok := r.lookup.(providerTypeLister); ok {
		return typed.ProviderTypes()
	}

	seen := make(map[string]struct{})
	result := make([]string, 0)
	for _, model := range r.lookup.ListModels() {
		providerType := strings.TrimSpace(r.lookup.GetProviderType(model.ID))
		if providerType == "" {
			continue
		}
		if _, exists := seen[providerType]; exists {
			continue
		}
		seen[providerType] = struct{}{}
		result = append(result, providerType)
	}
	sort.Strings(result)
	return result
}

// NativeFileProviderTypes returns the registered provider types that support
// native file operations. This inventory is independent of the public model
// catalog whenever the underlying lookup can expose provider types directly.
func (r *Router) NativeFileProviderTypes() []string {
	providerTypes := r.providerTypes()
	result := make([]string, 0, len(providerTypes))
	for _, providerType := range providerTypes {
		provider := r.providerByTypeRegistry(providerType)
		if provider == nil {
			continue
		}
		if _, ok := provider.(core.NativeFileProvider); !ok {
			continue
		}
		result = append(result, providerType)
	}
	return result
}

// Passthrough routes an opaque provider-native request by provider type.
func (r *Router) Passthrough(ctx context.Context, providerType string, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	pp, err := r.resolvePassthroughProvider(providerType)
	if err != nil {
		return nil, err
	}
	return pp.Passthrough(ctx, req)
}

// CreateBatch routes native batch creation to a provider type.
func (r *Router) CreateBatch(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchResponse, error) {
	resp, err := routeNativeBatchCall(r, ctx, providerType, func(ctx context.Context, bp core.NativeBatchProvider) (*core.BatchResponse, error) {
		return bp.CreateBatch(ctx, req)
	})
	return stampProvider(resp, providerType), err
}

// CreateBatchWithHints routes native batch creation and returns any provider
// batch-result shaping hints that need gateway persistence.
func (r *Router) CreateBatchWithHints(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchResponse, map[string]string, error) {
	type createBatchWithHintsResult struct {
		resp  *core.BatchResponse
		hints map[string]string
	}
	result, err := routeNativeBatchCall(r, ctx, providerType, func(ctx context.Context, bp core.NativeBatchProvider) (createBatchWithHintsResult, error) {
		if hinted, ok := bp.(core.BatchCreateHintAwareProvider); ok {
			resp, hints, err := hinted.CreateBatchWithHints(ctx, req)
			return createBatchWithHintsResult{resp: resp, hints: hints}, err
		}
		resp, err := bp.CreateBatch(ctx, req)
		return createBatchWithHintsResult{resp: resp}, err
	})
	return stampProvider(result.resp, providerType), result.hints, err
}

// GetBatch routes native batch lookup to a provider type.
func (r *Router) GetBatch(ctx context.Context, providerType, id string) (*core.BatchResponse, error) {
	resp, err := routeNativeBatchCall(r, ctx, providerType, func(ctx context.Context, bp core.NativeBatchProvider) (*core.BatchResponse, error) {
		return bp.GetBatch(ctx, id)
	})
	return stampProvider(resp, providerType), err
}

// ListBatches routes native batch listing to a provider type.
func (r *Router) ListBatches(ctx context.Context, providerType string, limit int, after string) (*core.BatchListResponse, error) {
	resp, err := routeNativeBatchCall(r, ctx, providerType, func(ctx context.Context, bp core.NativeBatchProvider) (*core.BatchListResponse, error) {
		return bp.ListBatches(ctx, limit, after)
	})
	if err != nil {
		return nil, err
	}
	if resp != nil {
		for i := range resp.Data {
			resp.Data[i].Provider = providerType
		}
	}
	return resp, nil
}

// CancelBatch routes native batch cancellation to a provider type.
func (r *Router) CancelBatch(ctx context.Context, providerType, id string) (*core.BatchResponse, error) {
	resp, err := routeNativeBatchCall(r, ctx, providerType, func(ctx context.Context, bp core.NativeBatchProvider) (*core.BatchResponse, error) {
		return bp.CancelBatch(ctx, id)
	})
	return stampProvider(resp, providerType), err
}

// GetBatchResults routes native batch results lookup to a provider type.
func (r *Router) GetBatchResults(ctx context.Context, providerType, id string) (*core.BatchResultsResponse, error) {
	return routeNativeBatchCall(r, ctx, providerType, func(ctx context.Context, bp core.NativeBatchProvider) (*core.BatchResultsResponse, error) {
		return bp.GetBatchResults(ctx, id)
	})
}

// GetBatchResultsWithHints routes native batch results lookup with persisted
// per-item endpoint hints when the provider supports them.
func (r *Router) GetBatchResultsWithHints(ctx context.Context, providerType, id string, endpointByCustomID map[string]string) (*core.BatchResultsResponse, error) {
	return routeNativeBatchCall(r, ctx, providerType, func(ctx context.Context, bp core.NativeBatchProvider) (*core.BatchResultsResponse, error) {
		if hinted, ok := bp.(core.BatchResultHintAwareProvider); ok && len(endpointByCustomID) > 0 {
			return hinted.GetBatchResultsWithHints(ctx, id, endpointByCustomID)
		}
		return bp.GetBatchResults(ctx, id)
	})
}

// ClearBatchResultHints clears transient provider-side batch result hints once
// they have been persisted by the gateway.
func (r *Router) ClearBatchResultHints(providerType, batchID string) {
	if strings.TrimSpace(batchID) == "" {
		return
	}
	bp, err := r.resolveNativeBatchProvider(providerType)
	if err != nil {
		return
	}
	hinted, ok := bp.(core.BatchResultHintAwareProvider)
	if !ok {
		return
	}
	hinted.ClearBatchResultHints(batchID)
}

// CreateFile routes file upload to a provider type.
func (r *Router) CreateFile(ctx context.Context, providerType string, req *core.FileCreateRequest) (*core.FileObject, error) {
	resp, err := routeNativeFileCall(r, ctx, providerType, func(ctx context.Context, fp core.NativeFileProvider) (*core.FileObject, error) {
		return fp.CreateFile(ctx, req)
	})
	return stampProvider(resp, providerType), err
}

// ListFiles routes file listing to a provider type.
func (r *Router) ListFiles(ctx context.Context, providerType, purpose string, limit int, after string) (*core.FileListResponse, error) {
	resp, err := routeNativeFileCall(r, ctx, providerType, func(ctx context.Context, fp core.NativeFileProvider) (*core.FileListResponse, error) {
		return fp.ListFiles(ctx, purpose, limit, after)
	})
	if err != nil {
		return nil, err
	}
	if resp != nil {
		for i := range resp.Data {
			resp.Data[i].Provider = providerType
		}
	}
	return resp, nil
}

// GetFile routes file retrieval to a provider type.
func (r *Router) GetFile(ctx context.Context, providerType, id string) (*core.FileObject, error) {
	resp, err := routeNativeFileCall(r, ctx, providerType, func(ctx context.Context, fp core.NativeFileProvider) (*core.FileObject, error) {
		return fp.GetFile(ctx, id)
	})
	return stampProvider(resp, providerType), err
}

// DeleteFile routes file deletion to a provider type.
func (r *Router) DeleteFile(ctx context.Context, providerType, id string) (*core.FileDeleteResponse, error) {
	return routeNativeFileCall(r, ctx, providerType, func(ctx context.Context, fp core.NativeFileProvider) (*core.FileDeleteResponse, error) {
		return fp.DeleteFile(ctx, id)
	})
}

// GetFileContent routes file content retrieval to a provider type.
func (r *Router) GetFileContent(ctx context.Context, providerType, id string) (*core.FileContentResponse, error) {
	return routeNativeFileCall(r, ctx, providerType, func(ctx context.Context, fp core.NativeFileProvider) (*core.FileContentResponse, error) {
		return fp.GetFileContent(ctx, id)
	})
}
