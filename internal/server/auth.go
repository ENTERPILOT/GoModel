package server

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
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
				return c.JSON(http.StatusUnauthorized, map[string]any{
					"error": map[string]any{
						"type":    "authentication_error",
						"message": "missing authorization header",
					},
				})
			}

			// Extract Bearer token
			const prefix = "Bearer "
			if !strings.HasPrefix(authHeader, prefix) {
				return c.JSON(http.StatusUnauthorized, map[string]any{
					"error": map[string]any{
						"type":    "authentication_error",
						"message": "invalid authorization header format, expected 'Bearer <token>'",
					},
				})
			}

			token := strings.TrimPrefix(authHeader, prefix)
			if subtle.ConstantTimeCompare([]byte(token), []byte(masterKey)) != 1 {
				return c.JSON(http.StatusUnauthorized, map[string]any{
					"error": map[string]any{
						"type":    "authentication_error",
						"message": "invalid master key",
					},
				})
			}

			// Authentication successful, proceed to next handler
			return next(c)
		}
	}
}
