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

// NamedPassthrough routes an opaque passthrough request to the specific
// provider instance registered under the given YAML instance name.
// This is preferred over RoutablePassthrough when multiple instances of the
// same provider type are configured, as it targets the exact credentials.
type NamedPassthrough interface {
	PassthroughByName(ctx context.Context, instanceName string, req *PassthroughRequest) (*PassthroughResponse, error)
}

// PassthroughProviderResolver resolves the concrete PassthroughProvider and
// its declared provider type for a given configured instance name, without
// executing the passthrough call itself. This allows middleware to pre-resolve
// the provider and store it in context before the handler runs.
type PassthroughProviderResolver interface {
	ResolvePassthroughByName(instanceName string) (PassthroughProvider, string, error)
}
