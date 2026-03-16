package auditlog

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

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
			if cfg.OnlyModelInteractions && !IsModelInteractionPath(c.Request().URL.Path) {
				return next(c)
			}

			start := time.Now()
			req := c.Request()

			// Read request ID (always set by the request ID middleware in http.go)
			requestID := req.Header.Get("X-Request-ID")

			// Create initial log entry
			entry := &LogEntry{
				ID:        uuid.NewString(),
				Timestamp: start,
				RequestID: requestID,
				ClientIP:  c.RealIP(),
				Method:    req.Method,
				Path:      req.URL.Path,
				Data: &LogData{
					UserAgent: req.UserAgent(),
				},
			}

			// Hash API key if present (for identification without exposing the key)
			if authHeader := req.Header.Get("Authorization"); authHeader != "" {
				entry.Data.APIKeyHash = hashAPIKey(authHeader)
			}

			// Log request headers if enabled
			if cfg.LogHeaders {
				entry.Data.RequestHeaders = extractHeaders(req.Header)
			}

			// Capture request body if enabled
			if cfg.LogBodies && req.Body != nil {
				if snapshot := core.GetRequestSnapshot(req.Context()); snapshot != nil {
					body := snapshot.CapturedBody()
					switch {
					case snapshot.BodyNotCaptured:
						entry.Data.RequestBodyTooBigToHandle = true
					case body != nil:
						captureLoggedRequestBody(entry, body)
					default:
						captureRequestBodyForLogging(entry, req)
					}
				} else {
					captureRequestBodyForLogging(entry, req)
				}
			}

			// Store entry in context for potential enrichment by handlers
			c.Set(string(LogEntryKey), entry)

			// Create response body capture if logging bodies
			var responseCapture *responseBodyCapture
			if cfg.LogBodies {
				responseCapture = &responseBodyCapture{
					ResponseWriter: c.Response(),
					body:           &bytes.Buffer{},
				}
				c.SetResponse(responseCapture)
			}

			// Execute the handler
			err := next(c)

			applyExecutionPlan(entry, c.Request().Context())

			// Calculate duration
			entry.DurationNs = time.Since(start).Nanoseconds()

			// ResolveResponseStatus applies Echo v5 precedence rules for committed responses,
			// suggested status codes, and errors implementing HTTPStatusCoder.
			_, entry.StatusCode = echo.ResolveResponseStatus(c.Response(), err)

			// Log response headers if enabled
			if cfg.LogHeaders {
				entry.Data.ResponseHeaders = extractHeaders(c.Response().Header())
			}

			// Capture response body if enabled
			if cfg.LogBodies && responseCapture != nil && responseCapture.body.Len() > 0 {
				// Set truncation flag if response body exceeded limit
				if responseCapture.truncated {
					entry.Data.ResponseBodyTooBigToHandle = true
				}

				bodyBytes := responseCapture.body.Bytes()

				// Decompress if Content-Encoding header is present
				if contentEncoding := c.Response().Header().Get("Content-Encoding"); contentEncoding != "" {
					if decompressed, ok := decompressBody(bodyBytes, contentEncoding); ok {
						bodyBytes = decompressed
					}
				}

				// Parse JSON to interface{} for native BSON storage in MongoDB
				var parsed interface{}
				if jsonErr := json.Unmarshal(bodyBytes, &parsed); jsonErr == nil {
					entry.Data.ResponseBody = parsed
				} else {
					// Fallback: store as valid UTF-8 string if not valid JSON
					entry.Data.ResponseBody = toValidUTF8String(bodyBytes)
				}
			}

			// Write log entry asynchronously (skip if streaming - StreamLogWrapper handles it)
			if !IsEntryMarkedAsStreaming(c) {
				logger.Write(entry)
			}

			return err
		}
	}
}

func applyExecutionPlan(entry *LogEntry, ctx context.Context) {
	if entry == nil || ctx == nil {
		return
	}

	if plan := core.GetExecutionPlan(ctx); plan != nil {
		enrichEntryWithExecutionPlan(entry, plan)
	}
}

func enrichEntryWithExecutionPlan(entry *LogEntry, plan *core.ExecutionPlan) {
	if entry == nil || plan == nil {
		return
	}

	if requestedModel := plan.RequestedQualifiedModel(); requestedModel != "" {
		entry.Model = requestedModel
	}
	if resolvedModel := plan.ResolvedQualifiedModel(); resolvedModel != "" {
		entry.ResolvedModel = resolvedModel
	}
	if plan.Mode == core.ExecutionModePassthrough && plan.Passthrough != nil {
		if model := strings.TrimSpace(plan.Passthrough.Model); model != "" {
			entry.Model = model
		}
	}
	if providerType := strings.TrimSpace(plan.ProviderType); providerType != "" {
		entry.Provider = providerType
	} else if plan.Resolution != nil && strings.TrimSpace(plan.Resolution.ProviderType) != "" {
		entry.Provider = strings.TrimSpace(plan.Resolution.ProviderType)
	}
	if plan.Resolution != nil {
		entry.AliasUsed = plan.Resolution.AliasApplied
	}
}

func captureRequestBodyForLogging(entry *LogEntry, req *http.Request) {
	if req.ContentLength > MaxBodyCapture {
		entry.Data.RequestBodyTooBigToHandle = true
		return
	}

	// Read up to MaxBodyCapture+1 to detect overflow safely.
	// Uses io.LimitReader to enforce the cap regardless of
	// Content-Length (handles chunked/unknown-length requests).
	limitedReader := io.LimitReader(req.Body, MaxBodyCapture+1)
	bodyBytes, err := io.ReadAll(limitedReader)
	if err != nil {
		return
	}
	if int64(len(bodyBytes)) > MaxBodyCapture {
		entry.Data.RequestBodyTooBigToHandle = true
		// Reconstruct full body for downstream: read bytes + unread remainder
		origBody := req.Body
		req.Body = &combinedReadCloser{
			Reader: io.MultiReader(bytes.NewReader(bodyBytes), origBody),
			rc:     origBody,
		}
		return
	}

	captureLoggedRequestBody(entry, bodyBytes)
	req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
}

func captureLoggedRequestBody(entry *LogEntry, bodyBytes []byte) {
	if len(bodyBytes) == 0 {
		return
	}

	// Parse JSON to interface{} for native BSON storage in MongoDB
	var parsed interface{}
	if jsonErr := json.Unmarshal(bodyBytes, &parsed); jsonErr == nil {
		entry.Data.RequestBody = parsed
		return
	}

	// Fallback: store as valid UTF-8 string if not valid JSON
	entry.Data.RequestBody = toValidUTF8String(bodyBytes)
}

// combinedReadCloser delegates Read to an io.Reader and Close to an io.ReadCloser.
// Used to reconstruct a request body that preserves the original closer.
type combinedReadCloser struct {
	io.Reader
	rc io.ReadCloser
}

func (c *combinedReadCloser) Close() error {
	return c.rc.Close()
}

// responseBodyCapture wraps http.ResponseWriter to capture the response body.
// It implements http.Flusher and http.Hijacker by delegating to the underlying
// ResponseWriter if it supports those interfaces.
type responseBodyCapture struct {
	http.ResponseWriter
	body      *bytes.Buffer
	truncated bool
}

func (r *responseBodyCapture) Write(b []byte) (int, error) {
	// Write to the capture buffer (limit to MaxBodyCapture to avoid memory issues)
	if !r.truncated {
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

	entry.Model = model
	entry.Provider = provider
}

// EnrichEntryWithExecutionPlan attaches execution-plan metadata to the live
// audit entry. This is preferred over resolution-only enrichment once planning
// has completed for the request.
func EnrichEntryWithExecutionPlan(c *echo.Context, plan *core.ExecutionPlan) {
	entryVal := c.Get(string(LogEntryKey))
	if entryVal == nil {
		return
	}

	entry, ok := entryVal.(*LogEntry)
	if !ok || entry == nil {
		return
	}

	enrichEntryWithExecutionPlan(entry, plan)
}

// EnrichEntryWithError adds error information to the log entry.
func EnrichEntryWithError(c *echo.Context, errorType, errorMessage string) {
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
	}
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
