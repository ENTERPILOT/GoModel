package server

import (
	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/session"
)

// SessionCapture detects the client session id for model interaction requests
// and attaches it to the request context. It runs after RequestSnapshotCapture
// (detection reads the captured headers and body) and before audit logging so
// entries carry the session id from creation.
func SessionCapture(detector *session.Detector) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if detector == nil {
				return next(c)
			}
			req := c.Request()
			ctx := req.Context()
			snapshot := core.GetRequestSnapshot(ctx)
			if snapshot == nil || !core.IsModelInteractionPath(snapshot.Path) {
				return next(c)
			}
			if id := detector.Detect(snapshot, core.UserPathFromContext(ctx)); id != "" {
				c.SetRequest(req.WithContext(core.WithSessionID(ctx, id)))
			}
			return next(c)
		}
	}
}
