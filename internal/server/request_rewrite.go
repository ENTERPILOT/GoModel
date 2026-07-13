package server

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
)

// RequestRewriteMiddleware invokes registered ext.RequestRewriter extensions
// on the raw JSON body of inference requests. It must run after
// authentication (rewriters only see authenticated traffic and the final
// user path) and before workflow resolution (so body changes, including the
// "model" field, affect routing, failover, guardrails, budgets, and caching).
//
// Rewriter errors fail the request (fail-closed). When a rewriter changes
// the body, the audit entry is pinned to the original client body first so
// operators always see what the client actually sent.
func RequestRewriteMiddleware(rewriters []ext.RequestRewriter, auditLogger auditlog.LoggerInterface) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			endpoint, ok := rewriteEndpoint(c.Request())
			if !ok {
				return next(c)
			}

			body, err := requestBodyBytes(c)
			if err != nil {
				return handleError(c, core.NewInvalidRequestError("failed to read request body", err))
			}

			in := ext.Input{
				Endpoint:  endpoint,
				Body:      body,
				Header:    redactCredentialHeaders(c.Request().Header),
				UserPath:  core.UserPathFromContext(c.Request().Context()),
				RequestID: core.GetRequestID(c.Request().Context()),
			}

			changed := false
			tokensSaved := 0
			for _, rw := range rewriters {
				res, rwErr := rw.Rewrite(c.Request().Context(), in)
				if rwErr != nil {
					return handleError(c, rewriterGatewayError(rw.Name(), rwErr))
				}
				if res == nil {
					continue
				}
				applyRewriteResponseHeaders(c, res.ResponseHeader)
				if res.Body != nil {
					recordRequestRevision(c, auditLogger, rw.Name(), len(in.Body), res)
					in.Body = res.Body
					changed = true
					if res.TokensSaved > 0 {
						tokensSaved += res.TokensSaved
					}
				}
			}

			if changed {
				pinOriginalAuditRequestBody(c, auditLogger)
				applyRewrittenBody(c, in.Body)
				if tokensSaved > 0 {
					req := c.Request()
					c.SetRequest(req.WithContext(core.WithRewriteTokensSaved(req.Context(), tokensSaved)))
				}
			}
			return next(c)
		}
	}
}

// redactCredentialHeaders clones the request headers with credential values
// (Authorization, cookies, API keys, ...) masked. Rewriters run post-auth and
// get UserPath for identity, so they never need raw credentials — and this
// keeps secrets out of anything a rewriter might echo into its audit detail.
func redactCredentialHeaders(header http.Header) http.Header {
	out := header.Clone()
	for key := range out {
		if core.IsCredentialHeader(key) {
			out[key] = []string{"[REDACTED]"}
		}
	}
	return out
}

// rewriteEndpoint reports whether the request targets an endpoint eligible
// for rewriting. Subroutes (count_tokens, input_tokens, compact, :id) are
// deliberately excluded via exact path matching.
func rewriteEndpoint(req *http.Request) (ext.Endpoint, bool) {
	if req == nil || req.Method != http.MethodPost || req.URL == nil {
		return "", false
	}
	switch req.URL.Path {
	case string(ext.EndpointChatCompletions):
		return ext.EndpointChatCompletions, true
	case string(ext.EndpointMessages):
		return ext.EndpointMessages, true
	case string(ext.EndpointResponses):
		return ext.EndpointResponses, true
	}
	return "", false
}

// applyRewrittenBody installs the rewritten body on the live request and
// refreshes the request snapshot (and derived WhiteBoxPrompt semantics) that
// downstream middleware and handlers consume.
func applyRewrittenBody(c *echo.Context, body []byte) {
	req := c.Request()
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	storeRequestBodySnapshot(c, body)
}

// recordRequestRevision appends one entry to the audit trail's
// request-revision chain: rewriter name, body sizes, the rewriter-provided
// change detail, and — only when body logging is enabled and the body is
// within the capture limit — the rewritten body itself.
func recordRequestRevision(c *echo.Context, auditLogger auditlog.LoggerInterface, name string, bytesBefore int, res *ext.Result) {
	if auditLogger == nil {
		return
	}
	cfg := auditLogger.Config()
	if !cfg.Enabled {
		return
	}

	revision := auditlog.RequestRevisionSnapshot{
		Rewriter:    name,
		BytesBefore: bytesBefore,
		BytesAfter:  len(res.Body),
		TokensSaved: res.TokensSaved,
		Detail:      res.Detail,
	}
	if cfg.LogBodies && int64(len(res.Body)) <= auditlog.MaxBodyCapture {
		revision.Body = auditlog.CaptureLoggedBody(res.Body)
	}
	auditlog.EnrichEntryWithRequestRevision(c, revision)
}

// pinOriginalAuditRequestBody captures the pre-rewrite request into the live
// audit entry so audit logs always record what the client sent. It respects
// the audit logger's header/body capture configuration and is a no-op when
// the entry already holds a request body.
func pinOriginalAuditRequestBody(c *echo.Context, auditLogger auditlog.LoggerInterface) {
	if auditLogger == nil {
		return
	}
	cfg := auditLogger.Config()
	if !cfg.Enabled {
		return
	}
	entry, ok := c.Get(string(auditlog.LogEntryKey)).(*auditlog.LogEntry)
	if !ok || entry == nil {
		return
	}
	auditlog.PopulateRequestData(entry, c.Request(), cfg)
}

func applyRewriteResponseHeaders(c *echo.Context, headers http.Header) {
	for key, values := range headers {
		for i, value := range values {
			if i == 0 {
				c.Response().Header().Set(key, value)
				continue
			}
			c.Response().Header().Add(key, value)
		}
	}
}

// rewriterGatewayError maps a rewriter error to a gateway error, fail-closed.
// Only the rewriter name and error are logged — never request bodies or
// headers.
func rewriterGatewayError(name string, err error) error {
	var rejection *ext.RejectionError
	if errors.As(err, &rejection) {
		status := rejection.Status
		if status < http.StatusBadRequest || status > 599 {
			status = http.StatusBadRequest
		}
		slog.Warn("request rewriter rejected request",
			"rewriter", name,
			"status", status,
			"code", rejection.Code,
		)
		gatewayErr := core.NewInvalidRequestErrorWithStatus(status, rejection.Message, nil)
		if rejection.Code != "" {
			code := rejection.Code
			gatewayErr.Code = &code
		}
		return gatewayErr
	}

	slog.Error("request rewriter failed", "rewriter", name, "error", err)
	return core.NewProviderError("", http.StatusInternalServerError, "request rewriter failed", err)
}
