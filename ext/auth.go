package ext

import (
	"context"
	"net/http"
	"slices"
)

type authenticationContextKey struct{}

// Authentication describes an identity established by an extension.
// PrincipalID must be a stable, non-secret identifier within the
// authenticator's namespace. UserPath is the existing GoModel authorization
// and accounting subject; it is deliberately separate because a login identity
// and a policy hierarchy are not the same thing.
type Authentication struct {
	PrincipalID     string
	UserPath        string
	Labels          []string
	DashboardAccess bool
	// Method is a short, stable audit identifier such as "oidc" or "saml".
	// Core normalizes safe identifiers and records "extension" when it is empty
	// or invalid.
	Method string
}

// RequestAuthenticator authenticates requests using a mechanism other than
// the core bearer-token authenticators (for example an OIDC browser session).
// A nil result with a nil error means the mechanism does not apply to the
// request. Implementations must be safe for concurrent use.
type RequestAuthenticator interface {
	Name() string
	AuthenticateRequest(ctx context.Context, request *http.Request) (*Authentication, error)
}

// WithAuthentication attaches an extension-established identity to a request
// context. Core calls this after accepting an authenticator result.
func WithAuthentication(ctx context.Context, authentication Authentication) context.Context {
	authentication.Labels = slices.Clone(authentication.Labels)
	return context.WithValue(ctx, authenticationContextKey{}, authentication)
}

// AuthenticationFromContext returns the extension-established identity, if
// the request was authenticated by an extension.
func AuthenticationFromContext(ctx context.Context) (Authentication, bool) {
	if ctx == nil {
		return Authentication{}, false
	}
	authentication, ok := ctx.Value(authenticationContextKey{}).(Authentication)
	authentication.Labels = slices.Clone(authentication.Labels)
	return authentication, ok
}
