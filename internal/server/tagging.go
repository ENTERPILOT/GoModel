package server

import (
	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/tagging"
)

// TaggingCapture extracts request labels from the configured tagging headers
// and attaches them, together with the do-not-pass strip set, to the request
// context. It runs after RequestSnapshotCapture so audit logging still sees
// the original headers; stripping happens at the provider forwarding boundary.
func TaggingCapture(service *tagging.Service) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if service == nil || !service.HasRules() {
				return next(c)
			}
			scope := requestScope(c)
			scope.SetLabels(service.ExtractLabels(c.Request().Header))
			scope.SetTaggingStripHeaders(service.StripHeaders())
			return next(c)
		}
	}
}
