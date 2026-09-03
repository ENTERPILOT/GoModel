package ext

import (
	"context"
	"net/http"
	"slices"
	"time"
)

type authenticationContextKey struct{}
type clearedAuthentication struct{}

// Provider-neutral response headers let the bundled dashboard discover an
// extension-managed browser authentication flow without knowing whether the
// provider uses OIDC, SAML, or another protocol. Values are app-local paths.
const (
	AuthenticationLoginHeader  = "X-GoModel-Auth-Login"
	AuthenticationLogoutHeader = "X-GoModel-Auth-Logout"
	AuthenticationUserHeader   = "X-Gomodel-Auth-User" // canonical textproto spelling; header names are case-insensitive
)

// Authentication describes an identity established by an extension.
// PrincipalID must be a stable, non-secret identifier within the
// authenticator's namespace. UserPath is the existing GoModel authorization
// and accounting subject; it is deliberately separate because a login identity
// and a policy hierarchy are not the same thing.
type Authentication struct {
	PrincipalID string
	UserPath    string
	// AccessScope confines the identity to one user-path subtree: the objects
	// it may address on /v1 lifecycle endpoints and, with DashboardAccess,
	// the subtree it administers. Empty means unconfined, so an SSO admin whose
	// UserPath is a personal identity such as /users/alice keeps the whole
	// gateway. Set it to a tenant root (for example /team/alpha) to hand out
	// per-tenant admins. Unlike managed API keys, UserPath alone never scopes
	// an extension identity, because a login identity is not a tenancy.
	AccessScope     string
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

// AuthenticationEvent is a security-audit record emitted by an extension
// authentication flow. Reason must be a short, stable machine identifier; it
// must not contain provider responses, tokens, claims, or other secrets.
type AuthenticationEvent struct {
	Timestamp   time.Time
	Type        string
	Outcome     string
	Method      string
	PrincipalID string
	UserPath    string
	RequestID   string
	ClientIP    string
	HTTPMethod  string
	Path        string
	UserAgent   string
	Reason      string
}

// AuthenticationEventRecorder persists authentication lifecycle events in
// Core's audit trail. Implementations must be safe for concurrent use and
// must not block the authentication flow on durable storage I/O.
type AuthenticationEventRecorder interface {
	RecordAuthenticationEvent(AuthenticationEvent)
}

// AuthenticationEventRecorderAware is optionally implemented by a request
// authenticator that emits login, rejection, and logout lifecycle events.
// Core installs the recorder after its audit subsystem has initialized.
type AuthenticationEventRecorderAware interface {
	SetAuthenticationEventRecorder(AuthenticationEventRecorder)
}

// WithAuthentication attaches an extension-established identity to a request
// context. Core calls this after accepting an authenticator result.
func WithAuthentication(ctx context.Context, authentication Authentication) context.Context {
	authentication.Labels = slices.Clone(authentication.Labels)
	return context.WithValue(ctx, authenticationContextKey{}, authentication)
}

// WithoutAuthentication returns a context that hides any extension identity
// inherited from an outer middleware. Core uses it at the explicit-credential
// boundary so a bearer token cannot retain an ambient cookie principal.
func WithoutAuthentication(ctx context.Context) context.Context {
	return context.WithValue(ctx, authenticationContextKey{}, clearedAuthentication{})
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
