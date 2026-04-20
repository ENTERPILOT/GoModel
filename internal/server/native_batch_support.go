package server

import (
	"context"
	"strings"

	batchstore "gomodel/internal/batch"
	"gomodel/internal/batchrewrite"
	"gomodel/internal/core"
	"gomodel/internal/gateway"
)

type batchExecutionSelection struct {
	providerType string
	selector     core.WorkflowSelector
}

func determineBatchExecutionSelection(
	provider core.RoutableProvider,
	resolver RequestModelResolver,
	req *core.BatchRequest,
) (batchExecutionSelection, error) {
	return determineBatchExecutionSelectionWithAuthorizer(context.Background(), provider, resolver, nil, req)
}

func determineBatchExecutionSelectionWithAuthorizer(
	ctx context.Context,
	provider core.RoutableProvider,
	resolver RequestModelResolver,
	authorizer RequestModelAuthorizer,
	req *core.BatchRequest,
) (batchExecutionSelection, error) {
	selection, err := gateway.DetermineBatchExecutionSelectionWithAuthorizer(ctx, provider, resolver, authorizer, req)
	if err != nil {
		return batchExecutionSelection{}, err
	}
	return batchExecutionSelection{
		providerType: selection.ProviderType,
		selector:     selection.Selector,
	}, nil
}

func (h *Handler) cleanupPreparedBatchInputFile(ctx context.Context, providerType, fileID string) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return
	}
	files, ok := h.provider.(core.NativeFileRoutableProvider)
	if !ok {
		return
	}
	batchrewrite.CleanupFile(ctx, files, providerType, fileID, "")
}

func (h *Handler) logBatchUsageFromBatchResults(stored *batchstore.StoredBatch, result *core.BatchResultsResponse, fallbackRequestID string) bool {
	return gateway.LogBatchUsageFromBatchResults(stored, result, fallbackRequestID, h.usageLogger, h.pricingResolver)
}

func firstNonEmpty(values ...string) string {
	return gateway.FirstNonEmpty(values...)
}

func mergeStoredBatchFromUpstream(stored *batchstore.StoredBatch, upstream *core.BatchResponse) {
	gateway.MergeStoredBatchFromUpstream(stored, upstream)
}

func (h *Handler) cleanupStoredBatchRewrittenInputFile(ctx context.Context, stored *batchstore.StoredBatch) bool {
	if stored == nil || stored.Batch == nil {
		return false
	}
	fileID := strings.TrimSpace(stored.RewrittenInputFileID)
	if fileID == "" {
		return false
	}
	nativeFiles, err := h.nativeFiles().router()
	if err != nil {
		return false
	}
	if !batchrewrite.CleanupFile(ctx, nativeFiles, stored.Batch.Provider, fileID, "", "batch_id", stored.Batch.ID) {
		return false
	}
	stored.RewrittenInputFileID = ""
	return true
}

func isNativeBatchResultsPending(
	ctx context.Context,
	nativeRouter core.NativeBatchRoutableProvider,
	providerType, providerBatchID string,
	err error,
) (bool, *core.BatchResponse) {
	return gateway.IsNativeBatchResultsPending(ctx, nativeRouter, providerType, providerBatchID, err)
}
