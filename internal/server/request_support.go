package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"gomodel/internal/core"
)

func requestIDFromContextOrHeader(req *http.Request) string {
	if req == nil {
		return ""
	}
	requestID := strings.TrimSpace(core.GetRequestID(req.Context()))
	if requestID != "" {
		return requestID
	}
	return strings.TrimSpace(req.Header.Get(core.RequestIDHeader))
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
