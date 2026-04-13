package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	batchstore "gomodel/internal/batch"
	"gomodel/internal/batchrewrite"
	"gomodel/internal/core"
)

// nativeBatchService owns native batch orchestration so the HTTP handler layer
// can delegate batch execution and lifecycle behavior instead of coordinating
// it inline.
type nativeBatchService struct {
	provider                             core.RoutableProvider
	modelResolver                        RequestModelResolver
	modelAuthorizer                      RequestModelAuthorizer
	workflowPolicyResolver               RequestWorkflowPolicyResolver
	batchRequestPreparer                 BatchRequestPreparer
	batchStore                           batchstore.Store
	loadBatch                            func(*echo.Context, string) (*batchstore.StoredBatch, error)
	cleanupPreparedBatchInputFile        func(context.Context, string, string)
	cleanupStoredBatchRewrittenInputFile func(context.Context, *batchstore.StoredBatch) bool
	logBatchUsageFromBatchResults        func(*batchstore.StoredBatch, *core.BatchResultsResponse, string) bool
}

func (s *nativeBatchService) Batches(c *echo.Context) error {
	req, err := canonicalJSONRequestFromSemantics[*core.BatchRequest](c, core.DecodeBatchRequest)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}

	ctx, requestID := requestContextWithRequestID(c.Request())
	batchPreparation := &core.BatchPreparationMetadata{}
	ctx = core.WithBatchPreparationMetadata(ctx, batchPreparation)

	nativeRouter, ok := s.provider.(core.NativeBatchRoutableProvider)
	if !ok {
		return handleError(c, core.NewInvalidRequestError("batch routing is not supported by the current provider router", nil))
	}

	selection, err := determineBatchExecutionSelectionWithAuthorizer(ctx, s.provider, s.modelResolver, s.modelAuthorizer, req)
	if err != nil {
		return handleError(c, err)
	}
	providerType := selection.providerType
	workflow, err := s.storeWorkflowForBatch(c, selection)
	if err != nil {
		return handleError(c, err)
	}
	auditBatchEntry(c, providerType)

	forward := req
	var preparedHints map[string]string
	if s.batchRequestPreparer != nil {
		prepared, err := s.batchRequestPreparer.PrepareBatchRequest(ctx, providerType, req)
		if err != nil {
			return handleError(c, err)
		}
		if prepared != nil {
			if prepared.Request != nil {
				forward = prepared.Request
			}
			batchrewrite.RecordResult(ctx, prepared)
			preparedHints = prepared.RequestEndpointHints
		}
	}

	var (
		upstream *core.BatchResponse
		hints    map[string]string
	)
	if hintedRouter, ok := s.provider.(core.NativeBatchHintRoutableProvider); ok {
		upstream, hints, err = hintedRouter.CreateBatchWithHints(ctx, providerType, forward)
	} else {
		upstream, err = nativeRouter.CreateBatch(ctx, providerType, forward)
	}
	if err != nil {
		s.rollbackPreparedBatch(ctx, providerType, batchPreparation, "")
		return handleError(c, err)
	}
	if upstream == nil {
		s.rollbackPreparedBatch(ctx, providerType, batchPreparation, "")
		return handleError(c, core.NewProviderError(providerType, http.StatusBadGateway, "provider returned empty batch response", nil))
	}

	providerBatchID := upstream.ProviderBatchID
	if providerBatchID == "" {
		providerBatchID = upstream.ID
	}
	if providerBatchID == "" {
		s.rollbackPreparedBatch(ctx, providerType, batchPreparation, "")
		return handleError(c, core.NewProviderError(providerType, http.StatusBadGateway, "provider response missing batch id", nil))
	}

	resp := *upstream
	resp.Provider = providerType
	resp.ProviderBatchID = providerBatchID
	resp.ID = "batch_" + uuid.NewString()
	resp.Object = "batch"
	resp.InputFileID = firstNonEmpty(req.InputFileID, batchPreparation.OriginalInputFileID, resp.InputFileID)
	if resp.Endpoint == "" {
		resp.Endpoint = core.NormalizeOperationPath(req.Endpoint)
	}
	if resp.CompletionWindow == "" {
		resp.CompletionWindow = req.CompletionWindow
	}
	if resp.CompletionWindow == "" {
		resp.CompletionWindow = "24h"
	}
	if resp.Metadata == nil {
		resp.Metadata = map[string]string{}
	}
	resp.Metadata["provider"] = providerType
	resp.Metadata["provider_batch_id"] = providerBatchID
	resp.Metadata = sanitizePublicBatchMetadata(resp.Metadata)

	if s.batchStore != nil {
		mergedHints := batchrewrite.MergeEndpointHints(preparedHints, hints)
		stored := &batchstore.StoredBatch{
			Batch:                     &resp,
			RequestEndpointByCustomID: mergedHints,
			OriginalInputFileID:       batchPreparation.OriginalInputFileID,
			RewrittenInputFileID:      batchPreparation.RewrittenInputFileID,
			RequestID:                 requestID,
			UserPath:                  core.UserPathFromContext(ctx),
			WorkflowVersionID:         workflowVersionID(workflow),
			UsageEnabled:              boolPtr(workflow == nil || workflow.UsageEnabled()),
		}
		if err := s.batchStore.Create(ctx, stored); err != nil {
			s.rollbackPreparedBatch(ctx, providerType, batchPreparation, providerBatchID)
			return handleError(c, core.NewProviderError("batch_store", http.StatusInternalServerError, "failed to persist batch", err))
		}
		if hintedRouter, ok := s.provider.(core.NativeBatchHintRoutableProvider); ok && len(mergedHints) > 0 {
			hintedRouter.ClearBatchResultHints(providerType, providerBatchID)
		}
	}

	return c.JSON(http.StatusOK, resp)
}

func (s *nativeBatchService) rollbackPreparedBatch(ctx context.Context, providerType string, batchPreparation *core.BatchPreparationMetadata, providerBatchID string) {
	if batchPreparation != nil && s.cleanupPreparedBatchInputFile != nil {
		s.cleanupPreparedBatchInputFile(ctx, providerType, batchPreparation.RewrittenInputFileID)
	}
	s.clearUpstreamBatchResultHints(providerType, providerBatchID)
	s.cancelUpstreamBatch(ctx, providerType, providerBatchID)
}

func (s *nativeBatchService) storeWorkflowForBatch(c *echo.Context, selection batchExecutionSelection) (*core.Workflow, error) {
	workflow := cloneCurrentWorkflow(c)
	if workflow == nil {
		return nil, nil
	}
	workflow.Mode = core.ExecutionModeNativeBatch
	workflow.ProviderType = strings.TrimSpace(selection.providerType)

	if s.workflowPolicyResolver != nil {
		selector := core.NewWorkflowSelector(selection.selector.Provider, selection.selector.Model, core.UserPathFromContext(c.Request().Context()))
		if err := applyWorkflowPolicy(c.Request().Context(), workflow, s.workflowPolicyResolver, selector); err != nil {
			return nil, err
		}
	}

	storeWorkflow(c, workflow)
	return workflow, nil
}

func (s *nativeBatchService) clearUpstreamBatchResultHints(providerType, batchID string) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return
	}
	if hintedRouter, ok := s.provider.(core.NativeBatchHintRoutableProvider); ok {
		hintedRouter.ClearBatchResultHints(providerType, batchID)
	}
}

func (s *nativeBatchService) cancelUpstreamBatch(ctx context.Context, providerType, batchID string) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return
	}
	nativeRouter, ok := s.provider.(core.NativeBatchRoutableProvider)
	if !ok {
		return
	}
	if _, err := nativeRouter.CancelBatch(ctx, providerType, batchID); err != nil {
		slog.Warn(
			"failed to cancel upstream batch during rollback",
			"provider", providerType,
			"provider_batch_id", batchID,
			"error", err,
		)
	}
}

func (s *nativeBatchService) GetBatch(c *echo.Context) error {
	ctx, _ := requestContextWithRequestID(c.Request())

	id, err := batchIDFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}

	stored, err := s.requireStoredBatch(c, id)
	if err != nil {
		return handleError(c, err)
	}

	nativeRouter, ok := s.provider.(core.NativeBatchRoutableProvider)
	if !ok || stored.Batch.Provider == "" || stored.Batch.ProviderBatchID == "" {
		return c.JSON(http.StatusOK, stored.Batch)
	}

	latest, err := nativeRouter.GetBatch(ctx, stored.Batch.Provider, stored.Batch.ProviderBatchID)
	if err != nil {
		return handleError(c, err)
	}
	updated := false
	if latest != nil {
		mergeStoredBatchFromUpstream(stored, latest)
		updated = true
	}
	if isTerminalBatchStatus(stored.Batch.Status) && s.cleanupStoredBatchRewrittenInputFile != nil && s.cleanupStoredBatchRewrittenInputFile(ctx, stored) {
		updated = true
	}
	if updated && s.batchStore != nil {
		if err := s.batchStore.Update(c.Request().Context(), stored); err != nil && !errors.Is(err, batchstore.ErrNotFound) {
			return handleError(c, core.NewProviderError("batch_store", http.StatusInternalServerError, "failed to persist refreshed batch", err))
		}
	}

	return c.JSON(http.StatusOK, stored.Batch)
}

func (s *nativeBatchService) ListBatches(c *echo.Context) error {
	batchMeta, err := batchRouteInfoFromSemantics(c)
	if err != nil {
		return handleError(c, err)
	}

	limit := 20
	if batchMeta != nil && batchMeta.HasLimit {
		limit = batchMeta.Limit
	}

	after := ""
	if batchMeta != nil {
		after = strings.TrimSpace(batchMeta.After)
	}
	normalizedLimit := limit
	if normalizedLimit <= 0 {
		normalizedLimit = 20
	}
	if normalizedLimit > 100 {
		normalizedLimit = 100
	}

	items, err := s.batchStore.List(c.Request().Context(), normalizedLimit+1, after)
	if err != nil {
		if errors.Is(err, batchstore.ErrNotFound) {
			return handleError(c, core.NewNotFoundError("after cursor batch not found: "+after))
		}
		return handleError(c, core.NewProviderError("batch_store", http.StatusInternalServerError, "failed to list batches", err))
	}
	auditBatchEntry(c, "")

	hasMore := len(items) > normalizedLimit
	if hasMore {
		items = items[:normalizedLimit]
	}

	data := make([]core.BatchResponse, 0, len(items))
	for _, item := range items {
		if item == nil || item.Batch == nil {
			continue
		}
		data = append(data, *item.Batch)
	}

	resp := core.BatchListResponse{
		Object:  "list",
		Data:    data,
		HasMore: hasMore,
	}
	if len(data) > 0 {
		resp.FirstID = data[0].ID
		resp.LastID = data[len(data)-1].ID
	}

	return c.JSON(http.StatusOK, resp)
}

func (s *nativeBatchService) CancelBatch(c *echo.Context) error {
	ctx, _ := requestContextWithRequestID(c.Request())

	id, err := batchIDFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}

	stored, err := s.requireStoredBatch(c, id)
	if err != nil {
		return handleError(c, err)
	}

	nativeRouter, ok := s.provider.(core.NativeBatchRoutableProvider)
	if !ok || stored.Batch.Provider == "" || stored.Batch.ProviderBatchID == "" {
		return handleError(c, core.NewInvalidRequestError("native batch cancellation is not available", nil))
	}

	latest, err := nativeRouter.CancelBatch(ctx, stored.Batch.Provider, stored.Batch.ProviderBatchID)
	if err != nil {
		return handleError(c, err)
	}
	if latest != nil {
		mergeStoredBatchFromUpstream(stored, latest)
	}
	if isTerminalBatchStatus(stored.Batch.Status) && s.cleanupStoredBatchRewrittenInputFile != nil {
		s.cleanupStoredBatchRewrittenInputFile(ctx, stored)
	}

	if err := s.batchStore.Update(c.Request().Context(), stored); err != nil {
		if errors.Is(err, batchstore.ErrNotFound) {
			return handleError(c, core.NewNotFoundError("batch not found: "+id))
		}
		return handleError(c, core.NewProviderError("batch_store", http.StatusInternalServerError, "failed to cancel batch", err))
	}

	return c.JSON(http.StatusOK, stored.Batch)
}

func (s *nativeBatchService) BatchResults(c *echo.Context) error {
	ctx, requestID := requestContextWithRequestID(c.Request())

	id, err := batchIDFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}

	stored, err := s.requireStoredBatch(c, id)
	if err != nil {
		return handleError(c, err)
	}

	nativeRouter, ok := s.provider.(core.NativeBatchRoutableProvider)
	if !ok || stored.Batch.Provider == "" || stored.Batch.ProviderBatchID == "" {
		return c.JSON(http.StatusOK, core.BatchResultsResponse{
			Object:  "list",
			BatchID: stored.Batch.ID,
			Data:    stored.Batch.Results,
		})
	}

	var upstream *core.BatchResultsResponse
	if hintedRouter, ok := nativeRouter.(core.NativeBatchHintRoutableProvider); ok && len(stored.RequestEndpointByCustomID) > 0 {
		upstream, err = hintedRouter.GetBatchResultsWithHints(ctx, stored.Batch.Provider, stored.Batch.ProviderBatchID, stored.RequestEndpointByCustomID)
	} else {
		upstream, err = nativeRouter.GetBatchResults(ctx, stored.Batch.Provider, stored.Batch.ProviderBatchID)
	}
	if err != nil {
		if pending, latest := isNativeBatchResultsPending(ctx, nativeRouter, stored.Batch.Provider, stored.Batch.ProviderBatchID, err); pending {
			if latest != nil {
				mergeStoredBatchFromUpstream(stored, latest)
				if updateErr := s.batchStore.Update(c.Request().Context(), stored); updateErr != nil && !errors.Is(updateErr, batchstore.ErrNotFound) {
					slog.Warn(
						"failed to update batch store after refreshing pending results",
						"batch_id", stored.Batch.ID,
						"provider", stored.Batch.Provider,
						"provider_batch_id", stored.Batch.ProviderBatchID,
						"error", updateErr,
					)
				}
			}
			status := strings.TrimSpace(stored.Batch.Status)
			if status == "" {
				status = "in_progress"
			}
			return handleError(c, core.NewInvalidRequestErrorWithStatus(
				http.StatusConflict,
				fmt.Sprintf("batch results are not ready yet (status: %s)", status),
				err,
			))
		}
		return handleError(c, err)
	}
	if upstream == nil {
		return handleError(c, core.NewProviderError(stored.Batch.Provider, http.StatusBadGateway, "provider returned empty batch results response", nil))
	}

	result := *upstream
	result.BatchID = stored.Batch.ID
	usageLogged := false
	if s.logBatchUsageFromBatchResults != nil {
		usageLogged = s.logBatchUsageFromBatchResults(stored, &result, requestID)
	}
	if len(result.Data) > 0 {
		stored.Batch.Results = result.Data
	}
	cleanedRewrittenInput := false
	if isTerminalBatchStatus(stored.Batch.Status) && s.cleanupStoredBatchRewrittenInputFile != nil {
		cleanedRewrittenInput = s.cleanupStoredBatchRewrittenInputFile(ctx, stored)
	}
	if len(result.Data) > 0 || usageLogged || cleanedRewrittenInput {
		if updateErr := s.batchStore.Update(c.Request().Context(), stored); updateErr != nil {
			slog.Warn(
				"failed to update batch store after receiving batch results",
				"batch_id", stored.Batch.ID,
				"provider", stored.Batch.Provider,
				"provider_batch_id", stored.Batch.ProviderBatchID,
				"error", updateErr,
			)
		}
	}

	return c.JSON(http.StatusOK, result)
}

func (s *nativeBatchService) requireStoredBatch(c *echo.Context, id string) (*batchstore.StoredBatch, error) {
	if s.loadBatch == nil {
		return nil, core.NewProviderError("batch_store", http.StatusInternalServerError, "batch loader is unavailable", nil)
	}

	stored, err := s.loadBatch(c, id)
	if err != nil {
		return nil, err
	}
	if stored == nil || stored.Batch == nil {
		return nil, core.NewProviderError("batch_store", http.StatusInternalServerError, "stored batch payload missing", nil)
	}
	return stored, nil
}

func batchIDFromRequest(c *echo.Context) (string, error) {
	batchMeta, err := batchRouteInfoFromSemantics(c)
	if err != nil {
		return "", err
	}

	id := ""
	if batchMeta != nil {
		id = strings.TrimSpace(batchMeta.BatchID)
	}
	if id == "" {
		return "", core.NewInvalidRequestError("batch id is required", nil)
	}
	return id, nil
}

func auditBatchEntry(c *echo.Context, providerType string) {
	if c == nil {
		return
	}
	auditlog.EnrichEntry(c, "batch", providerType)
}
