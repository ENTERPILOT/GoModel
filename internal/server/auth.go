package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"reflect"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/authkeys"
	"github.com/enterpilot/gomodel/internal/core"
)

// BearerTokenAuthenticator authenticates managed bearer tokens and returns
// their internal auth key metadata on success.
type BearerTokenAuthenticator interface {
	Enabled() bool
	Authenticate(ctx context.Context, token string) (authkeys.AuthenticationResult, error)
}

// AuthMiddlewareWithAuthenticator creates an Echo middleware that validates
// the legacy master key and, when configured, managed auth keys from the auth
// key service. If no auth mechanism is configured, no authentication is
// required. skipPaths is a list of paths that should bypass authentication.
func AuthMiddlewareWithAuthenticator(masterKey string, authenticator BearerTokenAuthenticator, skipPaths []string, userPathHeader ...string) echo.MiddlewareFunc {
	return AuthMiddlewareWithRequestAuthenticators(masterKey, authenticator, nil, skipPaths, userPathHeader...)
}

// AuthMiddlewareWithRequestAuthenticators additionally accepts extension
// authenticators such as OIDC sessions. Explicit bearer credentials always
// take precedence over ambient request credentials such as cookies.
func AuthMiddlewareWithRequestAuthenticators(masterKey string, authenticator BearerTokenAuthenticator, requestAuthenticators []ext.RequestAuthenticator, skipPaths []string, userPathHeader ...string) echo.MiddlewareFunc {
	userPathHeaderName := configuredUserPathHeaderName(userPathHeader...)
	hasRequestAuthenticator := hasRequestAuthenticators(requestAuthenticators)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			// If no auth mechanism is configured, allow all requests.
			if masterKey == "" && (authenticator == nil || !authenticator.Enabled()) && !hasRequestAuthenticator {
				auditlog.EnrichEntryWithAuthMethod(c, auditlog.AuthMethodNoKey)
				setInteractionContinuationAllowed(c, true)
				return next(c)
			}

			// Check if path should skip authentication.
			// Paths ending with "/*" are treated as prefix matches.
			requestPath := c.Request().URL.Path
			for _, skipPath := range skipPaths {
				if strings.HasSuffix(skipPath, "/*") {
					prefix := strings.TrimSuffix(skipPath, "*")
					if strings.HasPrefix(requestPath, prefix) {
						auditlog.EnrichEntryWithAuthMethod(c, auditlog.AuthMethodNoKey)
						return next(c)
					}
				} else if requestPath == skipPath {
					auditlog.EnrichEntryWithAuthMethod(c, auditlog.AuthMethodNoKey)
					return next(c)
				}
			}

			token, tokenErr := requestAuthToken(c.Request())
			hasExplicitCredential := c.Request().Header.Get("Authorization") != "" || c.Request().Header.Get("x-api-key") != ""
			if hasExplicitCredential {
				// Explicit credentials take precedence over ambient extension
				// sessions. Hide every identity value installed by outer extension
				// middleware before validating the selected credential; clearing only
				// the response header would leave downstream context consumers scoped
				// to the wrong principal.
				setAuthenticationUserHeader(c, "")
				ctx := ext.WithoutAuthentication(c.Request().Context())
				ctx = core.WithEffectiveUserPath(ctx, "")
				ctx = core.WithCredentialAllowedModels(ctx, nil)
				ctx = core.WithAccessScope(ctx, core.AccessScope{})
				c.SetRequest(c.Request().WithContext(ctx))
				if tokenErr != "" {
					authErr := authenticationError(c, tokenErr)
					return writeGatewayError(c, authErr)
				}
				if masterKey != "" && subtle.ConstantTimeCompare([]byte(token), []byte(masterKey)) == 1 {
					auditlog.EnrichEntryWithAuthMethod(c, auditlog.AuthMethodMasterKey)
					setInteractionContinuationAllowed(c, true)
					return next(c)
				}

				if authenticator != nil && authenticator.Enabled() {
					auditlog.EnrichEntryWithAuthMethod(c, auditlog.AuthMethodAPIKey)
					authResult, err := authenticator.Authenticate(c.Request().Context(), token)
					if err == nil {
						applyAuthKeyResult(c, authResult, userPathHeaderName)
						return next(c)
					}

					authErr := authenticationErrorWithAudit(c, authFailureMessage(err), "authentication failed")
					return writeGatewayError(c, authErr)
				}
				message := "invalid credentials"
				if masterKey != "" && (authenticator == nil || !authenticator.Enabled()) {
					message = "invalid master key"
				}
				return writeGatewayError(c, authenticationError(c, message))
			}

			for _, requestAuthenticator := range requestAuthenticators {
				if requestAuthenticatorIsNil(requestAuthenticator) {
					continue
				}
				result, err := requestAuthenticator.AuthenticateRequest(c.Request().Context(), c.Request())
				if err != nil {
					return writeGatewayError(c, extensionAuthenticationError(c))
				}
				if result == nil {
					continue
				}
				if err := applyExtensionAuthResult(c, result, userPathHeaderName); err != nil {
					return writeGatewayError(c, extensionAuthenticationError(c))
				}
				return next(c)
			}

			authErr := authenticationError(c, tokenErr)
			return writeGatewayError(c, authErr)
		}
	}
}

func hasRequestAuthenticators(authenticators []ext.RequestAuthenticator) bool {
	for _, authenticator := range authenticators {
		if !requestAuthenticatorIsNil(authenticator) {
			return true
		}
	}
	return false
}

func requestAuthenticatorIsNil(authenticator ext.RequestAuthenticator) bool {
	if authenticator == nil {
		return true
	}
	value := reflect.ValueOf(authenticator)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func applyExtensionAuthResult(c *echo.Context, result *ext.Authentication, userPathHeaderName string) error {
	if result == nil {
		return errors.New("extension authenticator returned no identity")
	}
	principalID := strings.TrimSpace(result.PrincipalID)
	if principalID == "" {
		return errors.New("extension authenticator returned an empty principal id")
	}
	userPath, err := core.NormalizeUserPath(result.UserPath)
	if err != nil {
		return err
	}
	accessScope, err := core.NormalizeUserPath(result.AccessScope)
	if err != nil {
		return err
	}
	normalized := *result
	normalized.PrincipalID = principalID
	normalized.UserPath = userPath
	normalized.AccessScope = accessScope
	normalized.Method = auditlog.NormalizeAuthMethod(result.Method)
	if normalized.Method == "" {
		normalized.Method = auditlog.AuthMethodExtension
	}
	ctx := context.WithValue(c.Request().Context(), managedDashboardAccessKey{}, normalized.DashboardAccess)
	ctx = context.WithValue(ctx, interactionContinuationAllowedKey{}, normalized.DashboardAccess)
	ctx = ext.WithAuthentication(ctx, normalized)
	// Only an explicit access scope confines an extension identity; its user
	// path is a login identity used for attribution, not a tenancy. The
	// request header never widens the scope either way.
	ctx = core.WithAccessScope(ctx, core.AccessScope{UserPath: accessScope})
	if len(normalized.Labels) > 0 {
		ctx = core.WithRequestLabels(ctx, core.MergeLabels(core.RequestLabelsFromContext(ctx), normalized.Labels))
	}
	if userPath != "" {
		ctx = core.WithEffectiveUserPath(ctx, userPath)
		ctx = core.WithUserPathHeaderName(ctx, userPathHeaderName)
		if snapshot := core.GetRequestSnapshot(ctx); snapshot != nil {
			ctx = core.WithRequestSnapshot(ctx, snapshot.WithUserPathHeader(userPath, userPathHeaderName))
		}
		c.Request().Header.Set(userPathHeaderName, userPath)
		auditlog.EnrichEntryWithUserPath(c, userPath)
	}
	c.SetRequest(c.Request().WithContext(ctx))
	setAuthenticationUserHeader(c, userPath)
	auditlog.EnrichEntryWithAuthMethod(c, normalized.Method)
	auditlog.EnrichEntryWithPrincipalID(c, normalized.PrincipalID)
	return nil
}

// AdminAccessMiddleware denies admin API requests authenticated with a
// managed key or extension identity that lacks dashboard access. Master-key
// requests and requests that skipped authentication pass through unchanged.
func AdminAccessMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if allowed, isManagedKey := managedDashboardAccess(c.Request().Context()); isManagedKey && !allowed {
				const message = "identity does not have dashboard access"
				auditlog.EnrichEntryWithError(c, string(core.ErrorTypeAuthentication), message)
				gatewayErr := (&core.GatewayError{
					Type:       core.ErrorTypeAuthentication,
					Message:    message,
					StatusCode: http.StatusForbidden,
				}).WithCode("dashboard_access_denied")
				return writeGatewayError(c, gatewayErr)
			}
			return next(c)
		}
	}
}

func extensionAuthenticationError(c *echo.Context) *core.GatewayError {
	const (
		message = "authentication failed"
		code    = "extension_authentication_failed"
	)
	auditlog.EnrichEntryWithError(c, string(core.ErrorTypeAuthentication), message, code)
	return core.NewAuthenticationError("", message).WithCode(code)
}

// requestAuthToken extracts the caller's credential from the request. The
// primary scheme is "Authorization: Bearer <token>"; the Anthropic-native
// "x-api-key: <token>" header is accepted as a fallback so Anthropic SDK
// clients work without switching their auth configuration. A non-empty
// errMessage describes why no token could be extracted.
func requestAuthToken(r *http.Request) (token, errMessage string) {
	if authHeader := r.Header.Get("Authorization"); authHeader != "" {
		const prefix = "Bearer "
		if !strings.HasPrefix(authHeader, prefix) {
			return "", "invalid authorization header format, expected 'Bearer <token>'"
		}
		return strings.TrimPrefix(authHeader, prefix), ""
	}
	if apiKey := r.Header.Get("x-api-key"); apiKey != "" {
		return apiKey, ""
	}
	return "", "missing credentials: send 'Authorization: Bearer <token>' or 'x-api-key: <token>'"
}

// managedDashboardAccessKey marks requests authenticated with a managed key.
// Its bool value is the key's dashboard access. Requests authenticated with
// the master key (or with auth skipped) never carry it, so they are not
// subject to the admin gate.
type managedDashboardAccessKey struct{}
type interactionContinuationAllowedKey struct{}

// managedDashboardAccess reports whether the request was authenticated with a
// managed key and, if so, whether that key grants admin API and dashboard
// access.
func managedDashboardAccess(ctx context.Context) (allowed, isManagedKey bool) {
	allowed, isManagedKey = ctx.Value(managedDashboardAccessKey{}).(bool)
	return allowed, isManagedKey
}

func setInteractionContinuationAllowed(c *echo.Context, allowed bool) {
	if c == nil {
		return
	}
	req := c.Request()
	c.SetRequest(req.WithContext(context.WithValue(req.Context(), interactionContinuationAllowedKey{}, allowed)))
}

func interactionContinuationAllowed(ctx context.Context) bool {
	allowed, _ := ctx.Value(interactionContinuationAllowedKey{}).(bool)
	return allowed
}

// applyAuthKeyResult enriches the request context and audit entry with the
// authenticated managed key's identity, labels, and bound user path.
func applyAuthKeyResult(c *echo.Context, authResult authkeys.AuthenticationResult, userPathHeaderName string) {
	ctx := core.WithAuthKeyID(c.Request().Context(), authResult.ID)
	// The key's bound user path is the subtree it may administer and the
	// objects it may address; a key without one stays global.
	ctx = core.WithAccessScope(ctx, core.AccessScope{UserPath: strings.TrimSpace(authResult.UserPath)})
	ctx = context.WithValue(ctx, managedDashboardAccessKey{}, authResult.DashboardAccess)
	ctx = context.WithValue(ctx, interactionContinuationAllowedKey{}, authResult.DashboardAccess)
	if len(authResult.AllowedModels) > 0 {
		ctx = core.WithCredentialAllowedModels(ctx, authResult.AllowedModels)
	}
	if len(authResult.Labels) > 0 {
		// Key labels join any labels the tagging middleware already
		// extracted from request headers; duplicates collapse.
		ctx = core.WithRequestLabels(ctx, core.MergeLabels(core.RequestLabelsFromContext(ctx), authResult.Labels))
	}
	if userPath := strings.TrimSpace(authResult.UserPath); userPath != "" {
		ctx = core.WithEffectiveUserPath(ctx, userPath)
		ctx = core.WithUserPathHeaderName(ctx, userPathHeaderName)
		if snapshot := core.GetRequestSnapshot(ctx); snapshot != nil {
			ctx = core.WithRequestSnapshot(ctx, snapshot.WithUserPathHeader(userPath, userPathHeaderName))
		}
		c.Request().Header.Set(userPathHeaderName, userPath)
		auditlog.EnrichEntryWithUserPath(c, userPath)
	}
	c.SetRequest(c.Request().WithContext(ctx))
	setAuthenticationUserHeader(c, strings.TrimSpace(authResult.UserPath))
	auditlog.EnrichEntryWithAuthKeyID(c, authResult.ID)
}

func setAuthenticationUserHeader(c *echo.Context, userPath string) {
	if c == nil {
		return
	}
	c.Response().Header().Set(ext.AuthenticationUserHeader, strings.TrimSpace(userPath))
}

func authFailureMessage(err error) string {
	if err == nil {
		return "invalid API key"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "authentication unavailable"
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return "invalid API key"
	}
	return message
}

func authenticationError(c *echo.Context, message string) *core.GatewayError {
	auditlog.EnrichEntryWithError(c, string(core.ErrorTypeAuthentication), message)
	return core.NewAuthenticationError("", message)
}

func authenticationErrorWithAudit(c *echo.Context, auditMessage, responseMessage string) *core.GatewayError {
	auditlog.EnrichEntryWithError(c, string(core.ErrorTypeAuthentication), auditMessage)
	return core.NewAuthenticationError("", responseMessage)
}
