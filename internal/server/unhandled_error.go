package server

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"

	"github.com/enterpilot/gomodel/internal/core"
)

// unhandledErrorHandler is the echo HTTPErrorHandler: it receives every error
// that escapes the handler chain without being rendered — a panic recovered by
// middleware.Recover, a response serialization failure, or an echo status
// error raised by a built-in middleware (body limit, method not allowed).
//
// echo's default handler answers with a bare {"message": "Internal Server
// Error"} body and logs nothing, so a panicking admin handler left operators
// with a 500 and no trace of the cause. Unexpected errors are logged with the
// request path and, for panics, the stack, and rendered in the gateway error
// envelope. echo's own HTTPErrors keep their status and body through fallback.
func unhandledErrorHandler(fallback echo.HTTPErrorHandler) echo.HTTPErrorHandler {
	return func(c *echo.Context, err error) {
		if err == nil {
			return
		}
		// echo's own errors (body limit, method not allowed, ...) already
		// carry a status; leave their rendering to the default handler.
		if _, ok := errors.AsType[statusCodedError](err); ok {
			fallback(c, err)
			return
		}

		gatewayErr, ok := errors.AsType[*core.GatewayError](err)
		if !ok {
			gatewayErr = &core.GatewayError{
				Type:       "internal_error",
				Message:    "an unexpected error occurred",
				StatusCode: http.StatusInternalServerError,
				Err:        err,
			}
		}
		logUnhandledError(c, gatewayErr)

		if r, _ := echo.UnwrapResponse(c.Response()); r != nil && r.Committed {
			return
		}
		if writeErr := writeGatewayError(c, gatewayErr); writeErr != nil {
			slog.Error("failed to write error response", "error", writeErr, "path", c.Request().URL.Path)
		}
	}
}

// statusCodedError is an error that already names its HTTP status, as echo's
// built-in errors do (echo.HTTPStatusCoder plus error).
type statusCodedError interface {
	error
	StatusCode() int
}

// logUnhandledError records an error that reached the centralized handler. A
// recovered panic is split into its value and stack so the message stays
// readable and the stack lands in its own attribute.
func logUnhandledError(c *echo.Context, gatewayErr *core.GatewayError) {
	attrs := []any{
		"type", gatewayErr.Type,
		"status", gatewayErr.HTTPStatusCode(),
		"message", gatewayErr.Message,
	}
	if panicErr, ok := errors.AsType[*middleware.PanicStackError](gatewayErr.Err); ok {
		attrs = append(attrs, "panic", panicErr.Err, "stack", string(panicErr.Stack))
	} else if gatewayErr.Err != nil {
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
		slog.Error("unhandled request error", attrs...)
		return
	}
	slog.Warn("unhandled request error", attrs...)
}
