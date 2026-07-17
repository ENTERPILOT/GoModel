package server

import (
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/anthropicapi"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
)

// Anthropic Message Batches dialect (/v1/messages/batches*). Requests are
// translated at the edge into the canonical batch type and served by the same
// batch orchestrator as /v1/batches; responses render in the Anthropic
// message_batch shape. See ADR-0007 for the edge-translation pattern.

func (s *nativeBatchService) CreateMessageBatch(c *echo.Context) error {
	body, err := requestBodyBytes(c)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	createReq, err := anthropicapi.DecodeBatchCreateRequest(body)
	if err != nil {
		return handleError(c, core.NewInvalidRequestError("invalid request body: "+err.Error(), err))
	}
	req, err := anthropicapi.ToBatchRequest(createReq)
	if err != nil {
		return handleError(c, err)
	}

	ctx, requestID := requestContextWithRequestID(c.Request())
	result, err := s.batch().Create(ctx, req, batchRequestMeta(c, requestID))
	if err != nil {
		return handleError(c, err)
	}
	storeWorkflow(c, result.Workflow)
	auditBatchEntry(c, result.ProviderType)

	return c.JSON(http.StatusOK, anthropicapi.FromBatchResponse(result.Batch))
}

func (s *nativeBatchService) GetMessageBatch(c *echo.Context) error {
	ctx, _ := requestContextWithRequestID(c.Request())

	id, err := messageBatchIDFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}

	result, err := s.batch().Get(ctx, id)
	if err != nil {
		return handleError(c, err)
	}
	auditBatchEntry(c, result.ProviderType)

	return c.JSON(http.StatusOK, anthropicapi.FromBatchResponse(result.Batch))
}

func (s *nativeBatchService) ListMessageBatches(c *echo.Context) error {
	batchMeta, err := batchRouteInfoFromSemantics(c)
	if err != nil {
		return handleError(c, err)
	}

	params := gateway.BatchListParams{Limit: 20}
	if batchMeta != nil {
		if batchMeta.HasLimit {
			params.Limit = batchMeta.Limit
		}
		params.After = anthropicapi.GatewayBatchID(batchMeta.After)
	}
	// The Anthropic dialect paginates with after_id; the shared derivation
	// only reads "after", so pick up the dialect-native parameter here.
	if after := c.QueryParam("after_id"); after != "" {
		params.After = anthropicapi.GatewayBatchID(after)
	}

	resp, err := s.batch().List(c.Request().Context(), params)
	if err != nil {
		return handleError(c, err)
	}
	auditBatchEntry(c, "")

	return c.JSON(http.StatusOK, anthropicapi.FromBatchList(resp))
}

func (s *nativeBatchService) CancelMessageBatch(c *echo.Context) error {
	ctx, _ := requestContextWithRequestID(c.Request())

	id, err := messageBatchIDFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}

	result, err := s.batch().Cancel(ctx, id)
	if err != nil {
		return handleError(c, err)
	}
	auditBatchEntry(c, result.ProviderType)

	return c.JSON(http.StatusOK, anthropicapi.FromBatchResponse(result.Batch))
}

func (s *nativeBatchService) DeleteMessageBatch(c *echo.Context) error {
	ctx, _ := requestContextWithRequestID(c.Request())

	id, err := messageBatchIDFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}

	result, err := s.batch().Delete(ctx, id)
	if err != nil {
		return handleError(c, err)
	}
	auditBatchEntry(c, result.ProviderType)

	return c.JSON(http.StatusOK, anthropicapi.DeletedMessageBatch{
		ID:   anthropicapi.MessageBatchID(id),
		Type: "message_batch_deleted",
	})
}

func (s *nativeBatchService) MessageBatchResults(c *echo.Context) error {
	ctx, requestID := requestContextWithRequestID(c.Request())

	id, err := messageBatchIDFromRequest(c)
	if err != nil {
		return handleError(c, err)
	}

	result, err := s.batch().Results(ctx, id, requestID)
	if err != nil {
		return handleError(c, err)
	}
	auditBatchEntry(c, result.ProviderType)

	payload, err := anthropicapi.EncodeBatchResults(result.Response)
	if err != nil {
		return handleError(c, core.NewProviderError("", http.StatusInternalServerError, "failed to encode batch results", err))
	}
	return c.Blob(http.StatusOK, "application/x-jsonl", payload)
}

// messageBatchIDFromRequest reads the path batch ID and maps the Anthropic
// msgbatch_ form back to the gateway ID.
func messageBatchIDFromRequest(c *echo.Context) (string, error) {
	id, err := batchIDFromRequest(c)
	if err != nil {
		return "", err
	}
	return anthropicapi.GatewayBatchID(id), nil
}
