package server

import (
	"io"
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

	copyPassthroughResponseHeaders(c.Response().Header(), resp.Headers)

	if requestID != "" {
		c.Response().Header().Set(core.RequestIDHeader, requestID)
	}

	c.Response().WriteHeader(resp.StatusCode)

	if isSSEContentType(resp.Headers) {
		return flushStream(c.Response(), resp.Body)
	}

	_, err := io.Copy(c.Response(), resp.Body)
	return err
}
