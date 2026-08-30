package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
)

func requestIDFromContextOrHeader(req *http.Request) string {
	if req == nil {
		return ""
	}
	requestID := strings.TrimSpace(core.GetRequestID(req.Context()))
	if requestID != "" {
		return requestID
	}
	return clientRequestID(req.Header)
}

// applyUserPathHeaderToContext lifts the user-path header into the request
// context when no user path is bound yet. Endpoints outside the snapshot
// middleware's model-interaction set (GET /v1/models) use it so header-only
// callers are scoped like inference requests. An invalid header is rejected
// with the same 400 the middleware would return; the response is then
// already written and the handler must stop, which the false return signals.
func applyUserPathHeaderToContext(c *echo.Context) (bool, error) {
	req := c.Request()
	ctx := req.Context()
	if core.UserPathFromContext(ctx) != "" {
		return true, nil
	}
	headerName := core.UserPathHeaderNameFromContext(ctx)
	userPath, err := core.NormalizeUserPath(req.Header.Get(headerName))
	if err != nil {
		return false, handleError(c, core.NewInvalidRequestError("invalid "+headerName+" header", err))
	}
	if userPath == "" {
		return true, nil
	}
	req.Header.Set(headerName, userPath)
	ctx = core.WithUserPathHeaderName(ctx, headerName)
	c.SetRequest(req.WithContext(core.WithEffectiveUserPath(ctx, userPath)))
	return true, nil
}

// maxClientRequestIDLength bounds the request ID a caller may supply; anything
// longer is replaced with a generated one.
const maxClientRequestIDLength = 128

// clientRequestID returns the caller-supplied request ID when it is a plain
// token: 1..maxClientRequestIDLength visible ASCII characters. The ID is echoed
// into logs, audit entries, and response headers, so anything else (spaces,
// control characters, non-ASCII) is treated as absent and a fresh ID is
// generated instead.
func clientRequestID(header http.Header) string {
	id := strings.TrimSpace(header.Get(core.RequestIDHeader))
	if id == "" || len(id) > maxClientRequestIDLength {
		return ""
	}
	for i := 0; i < len(id); i++ {
		if id[i] <= ' ' || id[i] > '~' {
			return ""
		}
	}
	return id
}

func requestContextWithRequestID(req *http.Request) (context.Context, string) {
	if req == nil {
		requestID := uuid.NewString()
		return core.WithRequestID(context.Background(), requestID), requestID
	}

	requestID := requestIDFromContextOrHeader(req)
	if requestID == "" {
		requestID = uuid.NewString()
	}

	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set(core.RequestIDHeader, requestID)

	ctx := req.Context()
	if strings.TrimSpace(core.GetRequestID(ctx)) != requestID {
		ctx = core.WithRequestID(ctx, requestID)
		*req = *req.WithContext(ctx)
	}

	return ctx, requestID
}
