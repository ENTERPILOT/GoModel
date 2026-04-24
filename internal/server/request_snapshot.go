package server

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

const requestSnapshotInlineBodyLimit int64 = 64 * 1024

// RequestSnapshotCapture captures immutable transport-level request data for
// model-facing endpoints. Known-small JSON bodies are captured once for the
// hot path; larger or unknown-size bodies only get a bounded selector peek and
// stay on the live request stream until the handler actually decodes them.
func RequestSnapshotCapture() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req, requestID := ensureRequestID(c.Request())
			c.Response().Header().Set(core.RequestIDHeader, requestID)
			desc := core.DescribeEndpoint(req.Method, req.URL.Path)
			if !desc.IngressManaged {
				c.SetRequest(req)
				return next(c)
			}

			userPath, err := core.NormalizeUserPath(req.Header.Get(core.UserPathHeader))
			if err != nil {
				return handleError(c, core.NewInvalidRequestError("invalid X-GoModel-User-Path header", err))
			}
			if userPath != "" {
				req.Header.Set(core.UserPathHeader, userPath)
			}

			bodyBytes, bodyNotCaptured, bodyCaptured, err := captureSmallRequestBodyForSnapshot(req, desc.BodyMode)
			if err != nil {
				return handleError(c, core.NewInvalidRequestError("failed to read request body", err))
			}

			snapshot := core.NewRequestSnapshotWithOwnedBody(
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

			ctx := core.WithRequestSnapshot(req.Context(), snapshot)
			if semantics := core.DeriveWhiteBoxPrompt(snapshot); semantics != nil {
				if !bodyCaptured {
					seedRequestBodySelectorHints(req, desc.BodyMode, semantics)
				}
				ctx = core.WithWhiteBoxPrompt(ctx, semantics)
			}
			c.SetRequest(req.WithContext(ctx))

			return next(c)
		}
	}
}

func ensureRequestID(req *http.Request) (*http.Request, string) {
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	requestID := strings.TrimSpace(core.GetRequestID(req.Context()))
	if requestID == "" {
		requestID = strings.TrimSpace(req.Header.Get(core.RequestIDHeader))
	}
	if requestID == "" {
		requestID = uuid.NewString()
	}

	req.Header.Set(core.RequestIDHeader, requestID)
	if current := strings.TrimSpace(core.GetRequestID(req.Context())); current != requestID {
		req = req.WithContext(core.WithRequestID(req.Context(), requestID))
	}
	return req, requestID
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
	req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	return bodyBytes, false, true, nil
}

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
	ctx := core.WithRequestSnapshot(req.Context(), updated)
	semanticSnapshot := updated
	if bodyNotCaptured {
		semanticSnapshot = snapshot.WithOwnedCapturedBody(bodyBytes, false)
	}
	if semantics := core.DeriveWhiteBoxPrompt(semanticSnapshot); semantics != nil {
		ctx = core.WithWhiteBoxPrompt(ctx, semantics)
	}
	c.SetRequest(req.WithContext(ctx))
}
