package server

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/anthropicapi"
	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
)

// handleError converts gateway errors to an HTTP response, rendered in the wire
// dialect of the request path (Anthropic envelope for /v1/messages, otherwise
// the OpenAI-compatible envelope).
func handleError(c *echo.Context, err error) error {
	gatewayErr, ok := errors.AsType[*core.GatewayError](err)
	if !ok {
		gatewayErr = core.NewProviderError("", http.StatusInternalServerError, "an unexpected error occurred", err)
	}
	logHandledError(c, gatewayErr)
	enrichAuditEntryWithProviderAttempts(c)
	auditlog.EnrichEntryWithError(c, string(gatewayErr.Type), gatewayErr.Message, gatewayErrorCode(gatewayErr))
	applyErrorResponseHeaders(c, err)
	return writeGatewayError(c, gatewayErr)
}

// writeGatewayError renders a gateway error in the request's wire dialect
// without logging or audit enrichment, for callers that already recorded it.
func writeGatewayError(c *echo.Context, gatewayErr *core.GatewayError) error {
	if requestDialect(c) == "anthropic" {
		status, body := anthropicapi.ErrorFromGateway(gatewayErr)
		return c.JSON(status, body)
	}
	return c.JSON(gatewayErr.HTTPStatusCode(), gatewayErr.ToJSON())
}

// handleRouteNotFound renders unknown-route 404s in the caller's wire dialect
// so SDK clients raise clean typed errors instead of parsing echo's default
// {"message": "Not Found"} body. Anthropic SDK clients are recognized by the
// anthropic-version header they always send (the path itself is unclassified —
// that is what makes it a 404).
func handleRouteNotFound(c *echo.Context) error {
	r := c.Request()
	notFound := core.NewNotFoundError("unknown API endpoint: " + r.Method + " " + r.URL.Path)
	if requestDialect(c) == "anthropic" || r.Header.Get("anthropic-version") != "" {
		status, body := anthropicapi.ErrorFromGateway(notFound)
		return c.JSON(status, body)
	}
	return c.JSON(notFound.HTTPStatusCode(), notFound.ToJSON())
}

// requestDialect reports the ingress wire dialect classified for the request
// path (e.g. "anthropic", "openai_compat"), or "" when unclassified.
func requestDialect(c *echo.Context) string {
	if c == nil || c.Request() == nil {
		return ""
	}
	return core.DescribeEndpointPath(c.Request().URL.Path).Dialect
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
