package server

import (
	"bytes"
	"io"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/session"
)

// SessionCapture detects the client session id for model interaction requests
// and attaches it to the request context. It runs after RequestSnapshotCapture
// (detection reads the captured headers and body) and after authentication so
// client-provided and auto-detected ids are scoped by the effective user path,
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
			detectAndStamp := func(snapshot *core.RequestSnapshot) bool {
				req := c.Request()
				id := detector.Detect(snapshot, core.UserPathFromContext(req.Context()))
				if id == "" {
					return false
				}
				c.SetRequest(req.WithContext(core.WithSessionID(req.Context(), id)))
				return true
			}

			// A captured body lets the detector resolve every rule in one pass.
			if snapshot.CapturedBodyView() != nil {
				detectAndStamp(snapshot)
				return next(c)
			}

			// Header rules do not need the body. Resolve them first so an
			// explicit session header keeps large/chunked requests on the
			// zero-copy path.
			if detectAndStamp(snapshot) {
				return next(c)
			}

			var err error
			snapshot, err = sessionDetectionSnapshot(c, snapshot)
			if err != nil {
				return handleError(c, core.NewInvalidRequestError("failed to read request body", err))
			}
			detectAndStamp(snapshot)
			return next(c)
		}
	}
}

// sessionDetectionSnapshot returns a snapshot whose complete body is available
// for session detection, up to MaxBodyCapture. It never reads a known-oversized
// body and peeks only limit+1 bytes from an unknown-length body. Oversized
// bodies are replayed intact for the handler and fall back to header signals.
func sessionDetectionSnapshot(c *echo.Context, snapshot *core.RequestSnapshot) (*core.RequestSnapshot, error) {
	switch core.DescribeEndpoint(snapshot.Method, snapshot.Path).BodyMode {
	case core.BodyModeJSON, core.BodyModeOpaque:
	default:
		return snapshot, nil
	}
	req := c.Request()
	if req.Body == nil {
		return snapshot, nil
	}
	if req.ContentLength > auditlog.MaxBodyCapture {
		return markSessionBodyNotCaptured(c, snapshot), nil
	}

	originalBody := req.Body
	body, err := io.ReadAll(io.LimitReader(originalBody, auditlog.MaxBodyCapture+1))
	if err != nil {
		// Preserve the bytes already consumed even though this request will be
		// rejected, keeping the helper's ownership contract explicit.
		req.Body = &combinedReadCloser{
			Reader: io.MultiReader(bytes.NewReader(body), originalBody),
			rc:     originalBody,
		}
		return snapshot, err
	}
	if int64(len(body)) > auditlog.MaxBodyCapture {
		req.Body = &combinedReadCloser{
			Reader: io.MultiReader(bytes.NewReader(body), originalBody),
			rc:     originalBody,
		}
		return markSessionBodyNotCaptured(c, snapshot), nil
	}

	// The full body fit. Cache it on the shared snapshot and replay the same
	// bytes to downstream code without another read or allocation.
	req.Body = &combinedReadCloser{Reader: bytes.NewReader(body), rc: originalBody}
	storeRequestBodySnapshot(c, body)
	if refreshed := core.GetRequestSnapshot(c.Request().Context()); refreshed != nil {
		return refreshed, nil
	}
	return snapshot, nil
}

func markSessionBodyNotCaptured(c *echo.Context, snapshot *core.RequestSnapshot) *core.RequestSnapshot {
	updated := snapshot.WithOwnedCapturedBody(nil, true)
	req := c.Request()
	c.SetRequest(req.WithContext(core.WithRequestSnapshot(req.Context(), updated)))
	return updated
}
