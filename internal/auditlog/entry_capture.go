package auditlog

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"gomodel/internal/core"
)

// PopulateRequestData copies the configured request capture fields into the log entry.
// Streaming handlers call this before creating the detached stream entry so request
// metadata is preserved even though the middleware finishes later.
func PopulateRequestData(entry *LogEntry, req *http.Request, cfg Config) {
	if entry == nil || req == nil {
		return
	}

	data := ensureLogData(entry)

	if cfg.LogHeaders {
		data.RequestHeaders = extractHeaders(req.Header)
	}

	if !cfg.LogBodies {
		return
	}

	snapshot := core.GetRequestSnapshot(req.Context())
	if snapshot == nil {
		return
	}

	switch body := snapshot.CapturedBody(); {
	case snapshot.BodyNotCaptured:
		data.RequestBodyTooBigToHandle = true
	case body != nil:
		captureLoggedRequestBody(entry, body)
	}
}

// PopulateResponseHeaders copies response headers into the log entry when header logging is enabled.
func PopulateResponseHeaders(entry *LogEntry, headers http.Header) {
	if entry == nil || headers == nil {
		return
	}

	data := ensureLogData(entry)
	data.ResponseHeaders = extractHeaders(headers)
}

// PopulateResponseData copies the configured response capture fields into the
// log entry from already-buffered response bytes.
func PopulateResponseData(entry *LogEntry, headers http.Header, body []byte, bodyTruncated bool, cfg Config) {
	if entry == nil {
		return
	}

	if cfg.LogHeaders {
		PopulateResponseHeaders(entry, headers)
	}
	if !cfg.LogBodies {
		return
	}

	data := ensureLogData(entry)
	if bodyTruncated {
		data.ResponseBodyTooBigToHandle = true
	}
	if len(body) == 0 {
		return
	}
	captureLoggedResponseBody(entry, body)
}

// CaptureInternalJSONExchange applies normal audit capture policy to an
// internal JSON request/response pair without requiring the caller to
// synthesize HTTP transport details in the server layer.
func CaptureInternalJSONExchange(
	entry *LogEntry,
	ctx context.Context,
	method,
	path string,
	requestBody,
	responseBody any,
	responseErr error,
	cfg Config,
) {
	if entry == nil {
		return
	}

	if req := internalJSONAuditRequest(ctx, method, path, requestIDForEntry(entry), requestBody, cfg.LogBodies); req != nil {
		PopulateRequestData(entry, req, cfg)
	}
	headers, body, truncated := internalJSONAuditResponse(responseBody, responseErr, requestIDForEntry(entry), cfg.LogBodies)
	PopulateResponseData(entry, headers, body, truncated, cfg)
}

func ensureLogData(entry *LogEntry) *LogData {
	if entry.Data == nil {
		entry.Data = &LogData{}
	}
	return entry.Data
}

func requestIDForEntry(entry *LogEntry) string {
	if entry == nil {
		return ""
	}
	return strings.TrimSpace(entry.RequestID)
}

func internalJSONAuditRequest(ctx context.Context, method, path, requestID string, bodyValue any, logBodies bool) *http.Request {
	headers := internalJSONAuditHeaders(ctx, requestID)
	req := &http.Request{
		Method: method,
		URL:    &url.URL{Path: path},
		Header: headers,
	}
	reqCtx := ctx
	if logBodies {
		capturedBody, bodyTooBig := internalJSONAuditRequestBody(bodyValue)
		snapshot := core.NewRequestSnapshot(
			method,
			path,
			nil,
			nil,
			headers,
			headers.Get("Content-Type"),
			capturedBody,
			bodyTooBig,
			requestID,
			nil,
			core.UserPathFromContext(ctx),
		)
		reqCtx = core.WithRequestSnapshot(ctx, snapshot)
	}
	return req.WithContext(reqCtx)
}

func internalJSONAuditRequestBody(bodyValue any) ([]byte, bool) {
	if bodyValue == nil {
		return nil, false
	}

	body, err := json.Marshal(bodyValue)
	if err != nil {
		return nil, false
	}

	return boundedAuditBody(body, false)
}

func internalJSONAuditResponse(bodyValue any, responseErr error, requestID string, logBodies bool) (http.Header, []byte, bool) {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if requestID != "" {
		headers.Set("X-Request-ID", requestID)
	}

	if !logBodies {
		return headers, nil, false
	}

	var (
		body []byte
		err  error
	)
	switch {
	case bodyValue != nil:
		body, err = json.Marshal(bodyValue)
	case responseErr != nil:
		var gatewayErr *core.GatewayError
		if errors.As(responseErr, &gatewayErr) && gatewayErr != nil {
			body, err = json.Marshal(gatewayErr.ToJSON())
		} else {
			body, err = json.Marshal(core.NewProviderError("", http.StatusInternalServerError, responseErr.Error(), responseErr).ToJSON())
		}
	default:
		return headers, nil, false
	}
	if err != nil {
		return headers, nil, false
	}
	capturedBody, truncated := boundedAuditBody(body, true)
	return headers, capturedBody, truncated
}

func internalJSONAuditHeaders(ctx context.Context, requestID string) http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if requestID != "" {
		headers.Set("X-Request-ID", requestID)
	}
	if userPath := strings.TrimSpace(core.UserPathFromContext(ctx)); userPath != "" {
		headers.Set(core.UserPathHeader, userPath)
	}
	if snapshot := core.GetRequestSnapshot(ctx); snapshot != nil {
		snapshotHeaders := snapshot.GetHeaders()
		for _, key := range []string{"Traceparent", "Tracestate", "Baggage"} {
			for _, value := range snapshotHeaders[key] {
				headers.Add(key, value)
			}
		}
	}
	return headers
}

func boundedAuditBody(body []byte, truncate bool) ([]byte, bool) {
	if body == nil {
		return []byte{}, false
	}
	if len(body) <= MaxBodyCapture {
		cloned := make([]byte, len(body))
		copy(cloned, body)
		return cloned, false
	}
	if !truncate {
		return nil, true
	}
	cloned := make([]byte, MaxBodyCapture)
	copy(cloned, body[:MaxBodyCapture])
	return cloned, true
}
