package server

import (
	"bytes"
	"context"
	"io"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/session"
)

const interactionParentHeader = "X-GoModel-Interaction-Parent"

type interactionParentLookup interface {
	GetInteractionParent(ctx context.Context, id string) (*auditlog.InteractionParent, error)
}

// sessionCapture resolves session identity after authentication and before
// routing. Dashboard continuations inherit their persisted parent's resolved
// session; ordinary requests use the configured detector.
func sessionCapture(detector *session.Detector, parentLookup interactionParentLookup, authMiddlewareAbsent bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if detector == nil {
				return next(c)
			}
			snapshot := core.GetRequestSnapshot(c.Request().Context())
			if snapshot == nil || !core.IsModelInteractionPath(snapshot.Path) {
				return next(c)
			}
			stamp := func(id string) bool {
				id = strings.TrimSpace(id)
				if id == "" {
					return false
				}
				req := c.Request()
				c.SetRequest(req.WithContext(core.WithSessionID(req.Context(), id)))
				auditlog.EnrichEntryWithSessionID(c, id)
				return true
			}
			detectAndStamp := func(snapshot *core.RequestSnapshot) bool {
				return stamp(detector.Detect(snapshot, core.UserPathFromContext(c.Request().Context())))
			}
			if stamp(interactionParentSession(c, parentLookup, authMiddlewareAbsent)) {
				return next(c)
			}

			// A captured body lets the detector resolve every rule in one pass.
			if snapshot.CapturedBodyView() != nil {
				detectAndStamp(snapshot)
				return next(c)
			}

			// Header rules do not need the body. Resolve them first so an
			// explicit session header keeps large/chunked requests off the
			// materialization below.
			if detectAndStamp(snapshot) {
				return next(c)
			}

			var err error
			snapshot, err = sessionDetectionSnapshot(c, snapshot)
			if err != nil {
				return handleError(c, bodyReadError(err))
			}
			detectAndStamp(snapshot)
			return next(c)
		}
	}
}

func interactionParentSession(c *echo.Context, lookup interactionParentLookup, allowWithoutAuth bool) string {
	if c == nil || lookup == nil ||
		(!allowWithoutAuth && !interactionContinuationAllowed(c.Request().Context())) {
		return ""
	}
	parentID := strings.TrimSpace(c.Request().Header.Get(interactionParentHeader))
	if parentID == "" || len(parentID) > 200 || strings.ContainsAny(parentID, ",\x00") {
		return ""
	}
	parent, err := lookup.GetInteractionParent(c.Request().Context(), parentID)
	if err != nil || parent == nil {
		return ""
	}
	parentPath := strings.TrimSpace(parent.UserPath)
	if parentPath == "" {
		parentPath = "/"
	}
	requestPath := strings.TrimSpace(core.UserPathFromContext(c.Request().Context()))
	if requestPath == "" {
		requestPath = "/"
	}
	if parentPath != requestPath {
		return ""
	}
	return strings.TrimSpace(parent.SessionID)
}

// sessionDetectionSnapshot returns a snapshot whose complete body is available
// for session detection. JSON endpoints materialize the whole body: the handler
// decodes every byte of it anyway and the server's body size limit already
// bounds the read, so a long conversation keeps its body signals and
// content-derived id past the audit capture limit. Opaque bodies are forwarded
// upstream as a stream, so they are only peeked up to MaxBodyCapture and
// replayed intact; larger ones fall back to header signals.
func sessionDetectionSnapshot(c *echo.Context, snapshot *core.RequestSnapshot) (*core.RequestSnapshot, error) {
	req := c.Request()
	if req.Body == nil {
		return snapshot, nil
	}
	switch core.DescribeEndpoint(snapshot.Method, snapshot.Path).BodyMode {
	case core.BodyModeJSON:
		return materializeSessionBody(c, snapshot)
	case core.BodyModeOpaque:
		return peekSessionBody(c, snapshot)
	default:
		return snapshot, nil
	}
}

// materializeSessionBody reads the complete JSON body once, replays it from
// memory for the handler, and returns a snapshot carrying every byte for
// detection. The audit capture on the shared snapshot still stops at
// MaxBodyCapture; only the detection view sees an oversized body.
func materializeSessionBody(c *echo.Context, snapshot *core.RequestSnapshot) (*core.RequestSnapshot, error) {
	req := c.Request()
	originalBody := req.Body
	body, err := io.ReadAll(originalBody)
	if err != nil {
		// Preserve the bytes already consumed even though this request will be
		// rejected, keeping the helper's ownership contract explicit.
		req.Body = &combinedReadCloser{
			Reader: io.MultiReader(bytes.NewReader(body), originalBody),
			rc:     originalBody,
		}
		return snapshot, err
	}
	// The server owns the original body and closes it after the handler;
	// requestBodyBytes hands this buffer to the handler without another copy.
	req.Body = newBytesReadCloser(body)
	if complete := storeRequestBodySnapshot(c, body); complete != nil {
		return complete, nil
	}
	return snapshot, nil
}

// peekSessionBody reads an opaque body up to MaxBodyCapture for detection and
// replays it intact. It never reads a known-oversized body and peeks only
// limit+1 bytes from an unknown-length one.
func peekSessionBody(c *echo.Context, snapshot *core.RequestSnapshot) (*core.RequestSnapshot, error) {
	req := c.Request()
	if req.ContentLength > auditlog.MaxBodyCapture {
		return markSessionBodyNotCaptured(c, snapshot), nil
	}

	originalBody := req.Body
	body, err := io.ReadAll(io.LimitReader(originalBody, auditlog.MaxBodyCapture+1))
	if err != nil {
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
	if complete := storeRequestBodySnapshot(c, body); complete != nil {
		return complete, nil
	}
	return snapshot, nil
}

// bodyReadError shapes a failed request body read. The body size limit
// reports its 413 on the error itself; anything else is a client that stopped
// sending.
func bodyReadError(err error) error {
	if echo.StatusCode(err) > 0 {
		return escapedGatewayError(err)
	}
	return core.NewInvalidRequestError("failed to read request body", err)
}

func markSessionBodyNotCaptured(c *echo.Context, snapshot *core.RequestSnapshot) *core.RequestSnapshot {
	updated := snapshot.WithOwnedCapturedBody(nil, true)
	req := c.Request()
	c.SetRequest(req.WithContext(core.WithRequestSnapshot(req.Context(), updated)))
	return updated
}
