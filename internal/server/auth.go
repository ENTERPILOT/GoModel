package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

// BearerTokenAuthenticator authenticates managed bearer tokens and returns
// their internal auth key id on success.
type BearerTokenAuthenticator interface {
	Enabled() bool
	Authenticate(ctx context.Context, token string) (string, error)
}

// AuthMiddleware creates an Echo middleware that validates the master key
// if it's configured. If masterKey is empty, no authentication is required.
// skipPaths is a list of paths that should bypass authentication.
func AuthMiddleware(masterKey string, skipPaths []string) echo.MiddlewareFunc {
	return AuthMiddlewareWithAuthenticator(masterKey, nil, skipPaths)
}

// AuthMiddlewareWithAuthenticator validates the legacy master key and, when
// configured, managed auth keys from the auth key service.
func AuthMiddlewareWithAuthenticator(masterKey string, authenticator BearerTokenAuthenticator, skipPaths []string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			// If no auth mechanism is configured, allow all requests.
			if masterKey == "" && (authenticator == nil || !authenticator.Enabled()) {
				return next(c)
			}

			// Check if path should skip authentication.
			// Paths ending with "/*" are treated as prefix matches.
			requestPath := c.Request().URL.Path
			for _, skipPath := range skipPaths {
				if strings.HasSuffix(skipPath, "/*") {
					prefix := strings.TrimSuffix(skipPath, "*")
					if strings.HasPrefix(requestPath, prefix) {
						return next(c)
					}
				} else if requestPath == skipPath {
					return next(c)
				}
			}

			// Get Authorization header
			authHeader := c.Request().Header.Get("Authorization")
			if authHeader == "" {
				authErr := authenticationError(c, "missing authorization header")
				return c.JSON(authErr.HTTPStatusCode(), authErr.ToJSON())
			}

			// Extract Bearer token
			const prefix = "Bearer "
			if !strings.HasPrefix(authHeader, prefix) {
				authErr := authenticationError(c, "invalid authorization header format, expected 'Bearer <token>'")
				return c.JSON(authErr.HTTPStatusCode(), authErr.ToJSON())
			}

			token := strings.TrimPrefix(authHeader, prefix)
			if masterKey != "" && subtle.ConstantTimeCompare([]byte(token), []byte(masterKey)) == 1 {
				return next(c)
			}

			if authenticator != nil && authenticator.Enabled() {
				authKeyID, err := authenticator.Authenticate(c.Request().Context(), token)
				if err == nil {
					ctx := core.WithAuthKeyID(c.Request().Context(), authKeyID)
					c.SetRequest(c.Request().WithContext(ctx))
					auditlog.EnrichEntryWithAuthKeyID(c, authKeyID)
					return next(c)
				}

				authErr := authenticationErrorWithAudit(c, authFailureMessage(err), "authentication failed")
				return c.JSON(authErr.HTTPStatusCode(), authErr.ToJSON())
			}

			authErr := authenticationError(c, "invalid master key")
			return c.JSON(authErr.HTTPStatusCode(), authErr.ToJSON())
		}
	}
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
