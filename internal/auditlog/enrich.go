package auditlog

import (
	"context"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
)

// This file is the enrichment API handlers use to attach data to the live
// audit entry stored on the request context. Every context-based Enrich*
// helper publishes an audit.updated live event so connected dashboards see
// the entry evolve in realtime; the EnrichLogEntry* variants mutate an entry
// directly for executors that run outside Echo middleware state.

// entryFromContext returns the live audit entry stored on the request context,
// or nil when audit logging is inactive for the request.
func entryFromContext(c *echo.Context) *LogEntry {
	if c == nil {
		return nil
	}
	entry, ok := c.Get(string(LogEntryKey)).(*LogEntry)
	if !ok {
		return nil
	}
	return entry
}

func publishLiveAuditUpdate(c *echo.Context, entry *LogEntry) {
	if c == nil || entry == nil {
		return
	}
	publisher, ok := c.Get(string(LogEntryLivePublisherKey)).(LiveEventEmitter)
	if !ok || publisher == nil {
		return
	}
	publisher.PublishLiveEvent(LiveEventAuditUpdated, entry)
}

// EnrichEntry retrieves the log entry from context for enrichment by handlers.
// This allows handlers to add model and provider information.
func EnrichEntry(c *echo.Context, model, provider string) {
	entry := entryFromContext(c)
	if entry == nil {
		return
	}
	entry.RequestedModel = model
	entry.Provider = provider
	publishLiveAuditUpdate(c, entry)
}

// EnrichEntryWithRequestBody sets the audit request body from a handler that
// captures its own request payload (e.g. audio endpoints, which are not
// ingress-managed and so have no request snapshot to read from). A nil body or
// missing entry is a no-op; an already-populated request body is preserved.
func EnrichEntryWithRequestBody(c *echo.Context, body any) {
	if body == nil {
		return
	}
	entry := entryFromContext(c)
	if entry == nil {
		return
	}
	data := ensureLogData(entry)
	if data.RequestBody != nil {
		return
	}
	data.RequestBody = body
	publishLiveAuditUpdate(c, entry)
}

// EnrichEntryWithRawRequestBody captures a raw request payload from a handler
// whose endpoint is not ingress-managed (no request snapshot to read from),
// applying the same size cap and JSON/UTF-8 coercion as snapshot-backed
// capture. Callers gate on the audit LogBodies setting; an already-populated
// request body is preserved.
func EnrichEntryWithRawRequestBody(c *echo.Context, body []byte) {
	if len(body) == 0 {
		return
	}
	entry := entryFromContext(c)
	if entry == nil {
		return
	}
	data := ensureLogData(entry)
	if data.RequestBody != nil || data.RequestBodyTooBigToHandle {
		return
	}
	if len(body) > MaxBodyCapture {
		data.RequestBodyTooBigToHandle = true
	} else {
		data.RequestBody = CaptureLoggedBody(body)
	}
	publishLiveAuditUpdate(c, entry)
}

// EnrichEntryWithCapturedResponseBody sets the response body and truncation
// flag from a handler-owned response capture — for responses the middleware's
// own writer deliberately skips, like MCP's SSE-framed POST replies.
func EnrichEntryWithCapturedResponseBody(c *echo.Context, body any, truncated bool) {
	if body == nil && !truncated {
		return
	}
	entry := entryFromContext(c)
	if entry == nil {
		return
	}
	data := ensureLogData(entry)
	if body != nil {
		data.ResponseBody = body
	}
	if truncated {
		data.ResponseBodyTooBigToHandle = true
	}
	publishLiveAuditUpdate(c, entry)
}

// EnrichEntryWithResponseBody sets the audit response body from a handler that
// captures its own response payload (e.g. audio output served as raw bytes).
// A nil body or missing entry is a no-op.
func EnrichEntryWithResponseBody(c *echo.Context, body any) {
	if body == nil {
		return
	}
	entry := entryFromContext(c)
	if entry == nil {
		return
	}
	ensureLogData(entry).ResponseBody = body
	publishLiveAuditUpdate(c, entry)
}

// EnrichEntryWithRequestRevision appends one ingress request-rewrite revision
// to the live audit entry, assigning the next sequence number. A missing
// entry is a no-op.
func EnrichEntryWithRequestRevision(c *echo.Context, revision RequestRevisionSnapshot) {
	entry := entryFromContext(c)
	if entry == nil {
		return
	}
	data := ensureLogData(entry)
	revision.Seq = len(data.RequestRevisions) + 1
	data.RequestRevisions = append(data.RequestRevisions, revision)
	publishLiveAuditUpdate(c, entry)
}

// EnrichEntryWithRequestedModel attaches early requested-model metadata to the
// live audit entry before the final workflow policy has been resolved.
func EnrichEntryWithRequestedModel(c *echo.Context, requestedModel string) {
	entry := entryFromContext(c)
	if entry == nil {
		return
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return
	}
	entry.RequestedModel = requestedModel
	publishLiveAuditUpdate(c, entry)
}

// EnrichEntryWithWorkflow attaches workflow metadata to the live
// audit entry. This is preferred over requested-model-only enrichment once workflow
// resolution has completed for the request.
func EnrichEntryWithWorkflow(c *echo.Context, workflow *core.Workflow) {
	syncRequestWorkflow(c, workflow)

	entry := entryFromContext(c)
	if entry == nil {
		return
	}
	enrichEntryWithWorkflow(entry, workflow)
	populateLiveRequestDataAfterWorkflow(c, entry, workflow)
	publishLiveAuditUpdate(c, entry)
}

func syncRequestWorkflow(c *echo.Context, workflow *core.Workflow) {
	if c == nil || workflow == nil {
		return
	}
	req := c.Request()
	if req == nil {
		return
	}
	ctx := req.Context()
	if core.GetWorkflow(ctx) == workflow {
		return
	}
	c.SetRequest(req.WithContext(core.WithWorkflow(ctx, workflow)))
}

// EnrichLogEntryWithWorkflow attaches workflow metadata directly to
// an existing log entry. Internal translated executors can use this without
// depending on Echo middleware state.
func EnrichLogEntryWithWorkflow(entry *LogEntry, workflow *core.Workflow) {
	enrichEntryWithWorkflow(entry, workflow)
}

// EnrichEntryWithResolvedRoute attaches the final executed route to the live
// audit entry after execution resolved to a concrete provider/model.
func EnrichEntryWithResolvedRoute(c *echo.Context, resolvedModel, providerType, providerName string) {
	entry := entryFromContext(c)
	if entry == nil {
		return
	}
	enrichEntryWithResolvedRoute(entry, resolvedModel, providerType, providerName)
	publishLiveAuditUpdate(c, entry)
}

// EnrichLogEntryWithResolvedRoute attaches the final executed route directly to
// an existing audit log entry.
func EnrichLogEntryWithResolvedRoute(entry *LogEntry, resolvedModel, providerType, providerName string) {
	enrichEntryWithResolvedRoute(entry, resolvedModel, providerType, providerName)
}

// EnrichEntryWithFailover records the configured failover selector used for the
// live request when translated execution redirected away from the primary
// selector.
func EnrichEntryWithFailover(c *echo.Context, targetModel string) {
	entry := entryFromContext(c)
	if entry == nil {
		return
	}
	enrichEntryWithFailover(entry, targetModel)
	publishLiveAuditUpdate(c, entry)
}

// EnrichEntryWithAttempts attaches provider attempt summaries to the live
// audit entry. The attempt list belongs to the logical request, not to separate
// top-level audit rows.
func EnrichEntryWithAttempts(c *echo.Context, attempts []AttemptSnapshot) {
	entry := entryFromContext(c)
	if entry == nil {
		return
	}
	enrichEntryWithAttempts(entry, gateAttemptCaptureFromContext(c, attempts))
	publishLiveAuditUpdate(c, entry)
}

// gateAttemptCaptureFromContext strips per-attempt response bodies/headers that
// the request's audit config did not opt into. When the config cannot be
// resolved, opt-in-only captures are dropped rather than persisted.
func gateAttemptCaptureFromContext(c *echo.Context, attempts []AttemptSnapshot) []AttemptSnapshot {
	if c == nil || len(attempts) == 0 {
		return attempts
	}
	if logger, ok := c.Get(string(LogEntryLivePublisherKey)).(LoggerInterface); ok && logger != nil {
		return GateAttemptCapture(attempts, logger.Config())
	}
	return GateAttemptCapture(attempts, Config{})
}

// EnrichLogEntryWithFailover attaches failover redirect metadata directly to an
// existing audit log entry.
func EnrichLogEntryWithFailover(entry *LogEntry, targetModel string) {
	enrichEntryWithFailover(entry, targetModel)
}

// EnrichLogEntryWithAttempts attaches provider attempt summaries directly to an
// existing audit log entry.
func EnrichLogEntryWithAttempts(entry *LogEntry, attempts []AttemptSnapshot) {
	enrichEntryWithAttempts(entry, attempts)
}

// EnrichEntryWithCacheType attaches cache-hit metadata to the live audit entry.
// The value is intentionally sourced directly from the cache middleware, not
// inferred from response headers after the fact.
func EnrichEntryWithCacheType(c *echo.Context, cacheType string) {
	entry := entryFromContext(c)
	if entry == nil {
		return
	}
	cacheType = normalizeCacheType(cacheType)
	if cacheType == "" {
		return
	}
	entry.CacheType = cacheType
	publishLiveAuditUpdate(c, entry)
}

// EnrichEntryWithAuthMethod records which authentication mechanism was used for the request.
func EnrichEntryWithAuthMethod(c *echo.Context, method string) {
	entry := entryFromContext(c)
	if entry == nil {
		return
	}
	method = strings.ToLower(strings.TrimSpace(method))
	switch method {
	case AuthMethodAPIKey, AuthMethodMasterKey, AuthMethodNoKey, "unknown":
	default:
		return
	}
	entry.AuthMethod = method
	publishLiveAuditUpdate(c, entry)
}

// EnrichEntryWithAuthKeyID attaches the authenticated managed auth key id to the live audit entry.
func EnrichEntryWithAuthKeyID(c *echo.Context, authKeyID string) {
	entry := entryFromContext(c)
	if entry == nil {
		return
	}
	authKeyID = strings.TrimSpace(authKeyID)
	if authKeyID == "" {
		return
	}
	entry.AuthKeyID = authKeyID
	publishLiveAuditUpdate(c, entry)
}

// EnrichEntryWithUserPath attaches the effective user path to the live audit entry.
func EnrichEntryWithUserPath(c *echo.Context, userPath string) {
	entry := entryFromContext(c)
	if entry == nil {
		return
	}
	userPath = strings.TrimSpace(userPath)
	if userPath == "" {
		return
	}
	entry.UserPath = userPath
	publishLiveAuditUpdate(c, entry)
}

// EnrichLogEntryWithRequestContext attaches auth and effective user-path
// metadata from context directly to an existing log entry.
func EnrichLogEntryWithRequestContext(entry *LogEntry, ctx context.Context) {
	applyAuthentication(entry, ctx)
}

// EnrichEntryWithError adds error information to the log entry.
func EnrichEntryWithError(c *echo.Context, errorType, errorMessage string, errorCode ...string) {
	entry := entryFromContext(c)
	if entry == nil {
		return
	}
	entry.ErrorType = errorType
	data := ensureLogData(entry)
	data.ErrorMessage = errorMessage
	if len(errorCode) > 0 {
		if code := strings.TrimSpace(errorCode[0]); code != "" {
			data.ErrorCode = code
		}
	}
	publishLiveAuditUpdate(c, entry)
}

// EnrichEntryWithStream marks the log entry as a streaming request.
func EnrichEntryWithStream(c *echo.Context, stream bool) {
	entry := entryFromContext(c)
	if entry == nil {
		return
	}
	entry.Stream = stream
	publishLiveAuditUpdate(c, entry)
}

func populateLiveRequestDataAfterWorkflow(c *echo.Context, entry *LogEntry, workflow *core.Workflow) {
	if c == nil || entry == nil || workflow == nil || !workflow.AuditEnabled() {
		return
	}
	logger, ok := c.Get(string(LogEntryLivePublisherKey)).(LoggerInterface)
	if !ok || logger == nil {
		return
	}
	cfg := logger.Config()
	if !cfg.Enabled || !cfg.LogBodies {
		return
	}
	PopulateRequestData(entry, c.Request(), cfg)
}

func enrichEntryWithWorkflow(entry *LogEntry, workflow *core.Workflow) {
	if entry == nil || workflow == nil {
		return
	}

	if requestID := strings.TrimSpace(workflow.RequestID); requestID != "" {
		entry.RequestID = requestID
	}
	if requestedModel := workflow.RequestedQualifiedModel(); requestedModel != "" {
		entry.RequestedModel = requestedModel
	}

	// When a runtime failover already recorded the actual executed route
	// (resolved_model/provider, via EnrichEntryWithResolvedRoute), the workflow
	// here only carries the planned primary resolution — so it must not clobber
	// the real route the request ended up taking. A failover snapshot alone is
	// not proof a given route field was populated, so gate each field on its own
	// concrete value: if the executed route left a field empty, fall back to the
	// workflow's planned value instead of leaving it blank.
	failoverRecorded := entry.Data != nil && entry.Data.Failover != nil
	executedResolvedModel := failoverRecorded && strings.TrimSpace(entry.ResolvedModel) != ""
	executedProvider := failoverRecorded && strings.TrimSpace(entry.Provider) != ""
	executedProviderName := failoverRecorded && strings.TrimSpace(entry.ProviderName) != ""

	if resolvedModel := resolvedModelForAuditLog(workflow); resolvedModel != "" && !executedResolvedModel {
		entry.ResolvedModel = resolvedModel
	}
	if workflow.Mode == core.ExecutionModePassthrough && workflow.Passthrough != nil {
		if model := strings.TrimSpace(workflow.Passthrough.Model); model != "" {
			entry.RequestedModel = model
		}
	}
	if !executedProvider {
		if providerType := strings.TrimSpace(workflow.ProviderType); providerType != "" {
			entry.Provider = providerType
		} else if workflow.Resolution != nil && strings.TrimSpace(workflow.Resolution.ProviderType) != "" {
			entry.Provider = strings.TrimSpace(workflow.Resolution.ProviderType)
		}
	}
	if workflow.Resolution != nil {
		if providerName := strings.TrimSpace(workflow.Resolution.ProviderName); providerName != "" && !executedProviderName {
			entry.ProviderName = providerName
		}
		entry.AliasUsed = workflow.Resolution.AliasApplied
	}
	if versionID := strings.TrimSpace(workflow.WorkflowVersionID()); versionID != "" {
		entry.WorkflowVersionID = versionID
	}
	if workflow.Policy != nil {
		ensureLogData(entry).WorkflowFeatures = &WorkflowFeaturesSnapshot{
			Cache:      workflow.Policy.Features.Cache,
			Audit:      workflow.Policy.Features.Audit,
			Usage:      workflow.Policy.Features.Usage,
			Budget:     workflow.Policy.Features.Budget,
			Guardrails: workflow.Policy.Features.Guardrails,
			Failover:   workflow.Policy.Features.Failover,
		}
	}
}

func resolvedModelForAuditLog(workflow *core.Workflow) string {
	if workflow == nil || workflow.Resolution == nil {
		return ""
	}
	model := strings.TrimSpace(workflow.Resolution.ResolvedSelector.Model)
	if model == "" {
		return ""
	}
	if providerName := strings.TrimSpace(workflow.Resolution.ProviderName); providerName != "" {
		return providerName + "/" + model
	}
	if provider := strings.TrimSpace(workflow.Resolution.ResolvedSelector.Provider); provider != "" {
		return provider + "/" + model
	}
	return model
}

func enrichEntryWithResolvedRoute(entry *LogEntry, resolvedModel, providerType, providerName string) {
	if entry == nil {
		return
	}
	if resolvedModel = strings.TrimSpace(resolvedModel); resolvedModel != "" {
		entry.ResolvedModel = resolvedModel
	}
	if providerType = strings.TrimSpace(providerType); providerType != "" {
		entry.Provider = providerType
	}
	if providerName = strings.TrimSpace(providerName); providerName != "" {
		entry.ProviderName = providerName
	}
}

func enrichEntryWithFailover(entry *LogEntry, targetModel string) {
	if entry == nil {
		return
	}

	targetModel = strings.TrimSpace(targetModel)
	if targetModel == "" {
		return
	}

	ensureLogData(entry).Failover = &FailoverSnapshot{
		TargetModel: targetModel,
	}
}

func enrichEntryWithAttempts(entry *LogEntry, attempts []AttemptSnapshot) {
	if entry == nil {
		return
	}
	normalized := normalizeAttemptSnapshots(attempts)
	if len(normalized) == 0 {
		return
	}
	ensureLogData(entry).Attempts = normalized
}
