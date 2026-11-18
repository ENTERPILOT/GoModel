package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
	"github.com/enterpilot/gomodel/internal/requestlog"
)

// newInternalRequestLogEntry starts the request log entry of a gateway-internal call to
// path, or returns nil when request logging is off.
func newInternalRequestLogEntry(
	logger requestlog.LoggerInterface,
	ctx context.Context,
	requestID string,
	requested core.RequestedModelSelector,
	path string,
) *requestlog.LogEntry {
	if logger == nil || !logger.Config().Enabled {
		return nil
	}

	userPath := core.UserPathFromContext(ctx)
	if userPath == "" {
		userPath = "/"
	}

	entry := &requestlog.LogEntry{
		ID:        uuid.NewString(),
		Timestamp: time.Now(),
		RequestID: requestID,
		Method:    http.MethodPost,
		Path:      path,
		UserPath:  userPath,
		SessionID: core.SessionIDFromContext(ctx),
		Data:      &requestlog.LogData{Labels: core.RequestLabelsFromContext(ctx)},
	}
	if requestedModel := requested.RequestedQualifiedModel(); requestedModel != "" {
		entry.RequestedModel = requestedModel
	}
	return entry
}

// internalExchange is the outcome of a gateway-internal call a request log entry
// records.
type internalExchange struct {
	path     string
	start    time.Time
	workflow *core.Workflow
	request  any
	response any
	// model is the model that answered, as the provider named it.
	model     string
	err       error
	cacheType string
	meta      gateway.ExecutionMeta
}

// finishInternalRequestLogEntry completes and writes the request log entry of a
// gateway-internal call.
func finishInternalRequestLogEntry(logger requestlog.LoggerInterface, ctx context.Context, entry *requestlog.LogEntry, x internalExchange) {
	if entry == nil || logger == nil || !logger.Config().Enabled {
		return
	}

	entry.DurationNs = time.Since(x.start).Nanoseconds()
	requestlog.EnrichLogEntryWithWorkflow(entry, x.workflow)
	requestlog.EnrichLogEntryWithFailover(entry, x.meta.FailoverModel)
	requestlog.EnrichLogEntryWithAttempts(entry, requestlog.GateAttemptCapture(requestLogAttemptsFromGateway(ctx), logger.Config().ForPath(x.path)))
	requestlog.EnrichLogEntryWithResolvedRoute(entry, qualifyExecutedModel(x.workflow, x.model, x.meta.ProviderName), x.meta.ProviderType, x.meta.ProviderName)
	requestlog.EnrichLogEntryWithRequestContext(entry, ctx)
	if x.workflow != nil && !x.workflow.RequestLogEnabled() {
		return
	}

	cfg := logger.Config()
	requestlog.CaptureInternalJSONExchange(entry, ctx, http.MethodPost, x.path, x.request, x.response, x.err, cfg)
	if x.cacheType != "" {
		entry.CacheType = x.cacheType
	}

	if x.err != nil {
		var gatewayErr *core.GatewayError
		if errors.As(x.err, &gatewayErr) && gatewayErr != nil {
			entry.ErrorType = string(gatewayErr.Type)
			entry.StatusCode = gatewayErr.HTTPStatusCode()
			if entry.Data != nil {
				entry.Data.ErrorMessage = requestlog.GatewayErrorMessage(gatewayErr, cfg.ForPath(x.path))
				if gatewayErr.Code != nil {
					entry.Data.ErrorCode = *gatewayErr.Code
				}
			}
		} else {
			entry.ErrorType = string(core.ErrorTypeProvider)
			entry.StatusCode = http.StatusInternalServerError
			if entry.Data != nil {
				entry.Data.ErrorMessage = x.err.Error()
			}
		}
	} else {
		entry.StatusCode = http.StatusOK
	}

	logger.Write(entry)
}
