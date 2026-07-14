package server

import (
	"bytes"
	"io"
	"net/http"
	"strings"

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
		enrichMCPAuditEntry(c)
		release, err := enforceRateLimit(c, s.rateLimiter, rateLimitRoute{})
		if err != nil {
			return handleError(c, err)
		}
		defer release()
		if err := enforceBudget(c, s.budgetChecker); err != nil {
			return handleError(c, err)
		}
	}
	if err := s.gateway.ServeHTTP(c.Response(), c.Request(), pinnedServer); err != nil {
		return handleError(c, err)
	}
	return nil
}

// enrichMCPAuditEntry labels the audit entry (and its live-log preview) with
// the JSON-RPC method carried by this POST — the tool name for tools/call —
// so MCP rows in the request log read as more than a bare path. The body is
// restored for the gateway handler; the body-limit middleware has already
// bounded its size.
func enrichMCPAuditEntry(c *echo.Context) {
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
