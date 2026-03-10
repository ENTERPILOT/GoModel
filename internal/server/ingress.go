package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

// IngressCapture captures immutable transport-level request data for model-facing endpoints.
// Small request bodies are captured once and shared through context; oversized bodies are left
// on the live request stream so ingress capture does not defeat audit-log body limits.
func IngressCapture() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if !isIngressManagedPath(c.Request().URL.Path) {
				return next(c)
			}

			req := c.Request()
			bodyBytes, bodyTooLarge, err := captureIngressBody(req)
			if err != nil {
				return handleError(c, core.NewInvalidRequestError("failed to read request body", err))
			}

			frame := &core.IngressFrame{
				Method:          req.Method,
				Path:            req.URL.Path,
				RouteParams:     cloneRouteParams(c.PathValues()),
				QueryParams:     cloneMultiMap(req.URL.Query()),
				Headers:         cloneMultiMap(req.Header),
				ContentType:     req.Header.Get("Content-Type"),
				RawBody:         bodyBytes,
				RawBodyTooLarge: bodyTooLarge,
				RequestID:       req.Header.Get("X-Request-ID"),
				TraceMetadata: extractTraceMetadata(req.Header),
			}

			ctx := core.WithIngressFrame(req.Context(), frame)
			if env := core.BuildSemanticEnvelope(frame); env != nil {
				ctx = core.WithSemanticEnvelope(ctx, env)
			}
			c.SetRequest(req.WithContext(ctx))

			return next(c)
		}
	}
}

func isIngressManagedPath(path string) bool {
	if strings.HasPrefix(path, "/p/") {
		return true
	}
	if strings.HasPrefix(path, "/v1/files") {
		return false
	}
	return auditlog.IsModelInteractionPath(path)
}

func cloneMultiMap(src map[string][]string) map[string][]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string][]string, len(src))
	for key, values := range src {
		if len(values) == 0 {
			dst[key] = nil
			continue
		}
		cloned := make([]string, len(values))
		copy(cloned, values)
		dst[key] = cloned
	}
	return dst
}

func cloneRouteParams(pathValues echo.PathValues) map[string]string {
	if len(pathValues) == 0 {
		return nil
	}
	params := make(map[string]string, len(pathValues))
	for _, pv := range pathValues {
		params[pv.Name] = pv.Value
	}
	return params
}

func extractTraceMetadata(headers map[string][]string) map[string]string {
	traceHeaders := []string{"Traceparent", "Tracestate", "Baggage"}
	metadata := make(map[string]string, len(traceHeaders))
	for _, key := range traceHeaders {
		if values, ok := headers[key]; ok && len(values) > 0 && strings.TrimSpace(values[0]) != "" {
			metadata[key] = values[0]
		}
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

func captureIngressBody(req *http.Request) ([]byte, bool, error) {
	if req.Body == nil {
		return []byte{}, false, nil
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
	if frame := core.GetIngressFrame(c.Request().Context()); frame != nil && frame.RawBody != nil {
		return frame.RawBody, nil
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

func decodeJSONRequest(c *echo.Context, target any) error {
	bodyBytes, err := requestBodyBytes(c)
	if err != nil {
		return err
	}
	return json.Unmarshal(bodyBytes, target)
}
