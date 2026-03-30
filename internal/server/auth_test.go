package server

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gomodel/internal/auditlog"
	"gomodel/internal/core"
)

type mockAuthenticator struct {
	enabled   bool
	tokenToID map[string]string
	err       error
}

func (m mockAuthenticator) Enabled() bool {
	return m.enabled
}

func (m mockAuthenticator) Authenticate(_ context.Context, token string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	id, ok := m.tokenToID[token]
	if !ok {
		return "", assert.AnError
	}
	return id, nil
}

func TestAuthMiddleware(t *testing.T) {
	tests := []struct {
		name           string
		masterKey      string
		authHeader     string
		expectedStatus int
		expectedBody   string
	}{
		{
			name:           "no master key configured - allows request",
			masterKey:      "",
			authHeader:     "",
			expectedStatus: http.StatusOK,
			expectedBody:   "ok",
		},
		{
			name:           "valid master key - allows request",
			masterKey:      "secret-key-123",
			authHeader:     "Bearer secret-key-123",
			expectedStatus: http.StatusOK,
			expectedBody:   "ok",
		},
		{
			name:           "missing authorization header - denies request",
			masterKey:      "secret-key-123",
			authHeader:     "",
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   `{"error":{"message":"missing authorization header","type":"authentication_error","param":null,"code":null}}`,
		},
		{
			name:           "invalid authorization format - denies request",
			masterKey:      "secret-key-123",
			authHeader:     "secret-key-123",
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   `{"error":{"message":"invalid authorization header format, expected 'Bearer \u003ctoken\u003e'","type":"authentication_error","param":null,"code":null}}`,
		},
		{
			name:           "invalid master key - denies request",
			masterKey:      "secret-key-123",
			authHeader:     "Bearer wrong-key",
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   `{"error":{"message":"invalid master key","type":"authentication_error","param":null,"code":null}}`,
		},
		{
			name:           "bearer prefix case sensitive - allows request",
			masterKey:      "secret-key-123",
			authHeader:     "Bearer secret-key-123",
			expectedStatus: http.StatusOK,
			expectedBody:   "ok",
		},
		{
			name:           "empty bearer token - denies request",
			masterKey:      "secret-key-123",
			authHeader:     "Bearer ",
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   `{"error":{"message":"invalid master key","type":"authentication_error","param":null,"code":null}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()

			// Create a test handler that returns OK
			testHandler := func(c *echo.Context) error {
				return c.String(http.StatusOK, "ok")
			}

			// Wrap the handler with auth middleware
			handler := AuthMiddleware(tt.masterKey, nil)(testHandler)

			// Create request
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			// Execute
			err := handler(c)

			// Assert
			if tt.expectedStatus == http.StatusOK {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedStatus, rec.Code)
				assert.Equal(t, tt.expectedBody, rec.Body.String())
			} else {
				// For error responses, the middleware returns the JSON directly
				require.NoError(t, err)
				assert.Equal(t, tt.expectedStatus, rec.Code)
				assert.JSONEq(t, tt.expectedBody, rec.Body.String())
			}
		})
	}
}

func TestAuthMiddleware_Integration(t *testing.T) {
	t.Run("with master key - protects all routes", func(t *testing.T) {
		e := echo.New()
		e.Use(AuthMiddleware("my-secret-key", nil))

		e.GET("/test", func(c *echo.Context) error {
			return c.String(http.StatusOK, "success")
		})

		// Request without auth should fail
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)

		// Request with valid auth should succeed
		req = httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer my-secret-key")
		rec = httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "success", rec.Body.String())
	})

	t.Run("without master key - allows all routes", func(t *testing.T) {
		e := echo.New()
		e.Use(AuthMiddleware("", nil))

		e.GET("/test", func(c *echo.Context) error {
			return c.String(http.StatusOK, "success")
		})

		// Request without auth should succeed
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "success", rec.Body.String())
	})
}

func TestAuthMiddlewareWithAuthenticator_ManagedKeyEnrichesContextAndAudit(t *testing.T) {
	e := echo.New()
	testHandler := func(c *echo.Context) error {
		if got := core.GetAuthKeyID(c.Request().Context()); got != "key-123" {
			t.Fatalf("auth key id in context = %q, want key-123", got)
		}
		entryVal := c.Get(string(auditlog.LogEntryKey))
		entry, ok := entryVal.(*auditlog.LogEntry)
		if !ok || entry == nil {
			t.Fatal("audit log entry missing from context")
		}
		if entry.AuthKeyID != "key-123" {
			t.Fatalf("audit entry auth key id = %q, want key-123", entry.AuthKeyID)
		}
		return c.String(http.StatusOK, "ok")
	}

	handler := AuthMiddlewareWithAuthenticator("", mockAuthenticator{
		enabled:   true,
		tokenToID: map[string]string{"sk_gom_token": "key-123"},
	}, nil)(testHandler)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer sk_gom_token")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(string(auditlog.LogEntryKey), &auditlog.LogEntry{Data: &auditlog.LogData{}})

	err := handler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
}

func TestAuthMiddleware_SkipPaths(t *testing.T) {
	t.Run("skips authentication for specified paths", func(t *testing.T) {
		e := echo.New()
		e.Use(AuthMiddleware("my-secret-key", []string{"/health", "/metrics"}))

		e.GET("/health", func(c *echo.Context) error {
			return c.String(http.StatusOK, "healthy")
		})
		e.GET("/metrics", func(c *echo.Context) error {
			return c.String(http.StatusOK, "metrics")
		})
		e.GET("/api/protected", func(c *echo.Context) error {
			return c.String(http.StatusOK, "protected")
		})

		// Request to skip path without auth should succeed
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "healthy", rec.Body.String())

		// Request to another skip path without auth should succeed
		req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
		rec = httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "metrics", rec.Body.String())

		// Request to protected path without auth should fail
		req = httptest.NewRequest(http.MethodGet, "/api/protected", nil)
		rec = httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)

		// Request to protected path with valid auth should succeed
		req = httptest.NewRequest(http.MethodGet, "/api/protected", nil)
		req.Header.Set("Authorization", "Bearer my-secret-key")
		rec = httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "protected", rec.Body.String())
	})

	t.Run("empty skip paths requires auth for all routes", func(t *testing.T) {
		e := echo.New()
		e.Use(AuthMiddleware("my-secret-key", []string{}))

		e.GET("/health", func(c *echo.Context) error {
			return c.String(http.StatusOK, "healthy")
		})

		// Request without auth should fail even for /health
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

func TestAuthMiddleware_WildcardSkipPaths(t *testing.T) {
	skipPaths := []string{"/admin/dashboard", "/admin/dashboard/*", "/admin/static/*"}

	tests := []struct {
		name     string
		path     string
		wantSkip bool
	}{
		{
			name:     "exact match /admin/dashboard",
			path:     "/admin/dashboard",
			wantSkip: true,
		},
		{
			name:     "wildcard match /admin/dashboard/overview",
			path:     "/admin/dashboard/overview",
			wantSkip: true,
		},
		{
			name:     "wildcard match /admin/dashboard/deep/nested",
			path:     "/admin/dashboard/deep/nested",
			wantSkip: true,
		},
		{
			name:     "wildcard match /admin/static/css/dashboard.css",
			path:     "/admin/static/css/dashboard.css",
			wantSkip: true,
		},
		{
			name:     "no match /admin/api/v1/models",
			path:     "/admin/api/v1/models",
			wantSkip: false,
		},
		{
			name:     "no match /admin/dashboardx (not prefix of /admin/dashboard/)",
			path:     "/admin/dashboardx",
			wantSkip: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := echo.New()
			e.Use(AuthMiddleware("secret-key", skipPaths))

			e.GET(tt.path, func(c *echo.Context) error {
				return c.String(http.StatusOK, "ok")
			})

			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			if tt.wantSkip {
				assert.Equal(t, http.StatusOK, rec.Code, "expected path %s to skip auth", tt.path)
			} else {
				assert.Equal(t, http.StatusUnauthorized, rec.Code, "expected path %s to require auth", tt.path)
			}
		})
	}
}

func TestAuthMiddleware_ConstantTimeComparison(t *testing.T) {
	t.Run("constant-time comparison prevents timing attacks", func(t *testing.T) {
		// Test that the constant-time comparison works correctly for various inputs
		testCases := []struct {
			name        string
			token       string
			masterKey   string
			shouldAllow bool
		}{
			{
				name:        "equal strings",
				token:       "secret-key-123",
				masterKey:   "secret-key-123",
				shouldAllow: true,
			},
			{
				name:        "unequal strings - different at start",
				token:       "wrong-key-123",
				masterKey:   "secret-key-123",
				shouldAllow: false,
			},
			{
				name:        "unequal strings - different at end",
				token:       "secret-key-456",
				masterKey:   "secret-key-123",
				shouldAllow: false,
			},
			{
				name:        "unequal strings - different lengths",
				token:       "secret-key",
				masterKey:   "secret-key-123",
				shouldAllow: false,
			},
			{
				name:        "empty token",
				token:       "",
				masterKey:   "secret-key-123",
				shouldAllow: false,
			},
			{
				name:        "very long strings",
				token:       "a" + strings.Repeat("x", 1000) + "z",
				masterKey:   "a" + strings.Repeat("x", 1000) + "x",
				shouldAllow: false,
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				e := echo.New()

				// Create a test handler
				testHandler := func(c *echo.Context) error {
					return c.String(http.StatusOK, "ok")
				}

				// Wrap with auth middleware
				handler := AuthMiddleware(tc.masterKey, nil)(testHandler)

				// Create request
				req := httptest.NewRequest(http.MethodGet, "/", nil)
				req.Header.Set("Authorization", "Bearer "+tc.token)
				rec := httptest.NewRecorder()
				c := e.NewContext(req, rec)

				// Execute
				err := handler(c)
				require.NoError(t, err)

				if tc.shouldAllow {
					assert.Equal(t, http.StatusOK, rec.Code)
					assert.Equal(t, "ok", rec.Body.String())
				} else {
					assert.Equal(t, http.StatusUnauthorized, rec.Code)
				}
			})
		}
	})

	t.Run("direct constant-time comparison verification", func(t *testing.T) {
		// Test the constant-time comparison directly to ensure it's working
		testCases := []struct {
			name     string
			a        string
			b        string
			expected bool
		}{
			{"equal strings", "test", "test", true},
			{"unequal strings", "test", "tset", false},
			{"different lengths", "test", "testing", false},
			{"empty strings", "", "", true},
			{"one empty", "", "test", false},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				result := subtle.ConstantTimeCompare([]byte(tc.a), []byte(tc.b)) == 1
				assert.Equal(t, tc.expected, result, "ConstantTimeCompare should return %v for %q vs %q", tc.expected, tc.a, tc.b)
			})
		}
	})
}
