package server

import (
	"net/http"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
	"gomodel/internal/mcpgateway"
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
