package server

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	batchstore "gomodel/internal/batch"
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
	return strings.TrimSpace(req.Header.Get("X-Request-ID"))
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
	req.Header.Set("X-Request-ID", requestID)

	ctx := req.Context()
	if strings.TrimSpace(core.GetRequestID(ctx)) != requestID {
		ctx = core.WithRequestID(ctx, requestID)
		*req = *req.WithContext(ctx)
	}

	return ctx, requestID
}

func sanitizePublicBatchMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}

	publicMetadata := make(map[string]string, len(metadata))
	for key, value := range metadata {
		switch key {
		case batchstore.RequestIDMetadataKey, batchstore.UsageLoggedAtMetadataKey:
			continue
		default:
			publicMetadata[key] = value
		}
	}
	if len(publicMetadata) == 0 {
		return nil
	}
	return publicMetadata
}
