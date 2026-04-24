package server

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

// PassthroughResponseHandler writes a raw upstream response to the Echo
// response writer. Implementations are kept small and swappable so future
// requirements (e.g. different error shapes, response mutations) can be
// addressed by plugging in an alternative implementation.
type PassthroughResponseHandler interface {
	Handle(c *echo.Context, requestID string, resp *core.PassthroughResponse) error
}

// rawPassthroughResponseHandler forwards the upstream response exactly as
// received. All status codes (2xx, 4xx, 5xx) are proxied without
// normalization: upstream headers are copied (hop-by-hop headers excluded),
// X-GoModel-Request-ID is added, and the body is streamed verbatim.
type rawPassthroughResponseHandler struct{}

func newRawPassthroughResponseHandler() PassthroughResponseHandler {
	return &rawPassthroughResponseHandler{}
}

func (h *rawPassthroughResponseHandler) Handle(c *echo.Context, requestID string, resp *core.PassthroughResponse) error {
	if resp == nil || resp.Body == nil {
		provider := strings.TrimSpace(getPassthroughProviderType(c))
		return core.NewProviderError(provider, http.StatusBadGateway, "upstream returned empty passthrough response", nil)
	}
	defer func() { _ = resp.Body.Close() }()

	sc := resp.StatusCode
	if sc < 100 || sc > 599 {
		provider := strings.TrimSpace(getPassthroughProviderType(c))
		return core.NewProviderError(provider, http.StatusBadGateway,
			fmt.Sprintf("upstream returned invalid HTTP status code: %d", sc), nil)
	}

	copyPassthroughResponseHeaders(c.Response().Header(), resp.Headers)

	if requestID != "" {
		c.Response().Header().Set(core.RequestIDHeader, requestID)
	}

	c.Response().WriteHeader(sc)

	if isSSEContentType(resp.Headers) {
		if streamErr := flushStream(c.Response(), resp.Body); streamErr != nil {
			logPassthroughResponseStreamError(c, requestID, sc, streamErr)
		}
		return nil
	}

	if _, copyErr := io.Copy(c.Response(), resp.Body); copyErr != nil {
		logPassthroughResponseStreamError(c, requestID, sc, copyErr)
	}
	return nil
}

func logPassthroughResponseStreamError(c *echo.Context, requestID string, statusCode int, streamErr error) {
	if streamErr == nil {
		return
	}
	attrs := []any{
		"status", statusCode,
		"error", streamErr,
	}
	if requestID != "" {
		attrs = append(attrs, "request_id", requestID)
	}
	if c != nil && c.Request() != nil {
		req := c.Request()
		attrs = append(attrs, "method", req.Method, "path", req.URL.Path)
	}
	slog.Warn("passthrough response stream failed", attrs...)
}
