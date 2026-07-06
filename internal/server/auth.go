package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/authkeys"
	"gomodel/internal/core"
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
	userPathHeaderName := configuredUserPathHeaderName(userPathHeader...)
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			// If no auth mechanism is configured, allow all requests.
			if masterKey == "" && (authenticator == nil || !authenticator.Enabled()) {
				auditlog.EnrichEntryWithAuthMethod(c, auditlog.AuthMethodNoKey)
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

			// Get Authorization header
			authHeader := c.Request().Header.Get("Authorization")
			if authHeader == "" {
				authErr := authenticationError(c, "missing authorization header")
				return writeGatewayError(c, authErr)
			}

			// Extract Bearer token
			const prefix = "Bearer "
			if !strings.HasPrefix(authHeader, prefix) {
				authErr := authenticationError(c, "invalid authorization header format, expected 'Bearer <token>'")
				return writeGatewayError(c, authErr)
			}

			token := strings.TrimPrefix(authHeader, prefix)
			if masterKey != "" && subtle.ConstantTimeCompare([]byte(token), []byte(masterKey)) == 1 {
				auditlog.EnrichEntryWithAuthMethod(c, auditlog.AuthMethodMasterKey)
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

			authErr := authenticationError(c, "invalid master key")
			return writeGatewayError(c, authErr)
		}
	}
}

// applyAuthKeyResult enriches the request context and audit entry with the
// authenticated managed key's identity, labels, and bound user path.
func applyAuthKeyResult(c *echo.Context, authResult authkeys.AuthenticationResult, userPathHeaderName string) {
	ctx := core.WithAuthKeyID(c.Request().Context(), authResult.ID)
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
	auditlog.EnrichEntryWithAuthKeyID(c, authResult.ID)
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
