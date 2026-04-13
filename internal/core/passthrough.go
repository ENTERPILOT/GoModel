package core

import (
	"context"
	"io"
	"net/http"
)

// PassthroughRequest is the transport-oriented request for opaque provider-native forwarding.
type PassthroughRequest struct {
	Method   string
	Endpoint string
	Body     io.ReadCloser
	Headers  http.Header
}

// PassthroughResponse is the raw upstream response for opaque forwarding.
// Body is an io.ReadCloser returned by the upstream provider, and callers are
// responsible for closing it when they are finished with the response body.
type PassthroughResponse struct {
	StatusCode int
	Headers    map[string][]string
	Body       io.ReadCloser
}

// PassthroughProvider supports opaque provider-native forwarding.
type PassthroughProvider interface {
	Passthrough(ctx context.Context, req *PassthroughRequest) (*PassthroughResponse, error)
}

// RoutablePassthrough resolves a provider type before issuing an opaque
// passthrough request.
type RoutablePassthrough interface {
	Passthrough(ctx context.Context, providerType string, req *PassthroughRequest) (*PassthroughResponse, error)
}

// PassthroughSemanticEnricher derives provider-specific passthrough metadata
// from ingress transport and best-effort prompt state before execution
// workflow resolution runs.
type PassthroughSemanticEnricher interface {
	ProviderType() string
	Enrich(snapshot *RequestSnapshot, prompt *WhiteBoxPrompt, info *PassthroughRouteInfo) *PassthroughRouteInfo
}
