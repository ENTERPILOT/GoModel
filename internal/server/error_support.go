package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"

	"github.com/enterpilot/gomodel/internal/anthropicapi"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/requestlog"
)

// handleError converts gateway errors to an HTTP response, rendered in the wire
// dialect of the request path (Anthropic envelope for /v1/messages, otherwise
// the OpenAI-compatible envelope).
func handleError(c *echo.Context, err error) error {
	return writeGatewayError(c, recordHandledError(c, err))
}

// handleErrorAsAnthropic renders err in the Anthropic error envelope whatever
// the path's own dialect is, with the same logging and request log enrichment as
// handleError. The models routes serve both dialects on one path, so their
// Anthropic clients are identified by the anthropic-version header instead.
func handleErrorAsAnthropic(c *echo.Context, err error) error {
	status, body := anthropicapi.ErrorFromGateway(recordHandledError(c, err))
	return c.JSON(status, body)
}

// statusClientClosedRequest is the de facto status proxies use for requests the
// client aborted before the response was ready.
const statusClientClosedRequest = 499

// recordHandledError normalizes err to a gateway error, logs it and enriches
// the request log entry and response headers, returning the error left to render.
func recordHandledError(c *echo.Context, err error) *core.GatewayError {
	if isClientClosedRequest(c, err) {
		return recordClientClosedRequest(c, err)
	}
	gatewayErr, ok := errors.AsType[*core.GatewayError](err)
	if !ok {
		gatewayErr = core.NewProviderError("", http.StatusInternalServerError, "an unexpected error occurred", err)
	}
	logHandledError(c, gatewayErr)
	enrichRequestLogEntryWithProviderAttempts(c)
	requestlog.EnrichEntryWithGatewayError(c, gatewayErr)
	applyErrorResponseHeaders(c, err)
	return gatewayErr
}

// isClientClosedRequest reports whether err is the caller's own cancellation:
// the request context is canceled and err carries that cancellation. Both are
// required, so a provider failure racing a disconnect still counts as one.
func isClientClosedRequest(c *echo.Context, err error) bool {
	if c == nil || c.Request() == nil || !errors.Is(err, context.Canceled) {
		return false
	}
	return errors.Is(c.Request().Context().Err(), context.Canceled)
}

// recordClientClosedRequest records a request the client abandoned before the
// response was ready as client_disconnected with status 499, the way the
// streaming path does, so the request log and error metrics do not count it as
// a provider failure. The rendered body only reaches a client that is gone.
func recordClientClosedRequest(c *echo.Context, err error) *core.GatewayError {
	gatewayErr := core.NewInvalidRequestErrorWithStatus(statusClientClosedRequest, "request canceled", err).
		WithCode("request_canceled")
	logHandledError(c, gatewayErr)
	enrichRequestLogEntryWithProviderAttempts(c)
	requestlog.EnrichEntryWithError(c, "client_disconnected", err.Error(), "")
	return gatewayErr
}

// writeGatewayError renders a gateway error in the request's wire dialect
// without logging or request log enrichment, for callers that already recorded it.
func writeGatewayError(c *echo.Context, gatewayErr *core.GatewayError) error {
	if requestDialect(c) == "anthropic" {
		status, body := anthropicapi.ErrorFromGateway(gatewayErr)
		return c.JSON(status, body)
	}
	return c.JSON(gatewayErr.HTTPStatusCode(), gatewayErr.ToJSON())
}

// escapedErrorHandledKey holds the escaped error the central handler last
// dealt with on a request.
const escapedErrorHandledKey = "gomodel.escaped-error-handled"

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
	// echo's default request logger hands an error here and then returns it,
	// so the server delivers the same error a second time. A different error
	// later on the same request, such as a panic, is still handled.
	if handled, ok := c.Get(escapedErrorHandledKey).(error); ok && isSameError(err, handled) {
		return
	}
	c.Set(escapedErrorHandledKey, err)
	// Once the response has started it cannot become an error response, so a
	// failure while writing it is a broken relay (most often the client hanging
	// up), not a gateway 500. A panic is still reported as one. Only the log is
	// written: the request log middleware has already queued the request's entry, so
	// a relay that wants the failure on the request log row records it before
	// returning (recordResponseFailure).
	committed := responseCommitted(c)
	if _, panicked := errors.AsType[*middleware.PanicStackError](err); committed && !panicked {
		logStreamFailure(c, responseTerminatedMessage, classifyStreamError(c.Request().Context(), err), err)
		return
	}
	gatewayErr := escapedGatewayError(err)
	logHandledError(c, gatewayErr)
	// Finalize as handleError does, so an error that never passed through it —
	// a panic, a rate-limit error returned rather than rendered — still lands
	// on the request log row and keeps headers such as Retry-After. The request log half is
	// skipped once an error is recorded: handleError returns its own response
	// write failures here, and re-enriching would replace the real cause with
	// this generic one.
	if !requestlog.HasRecordedError(c) {
		enrichRequestLogEntryWithProviderAttempts(c)
		requestlog.EnrichEntryWithGatewayError(c, gatewayErr)
	}
	applyErrorResponseHeaders(c, err)

	// Once the status line and body are on the wire nothing can be changed;
	// the response stands as the handler left it.
	if committed {
		return
	}
	if writeErr := writeGatewayError(c, gatewayErr); writeErr != nil {
		slog.Error("failed to send error response", "error", writeErr)
	}
}

const responseTerminatedMessage = "response terminated abnormally"

// recordResponseFailure records a response body that broke after the status
// line was sent, on the request log entry and in the log. Nothing can be rendered
// any more, so the caller returns nil.
func recordResponseFailure(c *echo.Context, err error) {
	if !requestlog.HasRecordedError(c) {
		enrichRequestLogEntryWithProviderAttempts(c)
	}
	recordStreamFailure(c, nil, responseTerminatedMessage, err)
}

// isSameError reports whether err is, or wraps, handled. Comparing two error
// values of an uncomparable type panics; those count as different errors.
func isSameError(err, handled error) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()
	return errors.Is(err, handled)
}

// responseCommitted reports whether the status line is already on the wire.
func responseCommitted(c *echo.Context) bool {
	response, err := echo.UnwrapResponse(c.Response())
	return err == nil && response.Committed
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
	status := echo.StatusCode(err)
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	if status < http.StatusInternalServerError {
		return core.NewInvalidRequestErrorWithStatus(status, echoErrorMessage(err, status), err)
	}
	// A 5xx message describes what broke inside the gateway and can quote an
	// internal error verbatim (echo's own Decompress middleware answers with
	// err.Error()), so the client gets the same opaque text every other
	// gateway 500 uses. The cause travels in Err, which only the logs read.
	return &core.GatewayError{
		Type:       core.ErrorTypeInternal,
		Message:    "an unexpected error occurred",
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

// invalidRequestBodyError renders a request-body decode failure for the client.
// The JSON decoder's own wording names Go types and struct fields and, inside a
// complete document, reports a type mismatch as an unexpected end of input, so
// the body is re-read to name the offending member instead.
func invalidRequestBodyError(c *echo.Context, err error) *core.GatewayError {
	body, readErr := requestBodyBytes(c)
	if readErr != nil {
		return core.NewInvalidRequestError("invalid request body: "+err.Error(), err)
	}
	return core.NewInvalidRequestBodyError(body, err)
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
	// A recovered panic arrives as echo's PanicStackError, whose message is
	// the panic value followed by the whole stack; split them so the message
	// stays readable and the stack lands in its own attribute.
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

	status := gatewayErr.HTTPStatusCode()
	if status == statusClientClosedRequest {
		slog.Debug("request canceled by client", attrs...)
		return
	}
	if status >= http.StatusInternalServerError {
		slog.Error("request failed", attrs...)
		return
	}
	slog.Warn("request failed", attrs...)
}
