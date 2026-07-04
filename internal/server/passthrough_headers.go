package server

import (
	"github.com/labstack/echo/v5"

	"gomodel/internal/providers"
)

// PassthroughHeaderCapture returns an Echo middleware that captures the incoming
// request headers, filters them against the hard-coded skip floor (credential
// headers and user-path headers), and stores the filtered map in the request
// context for downstream providers.
//
// The original request headers are never mutated; only the context is enriched.
// If userPathAlias is empty, only the hard-coded credential/user-path floor is
// applied.
func PassthroughHeaderCapture(userPathAlias string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			req := c.Request()
			filtered := providers.FilterIncomingHeaders(req.Header, userPathAlias)
			ctx := providers.WithPassthroughHeaders(req.Context(), filtered)
			c.SetRequest(req.WithContext(ctx))
			return next(c)
		}
	}
}