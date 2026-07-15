package server

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/goccy/go-json"
	"github.com/labstack/echo/v5"
	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/mcpgateway"
)

// mcpService adapts Echo requests to the MCP gateway. It stays a thin
// transport layer: gate the feature, enforce admission (rate limits and
// budget, per user path — the same consumer controls as model endpoints),
// then hand the exchange to the gateway, which owns the MCP protocol.
type mcpService struct {
	gateway       *mcpgateway.Service
	budgetChecker BudgetChecker
	rateLimiter   RateLimiter
	enabled       bool
	// logBodies mirrors the audit logger config. /mcp is not ingress-managed,
	// so JSON-RPC request bodies have no snapshot to be captured from, and the
	// SDK answers POSTs as SSE, which the middleware's response capture skips —
	// both are captured here instead when body logging is on.
	logBodies bool
}

// handle serves one MCP HTTP exchange. pinnedServer is the /mcp/{server}
// path segment, or "" for the aggregated endpoint.
func (s *mcpService) handle(c *echo.Context, pinnedServer string) error {
	if !s.enabled || s.gateway == nil {
		return handleError(c, core.NewInvalidRequestErrorWithStatus(http.StatusNotImplemented, "the MCP gateway is disabled", nil))
	}
	// POSTs carry the JSON-RPC traffic (every request, including tools/call),
	// so they count against user-path rate limits and budget gates. GET (the
	// notification stream) and DELETE (session teardown) stay free.
	if c.Request().Method == http.MethodPost {
		enrichMCPAuditEntry(c, s.logBodies)
		release, err := enforceRateLimit(c, s.rateLimiter, rateLimitRoute{})
		if err != nil {
			return handleError(c, err)
		}
		defer release()
		if err := enforceBudget(c, s.budgetChecker); err != nil {
			return handleError(c, err)
		}
		if s.logBodies {
			// A POST's SSE reply carries the JSON-RPC response and ends with
			// it, so buffering is bounded; the long-lived GET notification
			// stream is never wrapped.
			capture := &mcpResponseCapture{ResponseWriter: c.Response()}
			c.SetResponse(capture)
			defer capture.enrich(c)
		}
	}
	// MCP responses relay upstream JSON-RPC frames that can echo request
	// values; nosniff guarantees browsers never interpret them as HTML.
	c.Response().Header().Set("X-Content-Type-Options", "nosniff")
	if err := s.gateway.ServeHTTP(c.Response(), c.Request(), pinnedServer); err != nil {
		return handleError(c, err)
	}
	return nil
}

// enrichMCPAuditEntry labels the audit entry (and its live-log preview) with
// the JSON-RPC method carried by this POST — the tool name for tools/call —
// so MCP rows in the request log read as more than a bare path, and captures
// the JSON-RPC frame as the request body when body logging is on. The body is
// restored for the gateway handler; the body-limit middleware has already
// bounded its size.
func enrichMCPAuditEntry(c *echo.Context, logBodies bool) {
	req := c.Request()
	if req.Body == nil {
		return
	}
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil || len(body) == 0 {
		return
	}

	if label := mcpAuditLabel(body); label != "" {
		auditlog.EnrichEntry(c, label, "mcp")
	}
	if logBodies {
		auditlog.EnrichEntryWithRawRequestBody(c, body)
	}
}

// mcpAuditLabel derives the request-log label from one JSON-RPC frame: the
// tool/prompt name for calls, otherwise the method. Empty means unlabelable
// (a bare response or malformed frame).
func mcpAuditLabel(body []byte) string {
	method := strings.TrimSpace(gjson.GetBytes(body, "method").String())
	if method == "" {
		return ""
	}
	if name := strings.TrimSpace(gjson.GetBytes(body, "params.name").String()); name != "" &&
		(method == "tools/call" || method == "prompts/get") {
		return name
	}
	return method
}

// mcpResponseCapture tees an MCP POST response so its SSE-framed JSON-RPC
// reply can be audit-logged. The middleware's own capture deliberately skips
// text/event-stream (chat streams belong to the stream observer), so without
// this tee MCP responses would never reach the audit entry. Capture is capped
// at the audit body limit; overflow only sets the truncation flag.
type mcpResponseCapture struct {
	http.ResponseWriter
	body      bytes.Buffer
	truncated bool
}

func (r *mcpResponseCapture) Write(b []byte) (int, error) {
	if !r.truncated {
		remaining := auditlog.MaxBodyCapture - r.body.Len()
		switch {
		case remaining >= len(b):
			r.body.Write(b)
		case remaining > 0:
			r.body.Write(b[:remaining])
			r.truncated = true
		default:
			r.truncated = true
		}
	}
	return r.ResponseWriter.Write(b)
}

// Flush keeps SSE delivery working through the tee.
func (r *mcpResponseCapture) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *mcpResponseCapture) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// enrich records the captured SSE reply on the audit entry. Non-SSE responses
// (202 accepts, JSON errors) are left to the middleware's own capture, which
// handles them already — enriching those here would double-capture.
func (r *mcpResponseCapture) enrich(c *echo.Context) {
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header().Get("Content-Type"), ";")[0]))
	if contentType != "text/event-stream" {
		return
	}
	auditlog.EnrichEntryWithCapturedResponseBody(c, mcpSSEAuditBody(r.body.Bytes()), r.truncated)
}

// mcpSSEAuditBody decodes the JSON-RPC messages out of a buffered SSE body:
// one message per data line. A single message is stored bare, several as a
// list; a payload that does not decode (e.g. cut by the capture cap) falls
// back to its raw text.
func mcpSSEAuditBody(raw []byte) any {
	var frames []any
	for line := range strings.Lines(string(raw)) {
		payload, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "data:")
		if !ok {
			continue
		}
		payload = strings.TrimSpace(payload)
		if payload == "" {
			continue
		}
		var decoded any
		if err := json.Unmarshal([]byte(payload), &decoded); err == nil {
			frames = append(frames, decoded)
		} else {
			frames = append(frames, payload)
		}
	}
	switch len(frames) {
	case 0:
		return nil
	case 1:
		return frames[0]
	default:
		return frames
	}
}
