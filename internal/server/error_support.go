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
	auditlog.EnrichEntryWithGatewayError(c, gatewayErr)
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

// gatewayErrorHandler renders every error that escapes a handler — a recovered
// panic, a response body the JSON serializer could not encode, or an
// echo.HTTPError raised by middleware — in the caller's wire dialect, and logs
// it. Echo's default handler answers with a bare
// {"message": "Internal Server Error"} and logs nothing at all, so such a
// failure reaches the operator as a 500 naming neither the endpoint nor the
// cause, and reaches the client in an envelope no OpenAI SDK can parse.
func gatewayErrorHandler(c *echo.Context, err error) {
	if err == nil {
		return
	}
	// Once the status line and body are on the wire nothing can be changed;
	// the response stands as the handler left it.
	if response, unwrapErr := echo.UnwrapResponse(c.Response()); unwrapErr == nil && response.Committed {
		return
	}

	gatewayErr := escapedGatewayError(err)
	// gatewayErr.Err carries the original error, which for a recovered panic is
	// echo's PanicStackError: its message is the panic value plus the stack.
	logHandledError(c, gatewayErr)
	if writeErr := writeGatewayError(c, gatewayErr); writeErr != nil {
		slog.Error("failed to send error response", "error", writeErr)
	}
}

// escapedGatewayError classifies an error that reached the central handler. A
// gateway error is already shaped; an echo.HTTPError keeps its status; anything
// else is a failure inside the gateway and stays opaque to the client.
func escapedGatewayError(err error) *core.GatewayError {
	if gatewayErr, ok := errors.AsType[*core.GatewayError](err); ok {
		return gatewayErr
	}

	// Echo carries the intended status on the error itself — BodyLimit's 413,
	// the router's 405, any middleware's echo.NewHTTPError. An error without
	// one is a failure inside the gateway.
	status, message := http.StatusInternalServerError, "an unexpected error occurred"
	if code := echo.StatusCode(err); code > 0 {
		status, message = code, echoErrorMessage(err, code)
	}
	if status < http.StatusInternalServerError {
		return core.NewInvalidRequestErrorWithStatus(status, message, err)
	}
	return &core.GatewayError{
		Type:       core.ErrorTypeInternal,
		Message:    message,
		StatusCode: status,
		Err:        err,
	}
}

// echoErrorMessage is the client-facing text of an echo error: its own message
// when it carries one, else the status text.
func echoErrorMessage(err error, status int) string {
	if httpErr, ok := errors.AsType[*echo.HTTPError](err); ok && httpErr.Message != "" {
		return httpErr.Message
	}
	if text := http.StatusText(status); text != "" {
		return text
	}
	return "request failed"
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
