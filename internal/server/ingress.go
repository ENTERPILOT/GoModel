package server

import (
	"bytes"
	"io"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

// IngressCapture captures immutable transport-level request data for model-facing endpoints.
// It reads the request body once, restores it for downstream consumers, and stores both the
// ingress frame and a best-effort semantic envelope in the request context.
func IngressCapture() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if !isIngressManagedPath(c.Request().URL.Path) {
				return next(c)
			}

			req := c.Request()
			var bodyBytes []byte
			if req.Body != nil {
				var err error
				bodyBytes, err = io.ReadAll(req.Body)
				if err != nil {
					return handleError(c, core.NewInvalidRequestError("failed to read request body", err))
				}
			}
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))

			frame := &core.IngressFrame{
				Method:        req.Method,
				Path:          req.URL.Path,
				RouteParams:   cloneRouteParams(c.PathValues()),
				QueryParams:   cloneMultiMap(req.URL.Query()),
				Headers:       cloneMultiMap(req.Header),
				ContentType:   req.Header.Get("Content-Type"),
				RawBody:       append([]byte(nil), bodyBytes...),
				RequestID:     req.Header.Get("X-Request-ID"),
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
