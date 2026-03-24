package server

import (
	"crypto/subtle"
	"strings"

	"github.com/labstack/echo/v5"

	"gomodel/internal/core"
)

// AuthMiddleware creates an Echo middleware that validates the master key
// if it's configured. If masterKey is empty, no authentication is required.
// skipPaths is a list of paths that should bypass authentication.
func AuthMiddleware(masterKey string, skipPaths []string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			// If no master key is configured, allow all requests
			if masterKey == "" {
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
				authErr := core.NewAuthenticationError("", "missing authorization header")
				return c.JSON(authErr.HTTPStatusCode(), authErr.ToJSON())
			}

			// Extract Bearer token
			const prefix = "Bearer "
			if !strings.HasPrefix(authHeader, prefix) {
				authErr := core.NewAuthenticationError("", "invalid authorization header format, expected 'Bearer <token>'")
				return c.JSON(authErr.HTTPStatusCode(), authErr.ToJSON())
			}

			token := strings.TrimPrefix(authHeader, prefix)
			if subtle.ConstantTimeCompare([]byte(token), []byte(masterKey)) != 1 {
				authErr := core.NewAuthenticationError("", "invalid master key")
				return c.JSON(authErr.HTTPStatusCode(), authErr.ToJSON())
			}

			// Authentication successful, proceed to next handler
			return next(c)
		}
	}
}
