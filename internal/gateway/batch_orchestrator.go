package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	batchstore "gomodel/internal/batch"
	"gomodel/internal/batchrewrite"
	"gomodel/internal/core"
	"gomodel/internal/usage"
)

// BatchConfig configures native batch orchestration.
type BatchConfig struct {
	Provider                             core.RoutableProvider
	ModelResolver                        ModelResolver
	ModelAuthorizer                      ModelAuthorizer
	WorkflowPolicyResolver               WorkflowPolicyResolver
	BatchRequestPreparer                 BatchRequestPreparer
	BatchStore                           batchstore.Store
	CleanupPreparedBatchInputFile        func(context.Context, string, string)
	CleanupStoredBatchRewrittenInputFile func(context.Context, *batchstore.StoredBatch) bool
	UsageLogger                          usage.LoggerInterface
	PricingResolver                      usage.PricingResolver
	BudgetEnforcer                       func(context.Context) error
}

// BatchOrchestrator owns native batch lifecycle behavior independent of HTTP.
type BatchOrchestrator struct {
	provider                             core.RoutableProvider
	modelResolver                        ModelResolver
	modelAuthorizer                      ModelAuthorizer
	workflowPolicyResolver               WorkflowPolicyResolver
	batchRequestPreparer                 BatchRequestPreparer
	batchStore                           batchstore.Store
	cleanupPreparedBatchInputFile        func(context.Context, string, string)
	cleanupStoredBatchRewrittenInputFile func(context.Context, *batchstore.StoredBatch) bool
	usageLogger                          usage.LoggerInterface
	pricingResolver                      usage.PricingResolver
	budgetEnforcer                       func(context.Context) error
}

// NewBatchOrchestrator creates a native batch orchestrator.
func NewBatchOrchestrator(cfg BatchConfig) *BatchOrchestrator {
	return &BatchOrchestrator{
		provider:                             cfg.Provider,
		modelResolver:                        cfg.ModelResolver,
		modelAuthorizer:                      cfg.ModelAuthorizer,
		workflowPolicyResolver:               cfg.WorkflowPolicyResolver,
		batchRequestPreparer:                 cfg.BatchRequestPreparer,
		batchStore:                           cfg.BatchStore,
		cleanupPreparedBatchInputFile:        cfg.CleanupPreparedBatchInputFile,
		cleanupStoredBatchRewrittenInputFile: cfg.CleanupStoredBatchRewrittenInputFile,
		usageLogger:                          cfg.UsageLogger,
		pricingResolver:                      cfg.PricingResolver,
		budgetEnforcer:                       cfg.BudgetEnforcer,
	}
}

// BatchMeta carries transport-derived metadata into native batch use cases.
type BatchMeta struct {
	RequestID string
	Endpoint  core.EndpointDescriptor
	Workflow  *core.Workflow
}

// BatchCreateResult is the result of creating a native batch.
type BatchCreateResult struct {
	Batch        *core.BatchResponse
	Workflow     *core.Workflow
	ProviderType string
}

// BatchResult is the result of a single batch lifecycle operation.
type BatchResult struct {
	Batch        *core.BatchResponse
	ProviderType string
}

// BatchListParams contains native batch list pagination options.
type BatchListParams struct {
	Limit int
	After string
}

// BatchResultsResult is the result of retrieving batch output items.
type BatchResultsResult struct {
	Response     *core.BatchResultsResponse
	ProviderType string
}

// Create creates a native provider batch and persists the gateway batch mapping.
func (o *BatchOrchestrator) Create(ctx context.Context, req *core.BatchRequest, meta BatchMeta) (*BatchCreateResult, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("batch request is required", nil)
	}
	ctx = contextWithRequestID(ctx, meta.RequestID)
	batchPreparation := &core.BatchPreparationMetadata{}
	ctx = core.WithBatchPreparationMetadata(ctx, batchPreparation)

	nativeRouter, ok := o.provider.(core.NativeBatchRoutableProvider)
	if !ok {
		return nil, core.NewInvalidRequestError("batch routing is not supported by the current provider router", nil)
	}

	selection, err := DetermineBatchExecutionSelectionWithAuthorizer(ctx, o.provider, o.modelResolver, o.modelAuthorizer, req)
	if err != nil {
		return nil, err
	}
	providerType := selection.ProviderType
	workflow, err := o.workflowForBatch(ctx, meta, selection)
	if err != nil {
		return nil, err
	}
	ctx = core.WithWorkflow(ctx, workflow)
	if o.budgetEnforcer != nil {
		if err := o.budgetEnforcer(ctx); err != nil {
			return nil, err
		}
	}

	forward := req
	var preparedHints map[string]string
	if o.batchRequestPreparer != nil {
		prepared, err := o.batchRequestPreparer.PrepareBatchRequest(ctx, providerType, req)
		if err != nil {
			return nil, err
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
	if hintedRouter, ok := o.provider.(core.NativeBatchHintRoutableProvider); ok {
		upstream, hints, err = hintedRouter.CreateBatchWithHints(ctx, providerType, forward)
	} else {
		upstream, err = nativeRouter.CreateBatch(ctx, providerType, forward)
	}
	if err != nil {
		o.rollbackPreparedBatch(ctx, providerType, batchPreparation, "")
		return nil, err
	}
	if upstream == nil {
		o.rollbackPreparedBatch(ctx, providerType, batchPreparation, "")
		return nil, core.NewProviderError(providerType, http.StatusBadGateway, "provider returned empty batch response", nil)
	}

	providerBatchID := upstream.ProviderBatchID
	if providerBatchID == "" {
		providerBatchID = upstream.ID
	}
	if providerBatchID == "" {
		o.rollbackPreparedBatch(ctx, providerType, batchPreparation, "")
		return nil, core.NewProviderError(providerType, http.StatusBadGateway, "provider response missing batch id", nil)
	}

	resp := *upstream
	resp.Provider = providerType
	resp.ProviderBatchID = providerBatchID
	resp.ID = "batch_" + uuid.NewString()
	resp.Object = "batch"
	resp.InputFileID = FirstNonEmpty(req.InputFileID, batchPreparation.OriginalInputFileID, resp.InputFileID)
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
	resp.Metadata = SanitizePublicBatchMetadata(resp.Metadata)

	if o.batchStore != nil {
		mergedHints := batchrewrite.MergeEndpointHints(preparedHints, hints)
		stored := &batchstore.StoredBatch{
			Batch:                     &resp,
			RequestEndpointByCustomID: mergedHints,
			OriginalInputFileID:       batchPreparation.OriginalInputFileID,
			RewrittenInputFileID:      batchPreparation.RewrittenInputFileID,
			RequestID:                 strings.TrimSpace(meta.RequestID),
			UserPath:                  core.UserPathFromContext(ctx),
			WorkflowVersionID:         workflowVersionID(workflow),
			UsageEnabled:              boolPtr(workflow == nil || workflow.UsageEnabled()),
		}
		if err := o.batchStore.Create(ctx, stored); err != nil {
			o.rollbackPreparedBatch(ctx, providerType, batchPreparation, providerBatchID)
			return nil, core.NewProviderError("batch_store", http.StatusInternalServerError, "failed to persist batch", err)
		}
		if hintedRouter, ok := o.provider.(core.NativeBatchHintRoutableProvider); ok && len(mergedHints) > 0 {
			hintedRouter.ClearBatchResultHints(providerType, providerBatchID)
		}
	}

	return &BatchCreateResult{Batch: &resp, Workflow: workflow, ProviderType: providerType}, nil
}

func (o *BatchOrchestrator) workflowForBatch(ctx context.Context, meta BatchMeta, selection BatchExecutionSelection) (*core.Workflow, error) {
	workflow := cloneWorkflow(meta.Workflow)
	if workflow == nil {
		workflow = &core.Workflow{
			RequestID:    strings.TrimSpace(meta.RequestID),
			Endpoint:     meta.Endpoint,
			Capabilities: core.CapabilitiesForEndpoint(meta.Endpoint),
		}
	}
	workflow.Mode = core.ExecutionModeNativeBatch
	workflow.ProviderType = strings.TrimSpace(selection.ProviderType)

	if o.workflowPolicyResolver != nil {
		selector := core.NewWorkflowSelector(selection.Selector.Provider, selection.Selector.Model, core.UserPathFromContext(ctx))
		if err := ApplyWorkflowPolicy(ctx, workflow, o.workflowPolicyResolver, selector); err != nil {
			return nil, err
		}
	}
	return workflow, nil
}

func cloneWorkflow(workflow *core.Workflow) *core.Workflow {
	if workflow == nil {
		return nil
	}
	cloned := *workflow
	return &cloned
}

func (o *BatchOrchestrator) rollbackPreparedBatch(ctx context.Context, providerType string, batchPreparation *core.BatchPreparationMetadata, providerBatchID string) {
	if batchPreparation != nil && o.cleanupPreparedBatchInputFile != nil {
		o.cleanupPreparedBatchInputFile(ctx, providerType, batchPreparation.RewrittenInputFileID)
	}
	o.clearUpstreamBatchResultHints(providerType, providerBatchID)
	o.cancelUpstreamBatch(ctx, providerType, providerBatchID)
}

func (o *BatchOrchestrator) clearUpstreamBatchResultHints(providerType, batchID string) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return
	}
	if hintedRouter, ok := o.provider.(core.NativeBatchHintRoutableProvider); ok {
		hintedRouter.ClearBatchResultHints(providerType, batchID)
	}
}

func (o *BatchOrchestrator) cancelUpstreamBatch(ctx context.Context, providerType, batchID string) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return
	}
	nativeRouter, ok := o.provider.(core.NativeBatchRoutableProvider)
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

// Get refreshes and returns a stored batch.
func (o *BatchOrchestrator) Get(ctx context.Context, id string) (*BatchResult, error) {
	stored, err := o.requireStoredBatch(ctx, id)
	if err != nil {
		return nil, err
	}

	nativeRouter, ok := o.provider.(core.NativeBatchRoutableProvider)
	if !ok || stored.Batch.Provider == "" || stored.Batch.ProviderBatchID == "" {
		return &BatchResult{Batch: stored.Batch, ProviderType: stored.Batch.Provider}, nil
	}

	latest, err := nativeRouter.GetBatch(ctx, stored.Batch.Provider, stored.Batch.ProviderBatchID)
	if err != nil {
		return nil, err
	}
	updated := false
	if latest != nil {
		MergeStoredBatchFromUpstream(stored, latest)
		updated = true
	}
	if IsTerminalBatchStatus(stored.Batch.Status) && o.cleanupStoredBatchRewrittenInputFile != nil && o.cleanupStoredBatchRewrittenInputFile(ctx, stored) {
		updated = true
	}
	if updated && o.batchStore != nil {
		if err := o.batchStore.Update(ctx, stored); err != nil && !errors.Is(err, batchstore.ErrNotFound) {
			return nil, core.NewProviderError("batch_store", http.StatusInternalServerError, "failed to persist refreshed batch", err)
		}
	}

	return &BatchResult{Batch: stored.Batch, ProviderType: stored.Batch.Provider}, nil
}

// List returns gateway-tracked batches.
func (o *BatchOrchestrator) List(ctx context.Context, params BatchListParams) (*core.BatchListResponse, error) {
	if o.batchStore == nil {
		return nil, core.NewProviderError("batch_store", http.StatusInternalServerError, "batch store is unavailable", nil)
	}

	limit := params.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	after := strings.TrimSpace(params.After)

	items, err := o.batchStore.List(ctx, limit+1, after)
	if err != nil {
		if errors.Is(err, batchstore.ErrNotFound) {
			return nil, core.NewNotFoundError("after cursor batch not found: " + after)
		}
		return nil, core.NewProviderError("batch_store", http.StatusInternalServerError, "failed to list batches", err)
	}

	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}

	data := make([]core.BatchResponse, 0, len(items))
	for _, item := range items {
		if item == nil || item.Batch == nil {
			continue
		}
		data = append(data, *item.Batch)
	}

	resp := &core.BatchListResponse{
		Object:  "list",
		Data:    data,
		HasMore: hasMore,
	}
	if len(data) > 0 {
		resp.FirstID = data[0].ID
		resp.LastID = data[len(data)-1].ID
	}
	return resp, nil
}

// Cancel cancels an upstream native batch and persists refreshed state.
func (o *BatchOrchestrator) Cancel(ctx context.Context, id string) (*BatchResult, error) {
	stored, err := o.requireStoredBatch(ctx, id)
	if err != nil {
		return nil, err
	}

	nativeRouter, ok := o.provider.(core.NativeBatchRoutableProvider)
	if !ok || stored.Batch.Provider == "" || stored.Batch.ProviderBatchID == "" {
		return nil, core.NewInvalidRequestError("native batch cancellation is not available", nil)
	}

	latest, err := nativeRouter.CancelBatch(ctx, stored.Batch.Provider, stored.Batch.ProviderBatchID)
	if err != nil {
		return nil, err
	}
	if latest != nil {
		MergeStoredBatchFromUpstream(stored, latest)
	}
	if IsTerminalBatchStatus(stored.Batch.Status) && o.cleanupStoredBatchRewrittenInputFile != nil {
		o.cleanupStoredBatchRewrittenInputFile(ctx, stored)
	}

	if err := o.batchStore.Update(ctx, stored); err != nil {
		if errors.Is(err, batchstore.ErrNotFound) {
			return nil, core.NewNotFoundError("batch not found: " + id)
		}
		return nil, core.NewProviderError("batch_store", http.StatusInternalServerError, "failed to cancel batch", err)
	}

	return &BatchResult{Batch: stored.Batch, ProviderType: stored.Batch.Provider}, nil
}

// Results returns native batch output items and persists result/usage refreshes.
func (o *BatchOrchestrator) Results(ctx context.Context, id, fallbackRequestID string) (*BatchResultsResult, error) {
	stored, err := o.requireStoredBatch(ctx, id)
	if err != nil {
		return nil, err
	}

	nativeRouter, ok := o.provider.(core.NativeBatchRoutableProvider)
	if !ok || stored.Batch.Provider == "" || stored.Batch.ProviderBatchID == "" {
		return &BatchResultsResult{
			Response: &core.BatchResultsResponse{
				Object:  "list",
				BatchID: stored.Batch.ID,
				Data:    stored.Batch.Results,
			},
			ProviderType: stored.Batch.Provider,
		}, nil
	}

	var upstream *core.BatchResultsResponse
	if hintedRouter, ok := nativeRouter.(core.NativeBatchHintRoutableProvider); ok && len(stored.RequestEndpointByCustomID) > 0 {
		upstream, err = hintedRouter.GetBatchResultsWithHints(ctx, stored.Batch.Provider, stored.Batch.ProviderBatchID, stored.RequestEndpointByCustomID)
	} else {
		upstream, err = nativeRouter.GetBatchResults(ctx, stored.Batch.Provider, stored.Batch.ProviderBatchID)
	}
	if err != nil {
		if pending, latest := IsNativeBatchResultsPending(ctx, nativeRouter, stored.Batch.Provider, stored.Batch.ProviderBatchID, err); pending {
			if latest != nil {
				MergeStoredBatchFromUpstream(stored, latest)
				if updateErr := o.batchStore.Update(ctx, stored); updateErr != nil && !errors.Is(updateErr, batchstore.ErrNotFound) {
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
			return nil, core.NewInvalidRequestErrorWithStatus(
				http.StatusConflict,
				fmt.Sprintf("batch results are not ready yet (status: %s)", status),
				err,
			)
		}
		return nil, err
	}
	if upstream == nil {
		return nil, core.NewProviderError(stored.Batch.Provider, http.StatusBadGateway, "provider returned empty batch results response", nil)
	}

	result := *upstream
	result.BatchID = stored.Batch.ID
	usageLogged := LogBatchUsageFromBatchResults(stored, &result, fallbackRequestID, o.usageLogger, o.pricingResolver)
	if len(result.Data) > 0 {
		stored.Batch.Results = result.Data
	}
	cleanedRewrittenInput := false
	if IsTerminalBatchStatus(stored.Batch.Status) && o.cleanupStoredBatchRewrittenInputFile != nil {
		cleanedRewrittenInput = o.cleanupStoredBatchRewrittenInputFile(ctx, stored)
	}
	if len(result.Data) > 0 || usageLogged || cleanedRewrittenInput {
		if updateErr := o.batchStore.Update(ctx, stored); updateErr != nil {
			slog.Warn(
				"failed to update batch store after receiving batch results",
				"batch_id", stored.Batch.ID,
				"provider", stored.Batch.Provider,
				"provider_batch_id", stored.Batch.ProviderBatchID,
				"error", updateErr,
			)
		}
	}

	return &BatchResultsResult{Response: &result, ProviderType: stored.Batch.Provider}, nil
}

func (o *BatchOrchestrator) requireStoredBatch(ctx context.Context, id string) (*batchstore.StoredBatch, error) {
	if o.batchStore == nil {
		return nil, core.NewProviderError("batch_store", http.StatusInternalServerError, "batch store is unavailable", nil)
	}

	stored, err := o.batchStore.Get(ctx, id)
	if err != nil {
		if errors.Is(err, batchstore.ErrNotFound) {
			return nil, core.NewNotFoundError("batch not found: " + id)
		}
		return nil, core.NewProviderError("batch_store", http.StatusInternalServerError, "failed to load batch", err)
	}
	if stored == nil || stored.Batch == nil {
		return nil, core.NewProviderError("batch_store", http.StatusInternalServerError, "stored batch payload missing", nil)
	}
	return stored, nil
}

func workflowVersionID(workflow *core.Workflow) string {
	if workflow == nil {
		return ""
	}
	return workflow.WorkflowVersionID()
}

func boolPtr(value bool) *bool {
	return &value
}
