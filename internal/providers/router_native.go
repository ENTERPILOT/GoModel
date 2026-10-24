package providers

import (
	"context"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// CreateBatch routes native batch creation to a provider type.
func (r *Router) CreateBatch(ctx context.Context, providerType string, req *core.BatchRequest) (*core.BatchResponse, error) {
	forwardReq, err := adaptBatchRequest(ctx, req, providerType)
	if err != nil {
		return nil, err
	}
	resp, err := routeNativeBatchCall(r, ctx, providerType, func(ctx context.Context, bp core.NativeBatchProvider) (*core.BatchResponse, error) {
		return bp.CreateBatch(ctx, forwardReq)
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
	forwardReq, err := adaptBatchRequest(ctx, req, providerType)
	if err != nil {
		return nil, nil, err
	}
	result, err := routeNativeBatchCall(r, ctx, providerType, func(ctx context.Context, bp core.NativeBatchProvider) (createBatchWithHintsResult, error) {
		if hinted, ok := bp.(core.BatchCreateHintAwareProvider); ok {
			resp, hints, err := hinted.CreateBatchWithHints(ctx, forwardReq)
			return createBatchWithHintsResult{resp: resp, hints: hints}, err
		}
		resp, err := bp.CreateBatch(ctx, forwardReq)
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

// DeleteBatch routes native batch deletion to a provider type. It reports
// core.ErrNativeBatchDeleteUnsupported for providers whose upstream batch API
// has no delete operation, so callers can fall back to gateway-local deletion.
func (r *Router) DeleteBatch(ctx context.Context, providerType, id string) error {
	_, err := routeNativeBatchCall(r, ctx, providerType, func(ctx context.Context, bp core.NativeBatchProvider) (struct{}, error) {
		deleter, ok := bp.(core.NativeBatchDeleteProvider)
		if !ok {
			return struct{}{}, core.ErrNativeBatchDeleteUnsupported
		}
		return struct{}{}, deleter.DeleteBatch(ctx, id)
	})
	return err
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

// GetResponse routes native response retrieval to a provider type.
func (r *Router) GetResponse(ctx context.Context, providerType, id string, params core.ResponseRetrieveParams) (*core.ResponsesResponse, error) {
	resp, resolvedProviderType, err := routeNativeResponseLifecycleCall(r, ctx, providerType, func(ctx context.Context, rp core.NativeResponseLifecycleProvider) (*core.ResponsesResponse, error) {
		return rp.GetResponse(ctx, id, params)
	})
	return stampProvider(resp, resolvedProviderType), err
}

// ListResponseInputItems routes native response input item listing to a provider type.
func (r *Router) ListResponseInputItems(ctx context.Context, providerType, id string, params core.ResponseInputItemsParams) (*core.ResponseInputItemListResponse, error) {
	resp, _, err := routeNativeResponseLifecycleCall(r, ctx, providerType, func(ctx context.Context, rp core.NativeResponseLifecycleProvider) (*core.ResponseInputItemListResponse, error) {
		return rp.ListResponseInputItems(ctx, id, params)
	})
	return resp, err
}

// CancelResponse routes native response cancellation to a provider type.
func (r *Router) CancelResponse(ctx context.Context, providerType, id string) (*core.ResponsesResponse, error) {
	resp, resolvedProviderType, err := routeNativeResponseLifecycleCall(r, ctx, providerType, func(ctx context.Context, rp core.NativeResponseLifecycleProvider) (*core.ResponsesResponse, error) {
		return rp.CancelResponse(ctx, id)
	})
	return stampProvider(resp, resolvedProviderType), err
}

// DeleteResponse routes native response deletion to a provider type.
func (r *Router) DeleteResponse(ctx context.Context, providerType, id string) (*core.ResponseDeleteResponse, error) {
	resp, _, err := routeNativeResponseLifecycleCall(r, ctx, providerType, func(ctx context.Context, rp core.NativeResponseLifecycleProvider) (*core.ResponseDeleteResponse, error) {
		return rp.DeleteResponse(ctx, id)
	})
	return resp, err
}

// CountResponseInputTokens routes native response input token counting to a provider type.
func (r *Router) CountResponseInputTokens(ctx context.Context, providerType string, req *core.ResponsesRequest) (*core.ResponseInputTokensResponse, error) {
	resp, _, err := routeNativeResponseUtilityCall(r, ctx, providerType, func(ctx context.Context, rp core.NativeResponseUtilityProvider) (*core.ResponseInputTokensResponse, error) {
		return rp.CountResponseInputTokens(ctx, forwardNativeResponseUtilityRequest(req))
	})
	return resp, err
}

// CompactResponse routes native response compaction to a provider type.
func (r *Router) CompactResponse(ctx context.Context, providerType string, req *core.ResponsesRequest) (*core.ResponseCompactResponse, error) {
	resp, resolvedProviderType, err := routeNativeResponseUtilityCall(r, ctx, providerType, func(ctx context.Context, rp core.NativeResponseUtilityProvider) (*core.ResponseCompactResponse, error) {
		return rp.CompactResponse(ctx, forwardNativeResponseUtilityRequest(req))
	})
	return stampProvider(resp, resolvedProviderType), err
}

func forwardNativeResponseUtilityRequest(req *core.ResponsesRequest) *core.ResponsesRequest {
	if req == nil {
		return nil
	}
	forwardReq := *req
	forwardReq.Provider = ""
	return &forwardReq
}
