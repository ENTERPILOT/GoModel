package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	batchstore "gomodel/internal/batch"
	"gomodel/internal/core"
	"gomodel/internal/gateway"
	"gomodel/internal/usage"
)

// nativeBatchService adapts Echo requests to the transport-independent native
// batch orchestrator.
type nativeBatchService struct {
	provider                             core.RoutableProvider
	modelResolver                        RequestModelResolver
	modelAuthorizer                      RequestModelAuthorizer
	workflowPolicyResolver               RequestWorkflowPolicyResolver
	batchRequestPreparer                 BatchRequestPreparer
	batchStore                           batchstore.Store
	cleanupPreparedBatchInputFile        func(context.Context, string, string)
	cleanupStoredBatchRewrittenInputFile func(context.Context, *batchstore.StoredBatch) bool
	usageLogger                          usage.LoggerInterface
	budgetChecker                        BudgetChecker
	pricingResolver                      usage.PricingResolver

	orchestrator *gateway.BatchOrchestrator
}

func (s *nativeBatchService) batch() *gateway.BatchOrchestrator {
	if s.orchestrator != nil {
		return s.orchestrator
	}
	s.orchestrator = gateway.NewBatchOrchestrator(gateway.BatchConfig{
		Provider:                             s.provider,
		ModelResolver:                        s.modelResolver,
		ModelAuthorizer:                      s.modelAuthorizer,
		WorkflowPolicyResolver:               s.workflowPolicyResolver,
		BatchRequestPreparer:                 s.batchRequestPreparer,
		BatchStore:                           s.batchStore,
		CleanupPreparedBatchInputFile:        s.cleanupPreparedBatchInputFile,
		CleanupStoredBatchRewrittenInputFile: s.cleanupStoredBatchRewrittenInputFile,
		UsageLogger:                          s.usageLogger,
		PricingResolver:                      s.pricingResolver,
	})
	return s.orchestrator
}

func (s *nativeBatchService) Batches(c *echo.Context) error {
	req, err := canonicalJSONRequestFromSemantics[*core.BatchRequest](c, core.DecodeBatchRequest)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	if err := enforceBudget(c, s.budgetChecker); err != nil {
		return handleError(c, err)
	}

	ctx, requestID := requestContextWithRequestID(c.Request())
	result, err := s.batch().Create(ctx, req, batchRequestMeta(c, requestID))
	if err != nil {
		return handleError(c, err)
	}
	storeWorkflow(c, result.Workflow)
	auditBatchEntry(c, result.ProviderType)

	return c.JSON(http.StatusOK, result.Batch)
}

func (s *nativeBatchService) GetBatch(c *echo.Context) error {
	ctx, _ := requestContextWithRequestID(c.Request())

	id, err := batchIDFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}

	result, err := s.batch().Get(ctx, id)
	if err != nil {
		return handleError(c, err)
	}
	auditBatchEntry(c, result.ProviderType)

	return c.JSON(http.StatusOK, result.Batch)
}

func (s *nativeBatchService) ListBatches(c *echo.Context) error {
	batchMeta, err := batchRouteInfoFromSemantics(c)
	if err != nil {
		return handleError(c, err)
	}

	params := gateway.BatchListParams{Limit: 20}
	if batchMeta != nil {
		if batchMeta.HasLimit {
			params.Limit = batchMeta.Limit
		}
		params.After = strings.TrimSpace(batchMeta.After)
	}

	resp, err := s.batch().List(c.Request().Context(), params)
	if err != nil {
		return handleError(c, err)
	}
	auditBatchEntry(c, "")

	return c.JSON(http.StatusOK, resp)
}

func (s *nativeBatchService) CancelBatch(c *echo.Context) error {
	ctx, _ := requestContextWithRequestID(c.Request())

	id, err := batchIDFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}

	result, err := s.batch().Cancel(ctx, id)
	if err != nil {
		return handleError(c, err)
	}
	auditBatchEntry(c, result.ProviderType)

	return c.JSON(http.StatusOK, result.Batch)
}

func (s *nativeBatchService) BatchResults(c *echo.Context) error {
	ctx, requestID := requestContextWithRequestID(c.Request())

	id, err := batchIDFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}

	result, err := s.batch().Results(ctx, id, requestID)
	if err != nil {
		return handleError(c, err)
	}
	auditBatchEntry(c, result.ProviderType)

	return c.JSON(http.StatusOK, result.Response)
}

func batchRequestMeta(c *echo.Context, requestID string) gateway.BatchMeta {
	return gateway.BatchMeta{
		RequestID: requestID,
		Endpoint:  core.DescribeEndpoint(c.Request().Method, c.Request().URL.Path),
		Workflow:  core.GetWorkflow(c.Request().Context()),
	}
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
