package auditlog

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/goccy/go-json"

	"github.com/andybalholm/brotli"
	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

// Note: contextKey type and constants (LogEntryKey, LogEntryStreamingKey,
// MaxBodyCapture, APIKeyHashPrefixLength) are defined in constants.go

// Middleware creates an Echo middleware for audit logging.
// It captures request metadata at the start and response metadata at the end,
// then writes the log entry asynchronously.
func Middleware(logger LoggerInterface) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			// Skip if logging is disabled
			if logger == nil || !logger.Config().Enabled {
				return next(c)
			}

			cfg := logger.Config()

			// Skip non-model paths if OnlyModelInteractions is enabled
			if cfg.OnlyModelInteractions && !core.IsModelInteractionPath(c.Request().URL.Path) {
				return next(c)
			}

			// This only short-circuits when an upstream component has already
			// populated the context with an Audit=false workflow before next(c).
			// In the common path workflow resolution happens later, so the real
			// gating occurs after next(c) once the final workflow has been resolved.
			if !auditEnabledForContext(c.Request().Context()) {
				return next(c)
			}

			start := time.Now()
			req := c.Request()

			// Read request ID (always set by the request ID middleware in http.go)
			requestID := req.Header.Get("X-Request-ID")
			userPath := core.UserPathFromContext(req.Context())
			if userPath == "" {
				userPath = "/"
			}

			// Create initial log entry
			entry := &LogEntry{
				ID:        uuid.NewString(),
				Timestamp: start,
				RequestID: requestID,
				ClientIP:  c.RealIP(),
				Method:    req.Method,
				Path:      req.URL.Path,
				UserPath:  userPath,
				Data: &LogData{
					UserAgent: req.UserAgent(),
				},
			}

			// Hash API key if present (for identification without exposing the key)
			if authHeader := req.Header.Get("Authorization"); authHeader != "" {
				entry.Data.APIKeyHash = hashAPIKey(authHeader)
			}
			if cfg.LogHeaders {
				PopulateRequestHeaders(entry, req.Header)
			}

			// Store entry in context for potential enrichment by handlers
			c.Set(string(LogEntryKey), entry)
			if publisher, ok := logger.(LiveEventEmitter); ok {
				c.Set(string(LogEntryLivePublisherKey), publisher)
				publisher.PublishLiveEvent(LiveEventAuditStarted, entry)
			}

			// Create response body capture if logging bodies
			var responseCapture *responseBodyCapture
			if cfg.LogBodies {
				responseCapture = &responseBodyCapture{
					ResponseWriter: c.Response(),
					body:           &bytes.Buffer{},
					shouldCapture: func() bool {
						return auditEnabledForContext(c.Request().Context()) && shouldCaptureResponseBody(c)
					},
				}
				c.SetResponse(responseCapture)
			}

			// Execute the handler
			err := next(c)

			applyWorkflow(entry, c.Request().Context())
			applyAuthentication(entry, c.Request().Context())

			if !auditEnabledForContext(c.Request().Context()) {
				if publisher, ok := c.Get(string(LogEntryLivePublisherKey)).(LiveEventEmitter); ok && publisher != nil {
					publisher.PublishLiveEvent(LiveEventAuditRemoved, entry)
				}
				return err
			}

			// Calculate duration
			entry.DurationNs = time.Since(start).Nanoseconds()

			// ResolveResponseStatus applies Echo v5 precedence rules for committed responses,
			// suggested status codes, and errors implementing HTTPStatusCoder.
			_, entry.StatusCode = echo.ResolveResponseStatus(c.Response(), err)

			// Request capture is deferred until after next so a later-resolved
			// Audit=false workflow can skip it entirely.
			PopulateRequestData(entry, req, cfg)

			// Log response headers if enabled
			if cfg.LogHeaders {
				PopulateResponseHeaders(entry, c.Response().Header())
			}

			// Capture response body if enabled
			if cfg.LogBodies && responseCapture != nil && shouldCaptureResponseBody(c) && responseCapture.body.Len() > 0 {
				bodyBytes := responseCapture.body.Bytes()

				// Audio responses are binary; the audio handler captures them
				// losslessly as base64 (gated by LogAudioBodies) before this
				// runs. Skip here so we neither corrupt the bytes via UTF-8
				// coercion nor clobber the handler-set body — and do not apply
				// the writer's truncation flag, which would conflict with the
				// fully-stored audio body (the handler tracks its own size cap).
				if !IsAudioContentType(c.Response().Header().Get("Content-Type")) {
					// Set truncation flag if response body exceeded limit
					if responseCapture.truncated {
						entry.Data.ResponseBodyTooBigToHandle = true
					}

					// Decompress if Content-Encoding header is present
					if contentEncoding := c.Response().Header().Get("Content-Encoding"); contentEncoding != "" {
						if decompressed, ok := decompressBody(bodyBytes, contentEncoding); ok {
							bodyBytes = decompressed
						}
					}

					// Parse JSON to any for native BSON storage in MongoDB
					var parsed any
					if jsonErr := json.Unmarshal(bodyBytes, &parsed); jsonErr == nil {
						entry.Data.ResponseBody = parsed
					} else {
						// Fallback: store as valid UTF-8 string if not valid JSON
						entry.Data.ResponseBody = toValidUTF8String(bodyBytes)
					}
				}
			}

			// Write log entry asynchronously (skip if streaming - the stream observer path handles it)
			if !IsEntryMarkedAsStreaming(c) {
				logger.Write(entry)
			}

			return err
		}
	}
}

func applyWorkflow(entry *LogEntry, ctx context.Context) {
	if entry == nil || ctx == nil {
		return
	}

	if workflow := core.GetWorkflow(ctx); workflow != nil {
		enrichEntryWithWorkflow(entry, workflow)
	}
}

func applyAuthentication(entry *LogEntry, ctx context.Context) {
	if entry == nil || ctx == nil {
		return
	}
	if authKeyID := strings.TrimSpace(core.GetAuthKeyID(ctx)); authKeyID != "" {
		entry.AuthKeyID = authKeyID
	}
	if userPath := strings.TrimSpace(core.UserPathFromContext(ctx)); userPath != "" {
		entry.UserPath = userPath
	}
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

func captureLoggedRequestBody(entry *LogEntry, bodyBytes []byte) {
	entry.Data.RequestBody = captureLoggedBody(bodyBytes)
}

func captureLoggedResponseBody(entry *LogEntry, bodyBytes []byte) {
	entry.Data.ResponseBody = captureLoggedBody(bodyBytes)
}

func captureLoggedBody(bodyBytes []byte) any {
	if len(bodyBytes) == 0 {
		return nil
	}

	// Parse JSON to any for native BSON storage in MongoDB
	var parsed any
	if jsonErr := json.Unmarshal(bodyBytes, &parsed); jsonErr == nil {
		return parsed
	}

	// Fallback: store as valid UTF-8 string if not valid JSON
	return toValidUTF8String(bodyBytes)
}

// responseBodyCapture wraps http.ResponseWriter to capture the response body.
// It implements http.Flusher and http.Hijacker by delegating to the underlying
// ResponseWriter if it supports those interfaces.
type responseBodyCapture struct {
	http.ResponseWriter
	body      *bytes.Buffer
	truncated bool
	// shouldCapture allows middleware to stop buffering once the request is
	// known to be streaming. Streaming responses are handled by the stream observer path.
	shouldCapture func() bool
}

func (r *responseBodyCapture) Write(b []byte) (int, error) {
	// Write to the capture buffer (limit to MaxBodyCapture to avoid memory issues).
	// Streaming responses bypass this path once marked or identified as SSE.
	if r.captureEnabled() && !r.truncated {
		remaining := int(MaxBodyCapture) - r.body.Len()
		if remaining > 0 {
			if len(b) <= remaining {
				r.body.Write(b)
			} else {
				r.body.Write(b[:remaining])
				r.truncated = true
			}
		} else {
			r.truncated = true
		}
	}
	// Write to the original response writer
	return r.ResponseWriter.Write(b)
}

func (r *responseBodyCapture) captureEnabled() bool {
	if r == nil || r.shouldCapture == nil {
		return true
	}
	return r.shouldCapture()
}

// Flush implements http.Flusher. It delegates to the underlying ResponseWriter
// if it implements http.Flusher, otherwise it's a no-op.
// This is required for SSE streaming to work correctly.
func (r *responseBodyCapture) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Hijack implements http.Hijacker. It delegates to the underlying ResponseWriter
// if it implements http.Hijacker, otherwise it returns an error.
// This is required for WebSocket upgrades to work correctly.
func (r *responseBodyCapture) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := r.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

func (r *responseBodyCapture) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func shouldCaptureResponseBody(c *echo.Context) bool {
	if c == nil {
		return true
	}
	if IsEntryMarkedAsStreaming(c) {
		return false
	}
	return !isEventStreamContentType(c.Response().Header().Get("Content-Type"))
}

func isEventStreamContentType(contentType string) bool {
	if contentType == "" {
		return false
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	return mediaType == "text/event-stream"
}

// extractHeaders extracts headers from a map[string][]string (http.Header or echo headers),
// taking the first value for each key and redacting sensitive headers.
func extractHeaders(headers map[string][]string) map[string]string {
	result := make(map[string]string, len(headers))
	for key, values := range headers {
		if len(values) > 0 {
			result[key] = values[0]
		}
	}
	return RedactHeaders(result)
}

// hashAPIKey creates a short hash of the API key for identification.
// Returns first APIKeyHashPrefixLength hex characters of SHA256 hash.
func hashAPIKey(authHeader string) string {
	// Extract token from "Bearer <token>"
	token := strings.TrimPrefix(authHeader, "Bearer ")
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}

	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])[:APIKeyHashPrefixLength]
}

// EnrichEntry retrieves the log entry from context for enrichment by handlers.
// This allows handlers to add model and provider information.
func EnrichEntry(c *echo.Context, model, provider string) {
	entryVal := c.Get(string(LogEntryKey))
	if entryVal == nil {
		return
	}

	entry, ok := entryVal.(*LogEntry)
	if !ok || entry == nil {
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

// EnrichEntryWithRequestedModel attaches early requested-model metadata to the
// live audit entry before the final workflow policy has been resolved.
func EnrichEntryWithRequestedModel(c *echo.Context, requestedModel string) {
	entryVal := c.Get(string(LogEntryKey))
	if entryVal == nil {
		return
	}

	entry, ok := entryVal.(*LogEntry)
	if !ok || entry == nil {
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

	entryVal := c.Get(string(LogEntryKey))
	if entryVal == nil {
		return
	}

	entry, ok := entryVal.(*LogEntry)
	if !ok || entry == nil {
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
	entryVal := c.Get(string(LogEntryKey))
	if entryVal == nil {
		return
	}

	entry, ok := entryVal.(*LogEntry)
	if !ok || entry == nil {
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
	entryVal := c.Get(string(LogEntryKey))
	if entryVal == nil {
		return
	}

	entry, ok := entryVal.(*LogEntry)
	if !ok || entry == nil {
		return
	}

	enrichEntryWithFailover(entry, targetModel)
	publishLiveAuditUpdate(c, entry)
}

// EnrichEntryWithAttempts attaches provider attempt summaries to the live
// audit entry. The attempt list belongs to the logical request, not to separate
// top-level audit rows.
func EnrichEntryWithAttempts(c *echo.Context, attempts []AttemptSnapshot) {
	entryVal := c.Get(string(LogEntryKey))
	if entryVal == nil {
		return
	}

	entry, ok := entryVal.(*LogEntry)
	if !ok || entry == nil {
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

// EnrichEntryWithCacheType attaches cache-hit metadata to the live audit entry.
// The value is intentionally sourced directly from the cache middleware, not
// inferred from response headers after the fact.
func EnrichEntryWithCacheType(c *echo.Context, cacheType string) {
	entryVal := c.Get(string(LogEntryKey))
	if entryVal == nil {
		return
	}

	entry, ok := entryVal.(*LogEntry)
	if !ok || entry == nil {
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
	entryVal := c.Get(string(LogEntryKey))
	if entryVal == nil {
		return
	}
	entry, ok := entryVal.(*LogEntry)
	if !ok || entry == nil {
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
	entryVal := c.Get(string(LogEntryKey))
	if entryVal == nil {
		return
	}

	entry, ok := entryVal.(*LogEntry)
	if !ok || entry == nil {
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
	entryVal := c.Get(string(LogEntryKey))
	if entryVal == nil {
		return
	}

	entry, ok := entryVal.(*LogEntry)
	if !ok || entry == nil {
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

func auditEnabledForContext(ctx context.Context) bool {
	workflow := core.GetWorkflow(ctx)
	return workflow == nil || workflow.AuditEnabled()
}

// EnrichEntryWithError adds error information to the log entry.
func EnrichEntryWithError(c *echo.Context, errorType, errorMessage string, errorCode ...string) {
	entryVal := c.Get(string(LogEntryKey))
	if entryVal == nil {
		return
	}

	entry, ok := entryVal.(*LogEntry)
	if !ok || entry == nil {
		return
	}

	entry.ErrorType = errorType
	if entry.Data != nil {
		entry.Data.ErrorMessage = errorMessage
		if len(errorCode) > 0 {
			if code := strings.TrimSpace(errorCode[0]); code != "" {
				entry.Data.ErrorCode = code
			}
		}
	}
	publishLiveAuditUpdate(c, entry)
}

// EnrichEntryWithStream marks the log entry as a streaming request.
func EnrichEntryWithStream(c *echo.Context, stream bool) {
	entryVal := c.Get(string(LogEntryKey))
	if entryVal == nil {
		return
	}

	entry, ok := entryVal.(*LogEntry)
	if !ok || entry == nil {
		return
	}

	entry.Stream = stream
	publishLiveAuditUpdate(c, entry)
}

// toValidUTF8String converts bytes to a valid UTF-8 string.
// If the input is already valid UTF-8, it returns it as-is.
// Otherwise, it replaces invalid bytes with the Unicode replacement character.
// This prevents "Invalid UTF-8 string in BSON document" errors in MongoDB.
func toValidUTF8String(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	// Replace invalid UTF-8 sequences with replacement character
	return strings.ToValidUTF8(string(b), "\uFFFD")
}

// decompressBody attempts to decompress the response body based on Content-Encoding.
// Returns original body unchanged if no decompression needed or if decompression fails.
// Supports gzip, deflate, and brotli (br) encodings.
func decompressBody(body []byte, contentEncoding string) ([]byte, bool) {
	if len(body) == 0 || contentEncoding == "" {
		return body, false
	}

	// Parse encoding (handle "gzip, deflate" - take first)
	encoding := strings.TrimSpace(strings.Split(contentEncoding, ",")[0])
	encoding = strings.ToLower(encoding)

	if encoding == "identity" || encoding == "" {
		return body, false
	}

	const maxDecompressedSize = 2 * 1024 * 1024 // 2MB limit

	var reader io.ReadCloser
	var err error

	switch encoding {
	case "gzip":
		reader, err = gzip.NewReader(bytes.NewReader(body))
	case "deflate":
		reader = flate.NewReader(bytes.NewReader(body))
	case "br":
		reader = io.NopCloser(brotli.NewReader(bytes.NewReader(body)))
	default:
		return body, false
	}

	if err != nil {
		return body, false
	}
	defer reader.Close()

	// Read with size limit (compression bomb protection)
	decompressed, err := io.ReadAll(io.LimitReader(reader, maxDecompressedSize))
	if err != nil {
		return body, false
	}

	return decompressed, true
}
