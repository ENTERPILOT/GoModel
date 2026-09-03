package admin

import (
	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/internal/core"
)

// Admin scoping. A credential bound to a user path (a managed key or an
// extension identity with a user_path) administers only that subtree. Global
// credentials (master key, keys without a user path) behave exactly as before.
//
// Two error codes tell the dashboard what happened:
//   - admin_scope_denied: the endpoint is gateway-wide and needs a global credential.
//   - user_path_out_of_scope: a path or filter the caller named lies outside its subtree.
//
// Objects addressed by an opaque ID that belong outside the scope are reported
// as not found, so a scoped caller learns nothing about other tenants' IDs.

const (
	codeAdminScopeDenied     = "admin_scope_denied"
	codeUserPathOutOfScope   = "user_path_out_of_scope"
	adminScopeDeniedMessage  = "this endpoint requires a global admin credential"
	userPathOutOfScopeFormat = " is outside the credential's user path scope"
)

// requestScope returns the access scope of the authenticated credential.
func requestScope(c *echo.Context) core.AccessScope {
	return core.AccessScopeFromContext(c.Request().Context())
}

// RequireGlobalScope rejects requests from user-path scoped credentials with
// 403 admin_scope_denied. Mount it on gateway-wide endpoints.
func RequireGlobalScope() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if !requestScope(c).Global() {
				return handleError(c, scopeDeniedError())
			}
			return next(c)
		}
	}
}

func scopeDeniedError() error {
	return core.NewPermissionError(adminScopeDeniedMessage).WithCode(codeAdminScopeDenied)
}

func userPathOutOfScopeError(fieldName string) error {
	return core.NewPermissionError(fieldName + userPathOutOfScopeFormat).WithCode(codeUserPathOutOfScope)
}

// scopedUserPath resolves a caller-supplied user path filter or target against
// the credential's scope. Global credentials get the normalized value as-is.
// For scoped credentials an empty value defaults to the scope root and a
// value outside the scope is rejected with 403 user_path_out_of_scope.
func scopedUserPath(c *echo.Context, fieldName, raw string) (string, error) {
	userPath, err := normalizeUserPathQueryParam(fieldName, raw)
	if err != nil {
		return "", err
	}
	scope := requestScope(c)
	if scope.Global() {
		return userPath, nil
	}
	if userPath == "" {
		return scope.UserPath, nil
	}
	if !scope.Allows(userPath) {
		return "", userPathOutOfScopeError(fieldName)
	}
	return userPath, nil
}

// requireScopedSubject checks a budget or rate-limit target: scoped
// credentials may only address user_path rules whose subject lies inside
// their subtree. Gateway-wide rules (labels, providers, models) are out of
// reach for them.
func requireScopedSubject(c *echo.Context, isUserPathScope bool, subject string) error {
	scope := requestScope(c)
	if scope.Global() {
		return nil
	}
	if !isUserPathScope || !scope.Allows(subject) {
		return userPathOutOfScopeError("subject")
	}
	return nil
}
