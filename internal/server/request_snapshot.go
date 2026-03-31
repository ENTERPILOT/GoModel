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

// RequestSnapshotCapture captures immutable transport-level request data for model-facing endpoints.
// Small request bodies are captured once and shared through context; oversized bodies are left
// on the live request stream so snapshot capture does not defeat audit-log body limits.
func RequestSnapshotCapture() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req, requestID := ensureRequestID(c.Request())
			c.Response().Header().Set("X-Request-ID", requestID)
			desc := core.DescribeEndpoint(req.Method, req.URL.Path)
			if !desc.IngressManaged {
				c.SetRequest(req)
				return next(c)
			}

			bodyBytes, bodyTooLarge, err := captureRequestBodyForSnapshot(req, desc.BodyMode)
			if err != nil {
				return handleError(c, core.NewInvalidRequestError("failed to read request body", err))
			}
			userPath, err := core.NormalizeUserPath(req.Header.Get(core.UserPathHeader))
			if err != nil {
				return handleError(c, core.NewInvalidRequestError("invalid X-GoModel-User-Path header", err))
			}
			if userPath != "" {
				req.Header.Set(core.UserPathHeader, userPath)
			}

			snapshot := core.NewRequestSnapshotWithOwnedBody(
				req.Method,
				req.URL.Path,
				snapshotRouteParams(req.URL.Path, routeParamsMap(c.PathValues())),
				req.URL.Query(),
				req.Header,
				req.Header.Get("Content-Type"),
				bodyBytes,
				bodyTooLarge,
				requestID,
				extractTraceMetadata(req.Header),
				userPath,
			)

			ctx := core.WithRequestSnapshot(req.Context(), snapshot)
			if semantics := core.DeriveWhiteBoxPrompt(snapshot); semantics != nil {
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
		requestID = strings.TrimSpace(req.Header.Get("X-Request-ID"))
	}
	if requestID == "" {
		requestID = uuid.NewString()
	}

	req.Header.Set("X-Request-ID", requestID)
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

func captureRequestBodyForSnapshot(req *http.Request, bodyMode core.BodyMode) ([]byte, bool, error) {
	if req.Body == nil {
		return []byte{}, false, nil
	}
	if bodyMode == core.BodyModeMultipart {
		return nil, false, nil
	}
	if req.ContentLength > auditlog.MaxBodyCapture {
		return nil, true, nil
	}

	limitedReader := io.LimitReader(req.Body, auditlog.MaxBodyCapture+1)
	bodyBytes, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, false, err
	}
	if int64(len(bodyBytes)) > auditlog.MaxBodyCapture {
		origBody := req.Body
		req.Body = &combinedReadCloser{
			Reader: io.MultiReader(bytes.NewReader(bodyBytes), origBody),
			rc:     origBody,
		}
		return nil, true, nil
	}

	if bodyBytes == nil {
		bodyBytes = []byte{}
	}
	req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	return bodyBytes, false, nil
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
	return bodyBytes, nil
}
