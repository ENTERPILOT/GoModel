package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/gateway"
)

// newInternalAuditEntry starts the audit entry of a gateway-internal call to
// path, or returns nil when audit logging is off.
func newInternalAuditEntry(
	logger auditlog.LoggerInterface,
	ctx context.Context,
	requestID string,
	requested core.RequestedModelSelector,
	path string,
) *auditlog.LogEntry {
	if logger == nil || !logger.Config().Enabled {
		return nil
	}

	userPath := core.UserPathFromContext(ctx)
	if userPath == "" {
		userPath = "/"
	}

	entry := &auditlog.LogEntry{
		ID:        uuid.NewString(),
		Timestamp: time.Now(),
		RequestID: requestID,
		Method:    http.MethodPost,
		Path:      path,
		UserPath:  userPath,
		SessionID: core.SessionIDFromContext(ctx),
		Data:      &auditlog.LogData{Labels: core.RequestLabelsFromContext(ctx)},
	}
	if requestedModel := requested.RequestedQualifiedModel(); requestedModel != "" {
		entry.RequestedModel = requestedModel
	}
	return entry
}

// internalExchange is the outcome of a gateway-internal call an audit entry
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

// finishInternalAuditEntry completes and writes the audit entry of a
// gateway-internal call.
func finishInternalAuditEntry(logger auditlog.LoggerInterface, ctx context.Context, entry *auditlog.LogEntry, x internalExchange) {
	if entry == nil || logger == nil || !logger.Config().Enabled {
		return
	}

	entry.DurationNs = time.Since(x.start).Nanoseconds()
	auditlog.EnrichLogEntryWithWorkflow(entry, x.workflow)
	auditlog.EnrichLogEntryWithFailover(entry, x.meta.FailoverModel)
	auditlog.EnrichLogEntryWithAttempts(entry, auditlog.GateAttemptCapture(auditAttemptsFromGateway(ctx), logger.Config()))
	auditlog.EnrichLogEntryWithResolvedRoute(entry, qualifyExecutedModel(x.workflow, x.model, x.meta.ProviderName), x.meta.ProviderType, x.meta.ProviderName)
	auditlog.EnrichLogEntryWithRequestContext(entry, ctx)
	if x.workflow != nil && !x.workflow.AuditEnabled() {
		return
	}

	cfg := logger.Config()
	auditlog.CaptureInternalJSONExchange(entry, ctx, http.MethodPost, x.path, x.request, x.response, x.err, cfg)
	if x.cacheType != "" {
		entry.CacheType = x.cacheType
	}

	if x.err != nil {
		var gatewayErr *core.GatewayError
		if errors.As(x.err, &gatewayErr) && gatewayErr != nil {
			entry.ErrorType = string(gatewayErr.Type)
			entry.StatusCode = gatewayErr.HTTPStatusCode()
			if entry.Data != nil {
				entry.Data.ErrorMessage = gatewayErr.Message
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
