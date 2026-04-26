package server

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

// handleError converts gateway errors to appropriate HTTP responses.
func handleError(c *echo.Context, err error) error {
	if gatewayErr, ok := errors.AsType[*core.GatewayError](err); ok {
		logHandledError(c, gatewayErr)
		auditlog.EnrichEntryWithError(c, string(gatewayErr.Type), gatewayErr.Message, gatewayErrorCode(gatewayErr))
		applyErrorResponseHeaders(c, err)
		return c.JSON(gatewayErr.HTTPStatusCode(), gatewayErr.ToJSON())
	}

	gatewayErr := core.NewProviderError("", http.StatusInternalServerError, "an unexpected error occurred", err)
	logHandledError(c, gatewayErr)
	auditlog.EnrichEntryWithError(c, string(gatewayErr.Type), gatewayErr.Message, gatewayErrorCode(gatewayErr))
	return c.JSON(gatewayErr.HTTPStatusCode(), gatewayErr.ToJSON())
}

type responseHeaderError interface {
	ResponseHeaders() http.Header
}

func applyErrorResponseHeaders(c *echo.Context, err error) {
	if c == nil || err == nil {
		return
	}
	var headerErr responseHeaderError
	if !errors.As(err, &headerErr) {
		return
	}
	for key, values := range headerErr.ResponseHeaders() {
		for i, value := range values {
			if i == 0 {
				c.Response().Header().Set(key, value)
				continue
			}
			c.Response().Header().Add(key, value)
		}
	}
}

func gatewayErrorCode(err *core.GatewayError) string {
	if err == nil || err.Code == nil {
		return ""
	}
	return *err.Code
}

func logHandledError(c *echo.Context, gatewayErr *core.GatewayError) {
	if gatewayErr == nil {
		return
	}

	attrs := []any{
		"type", gatewayErr.Type,
		"status", gatewayErr.HTTPStatusCode(),
		"message", gatewayErr.Message,
	}
	if gatewayErr.Provider != "" {
		attrs = append(attrs, "provider", gatewayErr.Provider)
	}
	if gatewayErr.Param != nil {
		attrs = append(attrs, "param", *gatewayErr.Param)
	}
	if gatewayErr.Code != nil {
		attrs = append(attrs, "code", *gatewayErr.Code)
	}
	if gatewayErr.Err != nil {
		attrs = append(attrs, "error", gatewayErr.Err)
	}
	if c != nil && c.Request() != nil {
		req := c.Request()
		attrs = append(attrs,
			"method", req.Method,
			"path", req.URL.Path,
			"request_id", requestIDFromContextOrHeader(req),
		)
	}

	if gatewayErr.HTTPStatusCode() >= http.StatusInternalServerError {
		slog.Error("request failed", attrs...)
		return
	}
	slog.Warn("request failed", attrs...)
}
