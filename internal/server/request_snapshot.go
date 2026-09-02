package server

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
)

const requestSnapshotInlineBodyLimit int64 = 64 * 1024

// RequestSnapshotCapture captures immutable transport-level request data for
// model-facing endpoints. Known-small JSON bodies are captured once for the
// hot path; larger or unknown-size bodies only get a bounded selector peek and
// stay on the live request stream until the handler actually decodes them.
func RequestSnapshotCapture(userPathHeader ...string) echo.MiddlewareFunc {
	userPathHeaderName := configuredUserPathHeaderName(userPathHeader...)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			// One context wrap installs the request scope; every later stage
			// fills it in place instead of re-wrapping (and copying) the request.
			req, scope := installRequestScope(c.Request())
			requestID := ensureRequestID(req, scope)
			c.Response().Header().Set(core.RequestIDHeader, requestID)
			desc := core.DescribeEndpoint(req.Method, req.URL.Path)
			if !desc.IngressManaged {
				// The configured header name must reach every handler that
				// reads the user path itself (GET /v1/models), not only the
				// model-interaction endpoints below.
				scope.SetUserPathHeaderName(userPathHeaderName)
				// Model endpoints that own their transport (MCP, realtime,
				// audio) take no snapshot, but their identity must still reach
				// the context: rate limits, budgets, session binding, and
				// usage attribution all read core.UserPathFromContext. A
				// managed key's bound path still wins — auth middleware runs
				// later and its context value shadows this one.
				if desc.ModelInteraction {
					userPath, err := core.NormalizeUserPath(req.Header.Get(userPathHeaderName))
					if err != nil {
						return handleError(c, core.NewInvalidRequestError("invalid "+userPathHeaderName+" header", err))
					}
					if userPath != "" {
						req.Header.Set(userPathHeaderName, userPath)
						scope.SetEffectiveUserPath(userPath)
					}
				}
				c.SetRequest(req)
				return next(c)
			}

			userPath, err := core.NormalizeUserPath(req.Header.Get(userPathHeaderName))
			if err != nil {
				return handleError(c, core.NewInvalidRequestError("invalid "+userPathHeaderName+" header", err))
			}
			if userPath != "" {
				req.Header.Set(userPathHeaderName, userPath)
			}

			bodyBytes, bodyNotCaptured, bodyCaptured, err := captureSmallRequestBodyForSnapshot(req, desc.BodyMode)
			if err != nil {
				return handleError(c, core.NewInvalidRequestError("failed to read request body", err))
			}

			// Query/route/trace maps are freshly built here, so the snapshot can
			// own them directly; only req.Header is live and gets cloned.
			snapshot := core.NewRequestSnapshotWithOwnedMaps(
				req.Method,
				req.URL.Path,
				snapshotRouteParams(req.URL.Path, routeParamsMap(c.PathValues())),
				req.URL.Query(),
				req.Header,
				req.Header.Get("Content-Type"),
				bodyBytes,
				bodyNotCaptured,
				requestID,
				extractTraceMetadata(req.Header),
				userPath,
			)

			scope.SetUserPathHeaderName(userPathHeaderName)
			scope.SetSnapshot(snapshot)
			if semantics := core.DeriveWhiteBoxPrompt(snapshot); semantics != nil {
				if !bodyCaptured {
					seedRequestBodySelectorHints(req, desc.BodyMode, semantics)
				}
				scope.SetWhiteBoxPrompt(semantics)
			}
			c.SetRequest(req)

			return next(c)
		}
	}
}

func configuredUserPathHeaderName(headerNames ...string) string {
	if len(headerNames) == 0 {
		return core.UserPathHeader
	}
	return core.UserPathHeaderName(headerNames[0])
}

// installRequestScope returns req carrying a request scope, wrapping its
// context once when it has none yet.
func installRequestScope(req *http.Request) (*http.Request, *core.RequestScope) {
	if scope := core.RequestScopeFromContext(req.Context()); scope != nil {
		return req, scope
	}
	ctx, scope := core.WithRequestScope(req.Context())
	return req.WithContext(ctx), scope
}

// ensureRequestID resolves the request id (context, client header, or a fresh
// UUID), mirrors it into the request header, and records it on the scope.
func ensureRequestID(req *http.Request, scope *core.RequestScope) string {
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	requestID := strings.TrimSpace(core.GetRequestID(req.Context()))
	if requestID == "" {
		requestID = clientRequestID(req.Header)
	}
	if requestID == "" {
		requestID = uuid.NewString()
	}

	req.Header.Set(core.RequestIDHeader, requestID)
	scope.SetRequestID(requestID)
	return requestID
}

func snapshotRouteParams(path string, params map[string]string) map[string]string {
	if provider, endpoint, ok := core.ParseProviderPassthroughPath(path); ok {
		if params == nil {
			params = make(map[string]string, 2)
		}
		if params["provider"] == "" {
			params["provider"] = provider
		}
		if params["endpoint"] == "" && endpoint != "" {
			params["endpoint"] = endpoint
		}
	}
	return params
}

func extractTraceMetadata(headers map[string][]string) map[string]string {
	traceHeaders := []string{"Traceparent", "Tracestate", "Baggage"}
	metadata := make(map[string]string, len(traceHeaders))
	for _, key := range traceHeaders {
		if values, ok := headers[key]; ok && len(values) > 0 {
			joined := strings.TrimSpace(strings.Join(values, ","))
			if joined != "" {
				metadata[key] = joined
			}
		}
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

func captureSmallRequestBodyForSnapshot(req *http.Request, bodyMode core.BodyMode) ([]byte, bool, bool, error) {
	if !shouldCaptureSmallRequestBody(req, bodyMode) {
		return nil, snapshotBodyNotCaptured(req, bodyMode), false, nil
	}

	originalBody := req.Body
	limitedReader := io.LimitReader(originalBody, requestSnapshotInlineBodyLimit+1)
	bodyBytes, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, false, false, err
	}
	if int64(len(bodyBytes)) > requestSnapshotInlineBodyLimit {
		req.Body = &combinedReadCloser{
			Reader: io.MultiReader(bytes.NewReader(bodyBytes), originalBody),
			rc:     originalBody,
		}
		return nil, snapshotBodyNotCaptured(req, bodyMode), false, nil
	}

	if bodyBytes == nil {
		bodyBytes = []byte{}
	}
	body := &bytesReadCloser{}
	body.Reset(bodyBytes)
	req.Body = body
	return bodyBytes, false, true, nil
}

// bytesReadCloser is io.NopCloser(bytes.NewReader(b)) in one allocation
// instead of two; it keeps bytes.Reader's WriteTo for efficient copies.
type bytesReadCloser struct {
	bytes.Reader
}

func (*bytesReadCloser) Close() error { return nil }

func shouldCaptureSmallRequestBody(req *http.Request, bodyMode core.BodyMode) bool {
	if req == nil || req.Body == nil {
		return false
	}
	switch bodyMode {
	case core.BodyModeJSON, core.BodyModeOpaque:
	default:
		return false
	}
	return req.ContentLength >= 0 && req.ContentLength <= requestSnapshotInlineBodyLimit
}

func snapshotBodyNotCaptured(req *http.Request, bodyMode core.BodyMode) bool {
	if req == nil {
		return false
	}
	switch bodyMode {
	case core.BodyModeJSON, core.BodyModeOpaque:
		return req.ContentLength > auditlog.MaxBodyCapture
	default:
		return false
	}
}

type combinedReadCloser struct {
	io.Reader
	rc io.ReadCloser
}

func (c *combinedReadCloser) Close() error {
	return c.rc.Close()
}

func requestBodyBytes(c *echo.Context) ([]byte, error) {
	if snapshot := core.GetRequestSnapshot(c.Request().Context()); snapshot != nil {
		if body := snapshot.CapturedBodyView(); body != nil {
			return body, nil
		}
	}

	req := c.Request()
	if req.Body == nil {
		return []byte{}, nil
	}

	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	if bodyBytes == nil {
		bodyBytes = []byte{}
	}
	req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	storeRequestBodySnapshot(c, bodyBytes)
	return bodyBytes, nil
}

func storeRequestBodySnapshot(c *echo.Context, bodyBytes []byte) {
	if c == nil {
		return
	}
	req := c.Request()
	snapshot := core.GetRequestSnapshot(req.Context())
	if snapshot == nil {
		return
	}

	bodyNotCaptured := int64(len(bodyBytes)) > auditlog.MaxBodyCapture
	capturedBody := bodyBytes
	if bodyNotCaptured {
		capturedBody = nil
	}

	updated := snapshot.WithOwnedCapturedBody(capturedBody, bodyNotCaptured)
	scope := requestScope(c)
	scope.SetSnapshot(updated)
	previous := core.GetWhiteBoxPrompt(req.Context())
	semanticSnapshot := updated
	if bodyNotCaptured {
		semanticSnapshot = snapshot.WithOwnedCapturedBody(bodyBytes, false)
	}
	if semantics := core.RefreshWhiteBoxPrompt(semanticSnapshot, previous); semantics != nil {
		scope.SetWhiteBoxPrompt(semantics)
	}
}
