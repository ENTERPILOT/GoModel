package server

import (
	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/session"
)

// SessionCapture detects the client session id for model interaction requests
// and attaches it to the request context. It runs after RequestSnapshotCapture
// (detection reads the captured headers and body) and after authentication so
// weak ids and auto-detected ids are scoped by the effective user path,
// including a managed key's bound path. Audit entries pick the id up in the
// post-handler re-read.
func SessionCapture(detector *session.Detector) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if detector == nil {
				return next(c)
			}
			snapshot := core.GetRequestSnapshot(c.Request().Context())
			if snapshot == nil || !core.IsModelInteractionPath(snapshot.Path) {
				return next(c)
			}
			snapshot = sessionDetectionSnapshot(c, snapshot)
			req := c.Request()
			ctx := req.Context()
			if id := detector.Detect(snapshot, core.UserPathFromContext(ctx)); id != "" {
				c.SetRequest(req.WithContext(core.WithSessionID(ctx, id)))
			}
			return next(c)
		}
	}
}

// sessionDetectionSnapshot returns a snapshot whose body is available for
// session detection. Ingress capture only inlines bodies with a known
// Content-Length of at most 64 KiB, which would silently disable body-signal
// and content detection exactly where sessions matter most — large or chunked
// agent conversations. For chat and responses requests (whose handlers fully
// materialize the body anyway) the shared body materialization runs early, so
// detection, the handler, and audit capture reuse one buffered read. Bodies
// beyond the audit capture bound stay uncaptured and fall back to header
// signals.
func sessionDetectionSnapshot(c *echo.Context, snapshot *core.RequestSnapshot) *core.RequestSnapshot {
	if len(snapshot.CapturedBodyView()) > 0 {
		return snapshot
	}
	switch core.DescribeEndpoint(snapshot.Method, snapshot.Path).Operation {
	case core.OperationChatCompletions, core.OperationResponses:
	default:
		return snapshot
	}
	if _, err := requestBodyBytes(c); err != nil {
		return snapshot
	}
	if refreshed := core.GetRequestSnapshot(c.Request().Context()); refreshed != nil {
		return refreshed
	}
	return snapshot
}
