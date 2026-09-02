package core

import (
	"context"
	"strings"
)

// accessScopeKey stores the credential-derived AccessScope for the request.
const accessScopeKey contextKey = "access-scope"

// AccessScope is the user-path subtree the authenticated credential may act
// on. It is derived from the credential alone (a managed key's or extension
// identity's bound user path), never from request headers, so it is the value
// ownership and admin-scoping checks must consult. The zero value is global:
// the master key, unauthenticated requests, and credentials without a bound
// user path can reach every user path, including rows that carry none.
type AccessScope struct {
	UserPath string
}

// Global reports whether the scope places no user-path restriction.
func (s AccessScope) Global() bool {
	return s.UserPath == "" || s.UserPath == "/"
}

// Allows reports whether userPath lies inside the scope: the scope root
// itself or any descendant. A non-global scope never admits an empty path, so
// rows written without a user path stay visible to global scopes only.
func (s AccessScope) Allows(userPath string) bool {
	if s.Global() {
		return true
	}
	return UserPathContains(s.UserPath, userPath)
}

// UserPathContains reports whether path equals base or descends from it.
// Root ("/") contains every non-empty path; an empty path is contained by
// nothing. Both values are trimmed but otherwise expected to be canonical.
func UserPathContains(base, path string) bool {
	base = strings.TrimSpace(base)
	path = strings.TrimSpace(path)
	if base == "" || path == "" {
		return false
	}
	if base == "/" {
		return true
	}
	return path == base || strings.HasPrefix(path, base+"/")
}

// WithAccessScope returns a context carrying the credential's access scope.
// The path is canonicalized; an unparseable path is kept verbatim so it can
// never widen to global by accident.
func WithAccessScope(ctx context.Context, scope AccessScope) context.Context {
	if normalized, err := NormalizeUserPath(scope.UserPath); err == nil {
		scope.UserPath = normalized
	}
	return context.WithValue(ctx, accessScopeKey, scope)
}

// AccessScopeFromContext returns the credential's access scope. A context
// without one is global.
func AccessScopeFromContext(ctx context.Context) AccessScope {
	if ctx == nil {
		return AccessScope{}
	}
	if scope, ok := ctx.Value(accessScopeKey).(AccessScope); ok {
		return scope
	}
	return AccessScope{}
}
