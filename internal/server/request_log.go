package server

import (
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/labstack/echo/v5"
)

// redactSensitiveRequestURI keeps query parameters available to handlers while
// preventing the outer request logger from persisting OAuth credentials and
// transaction secrets. It applies generically so extensions do not need to
// expose provider-specific callback paths to Core.
func redactSensitiveRequestURI() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			request := c.Request()
			request.RequestURI = core.RedactSensitiveURLQuery(request.RequestURI)
			return next(c)
		}
	}
}
