package admin

import (
	"strings"

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
	codeAdminScopeDenied   = "admin_scope_denied"
	codeUserPathOutOfScope = "user_path_out_of_scope"
	// Auth-key writes: a key may not edit itself, and a key limited to a
	// model or MCP allowlist may not issue keys outside it.
	codeAuthKeySelfUpdate       = "auth_key_self_update"
	codeAllowedModelsOutOfScope = "allowed_models_out_of_scope"
	codeAllowedMCPOutOfScope    = "allowed_mcp_out_of_scope"
	adminScopeDeniedMessage     = "this endpoint requires a global admin credential"
	userPathOutOfScopeFormat    = " is outside the credential's user path scope"
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

// scopedUserPaths resolves several user path filters against the
// credential's scope. It returns the scope root as scope (empty for global
// credentials), so storage keeps rows inside it even when no path is named,
// and the normalized paths, each of which must lie inside the scope.
func scopedUserPaths(c *echo.Context, fieldName string, raws []string) (scope string, paths []string, err error) {
	access := requestScope(c)
	if !access.Global() {
		scope = access.UserPath
	}
	for _, raw := range raws {
		userPath, err := normalizeUserPathQueryParam(fieldName, raw)
		if err != nil {
			return "", nil, err
		}
		if userPath == "" {
			continue
		}
		if !access.Global() && !access.Allows(userPath) {
			return "", nil, userPathOutOfScopeError(fieldName)
		}
		paths = append(paths, userPath)
	}
	return scope, paths, nil
}

// requireScopedSubject checks a budget or rate-limit target: scoped
// credentials may only address user_path rules whose subject lies strictly
// inside their subtree. The rule on the scope root itself is the limit placed
// on the credential's own tenant, so it stays with global credentials;
// otherwise a tenant's dashboard key could raise, reset, or delete its own
// budget. Gateway-wide rules (labels, providers, models) are out of reach too.
func requireScopedSubject(c *echo.Context, isUserPathScope bool, subject string) error {
	scope := requestScope(c)
	if scope.Global() {
		return nil
	}
	if !isUserPathScope || !scopeAllowsWrite(scope, subject) {
		return userPathOutOfScopeError("subject")
	}
	return nil
}

// scopeAllowsWrite reports whether a scoped credential may change the rule or
// node at userPath: a strict descendant of the scope root. Global scopes may
// write anywhere.
func scopeAllowsWrite(scope core.AccessScope, userPath string) bool {
	if scope.Global() {
		return true
	}
	return scope.Allows(userPath) && strings.TrimSpace(userPath) != scope.UserPath
}
