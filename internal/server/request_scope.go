package server

import (
	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
)

// requestScope returns the request's mutable scope. RequestSnapshotCapture
// installs it for every request that passes through the middleware chain;
// handlers reached directly (tests, embedded use) get one installed here so
// later stages can update request state in place without re-wrapping the
// request context.
func requestScope(c *echo.Context) *core.RequestScope {
	req := c.Request()
	if scope := core.RequestScopeFromContext(req.Context()); scope != nil {
		return scope
	}
	ctx, scope := core.WithRequestScope(req.Context())
	c.SetRequest(req.WithContext(ctx))
	return scope
}
