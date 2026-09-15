package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/authkeys"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
)

type mockRequestAuthenticator struct {
	result *ext.Authentication
	err    error
	calls  int
}

func (m *mockRequestAuthenticator) Name() string { return "mock-request-auth" }

func (m *mockRequestAuthenticator) AuthenticateRequest(_ context.Context, _ *http.Request) (*ext.Authentication, error) {
	m.calls++
	return m.result, m.err
}

type mockAuthenticator struct {
	enabled        bool
	tokenToID      map[string]string
	tokenPath      map[string]string
	tokenLabels    map[string][]string
	tokenAllowed   map[string][]string
	tokenDashboard map[string]bool
	err            error
}

func (m mockAuthenticator) Enabled() bool {
	return m.enabled
}

func (m mockAuthenticator) Authenticate(_ context.Context, token string) (authkeys.AuthenticationResult, error) {
	if m.err != nil {
		return authkeys.AuthenticationResult{}, m.err
	}
	id, ok := m.tokenToID[token]
	if !ok {
		return authkeys.AuthenticationResult{}, assert.AnError
	}
	return authkeys.AuthenticationResult{
		ID:              id,
		UserPath:        m.tokenPath[token],
		Labels:          m.tokenLabels[token],
		AllowedModels:   m.tokenAllowed[token],
		DashboardAccess: m.tokenDashboard[token],
	}, nil
}

func TestAuthMiddlewareWithAuthenticator_ManagedKeyAllowedModelsReachContext(t *testing.T) {
	testHandler := func(c *echo.Context) error {
		got := core.GetCredentialAllowedModels(c.Request().Context())
		assert.Equal(t, []string{"anthropic/", "openai/gpt-4o"}, got)
		return c.String(http.StatusOK, "ok")
	}
	handler := AuthMiddlewareWithAuthenticator("", mockAuthenticator{
		enabled:      true,
		tokenToID:    map[string]string{"sk_gom_token": "key-123"},
		tokenAllowed: map[string][]string{"sk_gom_token": {"anthropic/", "openai/gpt-4o"}},
	}, nil)(testHandler)

	// An outer layer must never leak an allowlist into a request authenticated
	// by an explicit credential without one.
	c, rec := echotest.Get(t, "/", echotest.WithHeader("Authorization", "Bearer sk_gom_token"))
	c.SetRequest(c.Request().WithContext(core.WithCredentialAllowedModels(c.Request().Context(), []string{"stale/"})))
	require.NoError(t, handler(c))
	assert.Equal(t, http.StatusOK, rec.Code)

	unrestricted := func(c *echo.Context) error {
		assert.Nil(t, core.GetCredentialAllowedModels(c.Request().Context()))
		return c.String(http.StatusOK, "ok")
	}
	handler = AuthMiddlewareWithAuthenticator("", mockAuthenticator{
		enabled:   true,
		tokenToID: map[string]string{"sk_gom_token": "key-123"},
	}, nil)(unrestricted)
	c, rec = echotest.Get(t, "/", echotest.WithHeader("Authorization", "Bearer sk_gom_token"))
	c.SetRequest(c.Request().WithContext(core.WithCredentialAllowedModels(c.Request().Context(), []string{"stale/"})))
	require.NoError(t, handler(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuthMiddleware(t *testing.T) {
	tests := []struct {
		name           string
		masterKey      string
		authHeader     string
		apiKeyHeader   string
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
			name:           "missing credentials - denies request",
			masterKey:      "secret-key-123",
			authHeader:     "",
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   `{"error":{"message":"missing credentials: send 'Authorization: Bearer <token>' or 'x-api-key: <token>'","type":"authentication_error","param":null,"code":null}}`,
		},
		{
			name:           "valid x-api-key - allows request",
			masterKey:      "secret-key-123",
			apiKeyHeader:   "secret-key-123",
			expectedStatus: http.StatusOK,
			expectedBody:   "ok",
		},
		{
			name:           "invalid x-api-key - denies request",
			masterKey:      "secret-key-123",
			apiKeyHeader:   "wrong-key",
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   `{"error":{"message":"invalid master key","type":"authentication_error","param":null,"code":null}}`,
		},
		{
			name:           "authorization header takes precedence over x-api-key",
			masterKey:      "secret-key-123",
			authHeader:     "Bearer wrong-key",
			apiKeyHeader:   "secret-key-123",
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   `{"error":{"message":"invalid master key","type":"authentication_error","param":null,"code":null}}`,
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
			name:           "empty bearer token - denies request",
			masterKey:      "secret-key-123",
			authHeader:     "Bearer ",
			expectedStatus: http.StatusUnauthorized,
			expectedBody:   `{"error":{"message":"invalid master key","type":"authentication_error","param":null,"code":null}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a test handler that returns OK
			testHandler := func(c *echo.Context) error {
				return c.String(http.StatusOK, "ok")
			}

			// Wrap the handler with auth middleware
			handler := AuthMiddleware(tt.masterKey, nil)(testHandler)

			var opts []echotest.Option
			if tt.authHeader != "" {
				opts = append(opts, echotest.WithHeader("Authorization", tt.authHeader))
			}
			if tt.apiKeyHeader != "" {
				opts = append(opts, echotest.WithHeader("x-api-key", tt.apiKeyHeader))
			}
			c, rec := echotest.Get(t, "/", opts...)

			require.NoError(t, handler(c))
			assert.Equal(t, tt.expectedStatus, rec.Code)
			if tt.expectedStatus == http.StatusOK {
				assert.Equal(t, tt.expectedBody, rec.Body.String())
			} else {
				// For error responses, the middleware writes the JSON directly.
				assert.JSONEq(t, tt.expectedBody, rec.Body.String())
			}
		})
	}
}

func TestAuthMiddlewareWithAuthenticator_ManagedKeyEnrichesContextAndAudit(t *testing.T) {
	testHandler := func(c *echo.Context) error {
		got := core.GetAuthKeyID(c.Request().Context())
		require.Equal(t, "key-123", got)

		entryVal := c.Get(string(auditlog.LogEntryKey))
		entry, ok := entryVal.(*auditlog.LogEntry)
		require.True(t, ok)
		require.NotNil(t, entry)
		require.Equal(t, "key-123", entry.AuthKeyID)
		require.Equal(t, auditlog.AuthMethodAPIKey, entry.AuthMethod)

		return c.String(http.StatusOK, "ok")
	}

	handler := AuthMiddlewareWithAuthenticator("", mockAuthenticator{
		enabled:   true,
		tokenToID: map[string]string{"sk_gom_token": "key-123"},
	}, nil)(testHandler)

	c, rec := echotest.Get(t, "/",
		echotest.WithHeader("Authorization", "Bearer sk_gom_token"),
		echotest.WithValue(string(auditlog.LogEntryKey), &auditlog.LogEntry{Data: &auditlog.LogData{}}),
	)

	err := handler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
}

func TestAuthMiddlewareWithRequestAuthenticatorEnrichesRequest(t *testing.T) {
	requestAuth := &mockRequestAuthenticator{result: &ext.Authentication{
		PrincipalID:     "  oidc:principal-1  ",
		UserPath:        " /users/person@example.com/ ",
		Labels:          []string{"sso"},
		DashboardAccess: true,
		Method:          " OIDC ",
	}}
	handler := RequestSnapshotCapture()(AuthMiddlewareWithRequestAuthenticators(
		"", nil, []ext.RequestAuthenticator{requestAuth}, nil,
	)(func(c *echo.Context) error {
		assert.Equal(t, "/users/person@example.com", core.UserPathFromContext(c.Request().Context()))
		assert.Equal(t, []string{"sso"}, core.RequestLabelsFromContext(c.Request().Context()))
		assert.True(t, interactionContinuationAllowed(c.Request().Context()))
		identity, ok := ext.AuthenticationFromContext(c.Request().Context())
		assert.True(t, ok)
		assert.Equal(t, "oidc:principal-1", identity.PrincipalID)
		assert.Equal(t, "/users/person@example.com", identity.UserPath)
		assert.Equal(t, "oidc", identity.Method)
		entry := c.Get(string(auditlog.LogEntryKey)).(*auditlog.LogEntry)
		assert.Equal(t, "oidc", entry.AuthMethod)
		assert.Equal(t, "oidc:principal-1", entry.PrincipalID)
		assert.Equal(t, "/users/person@example.com", entry.UserPath)
		return c.NoContent(http.StatusNoContent)
	}))

	c, rec := echotest.Get(t, "/admin/usage")
	c.Set(string(auditlog.LogEntryKey), &auditlog.LogEntry{Data: &auditlog.LogData{}})
	require.NoError(t, handler(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "/users/person@example.com", rec.Header().Get(ext.AuthenticationUserHeader))
	assert.Equal(t, 1, requestAuth.calls)
}

func TestAuthMiddlewareWithRequestAuthenticatorsFailurePaths(t *testing.T) {
	secretError := errors.New("provider rejected cookie=super-secret")
	tests := []struct {
		name                 string
		auths                []*mockRequestAuthenticator
		adminGate            bool
		wantStatus           int
		wantCalls            []int
		wantExtensionFailure bool
	}{
		{
			name:       "authenticator error is sanitized",
			auths:      []*mockRequestAuthenticator{{err: secretError}},
			wantStatus: http.StatusUnauthorized, wantCalls: []int{1}, wantExtensionFailure: true,
		},
		{
			name: "nil result falls through to next authenticator",
			auths: []*mockRequestAuthenticator{{}, {result: &ext.Authentication{
				PrincipalID: "principal-1", UserPath: "/users/one", DashboardAccess: true, Method: "saml",
			}}},
			wantStatus: http.StatusNoContent, wantCalls: []int{1, 1},
		},
		{
			name:       "all nil results reject missing credentials",
			auths:      []*mockRequestAuthenticator{{}, {}},
			wantStatus: http.StatusUnauthorized, wantCalls: []int{1, 1},
		},
		{
			name:       "empty principal is rejected",
			auths:      []*mockRequestAuthenticator{{result: &ext.Authentication{PrincipalID: "  ", UserPath: "/users/one"}}},
			wantStatus: http.StatusUnauthorized, wantCalls: []int{1}, wantExtensionFailure: true,
		},
		{
			name:       "invalid user path is rejected",
			auths:      []*mockRequestAuthenticator{{result: &ext.Authentication{PrincipalID: "principal-1", UserPath: "/users/../admin"}}},
			wantStatus: http.StatusUnauthorized, wantCalls: []int{1}, wantExtensionFailure: true,
		},
		{
			name:      "identity without dashboard access is forbidden",
			auths:     []*mockRequestAuthenticator{{result: &ext.Authentication{PrincipalID: "principal-1", UserPath: "/users/one"}}},
			adminGate: true, wantStatus: http.StatusForbidden, wantCalls: []int{1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			authenticators := make([]ext.RequestAuthenticator, len(tt.auths))
			for i, authenticator := range tt.auths {
				authenticators[i] = authenticator
			}
			next := echo.HandlerFunc(func(c *echo.Context) error { return c.NoContent(http.StatusNoContent) })
			if tt.adminGate {
				next = AdminAccessMiddleware()(next)
			}
			handler := AuthMiddlewareWithRequestAuthenticators("", nil, authenticators, nil)(next)
			c, rec := echotest.Get(t, "/admin/auth-keys")
			entry := &auditlog.LogEntry{Data: &auditlog.LogData{}}
			c.Set(string(auditlog.LogEntryKey), entry)
			require.NoError(t, handler(c))
			assert.Equal(t, tt.wantStatus, rec.Code)
			for i, wantCalls := range tt.wantCalls {
				assert.Equal(t, wantCalls, tt.auths[i].calls)
			}
			assert.NotContains(t, rec.Body.String(), "super-secret")
			assert.NotContains(t, entry.Data.ErrorMessage, "super-secret")
			if tt.wantExtensionFailure {
				assert.Equal(t, "authentication failed", entry.Data.ErrorMessage)
				assert.Equal(t, "extension_authentication_failed", entry.Data.ErrorCode)
			}
			if tt.adminGate {
				assert.Equal(t, auditlog.AuthMethodExtension, entry.AuthMethod)
			}
		})
	}
}

func TestAuthMiddlewareWithOnlyNilRequestAuthenticatorsStaysDisabled(t *testing.T) {
	var typedNil *mockRequestAuthenticator
	tests := []struct {
		name  string
		auths []ext.RequestAuthenticator
	}{
		{name: "nil interface", auths: []ext.RequestAuthenticator{nil}},
		{name: "typed nil pointer", auths: []ext.RequestAuthenticator{typedNil}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := AuthMiddlewareWithRequestAuthenticators("", nil, tt.auths, nil)(func(c *echo.Context) error {
				return c.NoContent(http.StatusNoContent)
			})
			c, rec := echotest.Get(t, "/")
			require.NoError(t, handler(c))
			assert.Equal(t, http.StatusNoContent, rec.Code)
		})
	}
}

func TestAuthMiddlewareExplicitBearerPrecedesRequestAuthenticator(t *testing.T) {
	requestAuth := &mockRequestAuthenticator{result: &ext.Authentication{
		PrincipalID: "oidc:principal-1", UserPath: "/users/sso", DashboardAccess: true,
	}}
	handler := AuthMiddlewareWithRequestAuthenticators(
		"master", nil, []ext.RequestAuthenticator{requestAuth}, nil,
	)(func(c *echo.Context) error {
		assert.Empty(t, core.UserPathFromContext(c.Request().Context()))
		_, inherited := ext.AuthenticationFromContext(c.Request().Context())
		assert.False(t, inherited)
		return c.NoContent(http.StatusNoContent)
	})

	c, rec := echotest.Get(t, "/admin/usage", echotest.WithHeader("Authorization", "Bearer master"))
	ctx := ext.WithAuthentication(c.Request().Context(), ext.Authentication{
		PrincipalID: "oidc:ambient", UserPath: "/users/sso", Labels: []string{"sso"},
	})
	ctx = core.WithEffectiveUserPath(ctx, "/users/sso")
	c.SetRequest(c.Request().WithContext(ctx))
	rec.Header().Set(ext.AuthenticationUserHeader, "/users/sso")
	require.NoError(t, handler(c))
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, rec.Header().Get(ext.AuthenticationUserHeader))
	assert.Zero(t, requestAuth.calls)
}

func TestAuthMiddleware_InteractionContinuationAccess(t *testing.T) {
	tests := []struct {
		name          string
		masterKey     string
		authenticator BearerTokenAuthenticator
		token         string
		wantAllowed   bool
	}{
		{name: "authentication disabled", wantAllowed: true},
		{name: "master key", masterKey: "master", token: "master", wantAllowed: true},
		{
			name: "managed key with dashboard access",
			authenticator: mockAuthenticator{
				enabled:        true,
				tokenToID:      map[string]string{"managed": "key-1"},
				tokenDashboard: map[string]bool{"managed": true},
			},
			token:       "managed",
			wantAllowed: true,
		},
		{
			name: "managed key without dashboard access",
			authenticator: mockAuthenticator{
				enabled:   true,
				tokenToID: map[string]string{"managed": "key-1"},
			},
			token: "managed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := AuthMiddlewareWithAuthenticator(tt.masterKey, tt.authenticator, nil)(func(c *echo.Context) error {
				assert.Equal(t, tt.wantAllowed, interactionContinuationAllowed(c.Request().Context()))
				return c.NoContent(http.StatusNoContent)
			})

			var opts []echotest.Option
			if tt.token != "" {
				opts = append(opts, echotest.WithHeader("Authorization", "Bearer "+tt.token))
			}
			c, rec := echotest.Request(t, http.MethodPost, "/v1/responses", nil, opts...)
			err := handler(c)

			require.NoError(t, err)
			assert.Equal(t, http.StatusNoContent, rec.Code)
		})
	}
}

func TestAuthMiddlewareWithAuthenticator_ManagedKeyLabelsMergeWithHeaderLabels(t *testing.T) {
	testHandler := func(c *echo.Context) error {
		got := core.RequestLabelsFromContext(c.Request().Context())
		assert.Equal(t, []string{"from-header", "team-a", "batch"}, got)
		return c.String(http.StatusOK, "ok")
	}

	handler := AuthMiddlewareWithAuthenticator("", mockAuthenticator{
		enabled:     true,
		tokenToID:   map[string]string{"sk_gom_token": "key-123"},
		tokenLabels: map[string][]string{"sk_gom_token": {"team-a", "batch", "from-header"}},
	}, nil)(testHandler)

	c, rec := echotest.Get(t, "/", echotest.WithHeader("Authorization", "Bearer sk_gom_token"))
	// Simulate the tagging middleware having already extracted header labels.
	c.SetRequest(c.Request().WithContext(core.WithRequestLabels(c.Request().Context(), []string{"from-header"})))

	err := handler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuthMiddlewareWithAuthenticator_ManagedKeyWithoutLabelsKeepsHeaderLabels(t *testing.T) {
	testHandler := func(c *echo.Context) error {
		got := core.RequestLabelsFromContext(c.Request().Context())
		assert.Equal(t, []string{"from-header"}, got)
		return c.String(http.StatusOK, "ok")
	}

	handler := AuthMiddlewareWithAuthenticator("", mockAuthenticator{
		enabled:   true,
		tokenToID: map[string]string{"sk_gom_token": "key-123"},
	}, nil)(testHandler)

	c, rec := echotest.Get(t, "/", echotest.WithHeader("Authorization", "Bearer sk_gom_token"))
	c.SetRequest(c.Request().WithContext(core.WithRequestLabels(c.Request().Context(), []string{"from-header"})))

	err := handler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuthMiddlewareWithAuthenticator_ManagedKeyUserPathOverridesHeader(t *testing.T) {
	testHandler := func(c *echo.Context) error {
		got := core.UserPathFromContext(c.Request().Context())
		require.Equal(t, "/team/auth-key", got)

		snapshot := core.GetRequestSnapshot(c.Request().Context())
		require.NotNil(t, snapshot)
		got = snapshot.UserPath
		require.Equal(t, "/team/auth-key", got)
		got = c.Request().Header.Get(core.UserPathHeader)
		require.Equal(t, "/team/auth-key", got, "header %s", core.UserPathHeader)

		entryVal := c.Get(string(auditlog.LogEntryKey))
		entry, ok := entryVal.(*auditlog.LogEntry)
		require.True(t, ok)
		require.NotNil(t, entry)
		got = entry.UserPath
		require.Equal(t, "/team/auth-key", got)

		return c.String(http.StatusOK, "ok")
	}

	handler := RequestSnapshotCapture()(AuthMiddlewareWithAuthenticator("", mockAuthenticator{
		enabled:   true,
		tokenToID: map[string]string{"sk_gom_token": "key-123"},
		tokenPath: map[string]string{"sk_gom_token": "/team/auth-key"},
	}, nil)(testHandler))

	c, rec := echotest.Post(t, "/v1/chat/completions", `{"model":"gpt-5-mini"}`, echotest.WithHeader(core.UserPathHeader, "/team/from-header"), echotest.WithHeader("Authorization", "Bearer sk_gom_token"))
	c.Set(string(auditlog.LogEntryKey), &auditlog.LogEntry{Data: &auditlog.LogData{}})

	err := handler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "/team/auth-key", rec.Header().Get(ext.AuthenticationUserHeader))
}

func TestAuthMiddlewareWithAuthenticator_ManagedKeyUserPathUsesConfiguredHeader(t *testing.T) {
	const headerName = "X-Tenant-Path"
	testHandler := func(c *echo.Context) error {
		got := core.UserPathFromContext(c.Request().Context())
		require.Equal(t, "/team/auth-key", got)

		snapshot := core.GetRequestSnapshot(c.Request().Context())
		require.NotNil(t, snapshot)
		got = snapshot.GetHeaders()[headerName][0]
		require.Equal(t, "/team/auth-key", got)
		got = c.Request().Header.Get(headerName)
		require.Equal(t, "/team/auth-key", got, "header %s", headerName)
		got = c.Request().Header.Get(core.UserPathHeader)
		require.Empty(t, got, "header %s", core.UserPathHeader)

		return c.String(http.StatusOK, "ok")
	}

	handler := RequestSnapshotCapture(headerName)(AuthMiddlewareWithAuthenticator("", mockAuthenticator{
		enabled:   true,
		tokenToID: map[string]string{"sk_gom_token": "key-123"},
		tokenPath: map[string]string{"sk_gom_token": "/team/auth-key"},
	}, nil, headerName)(testHandler))

	c, rec := echotest.Post(t, "/v1/chat/completions", `{"model":"gpt-5-mini"}`, echotest.WithHeader("Authorization", "Bearer sk_gom_token"))

	err := handler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuthMiddlewareWithAuthenticator_ManagedKeyFailureUsesGenericClientMessage(t *testing.T) {
	handler := AuthMiddlewareWithAuthenticator("", mockAuthenticator{
		enabled: true,
		err:     context.DeadlineExceeded,
	}, nil)(func(c *echo.Context) error {
		t.Fatal("next handler should not be called")
		return nil
	})

	c, rec := echotest.Get(t, "/", echotest.WithHeader("Authorization", "Bearer sk_gom_token"))
	c.Set(string(auditlog.LogEntryKey), &auditlog.LogEntry{Data: &auditlog.LogData{}})

	err := handler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.JSONEq(t, `{"error":{"message":"authentication failed","type":"authentication_error","param":null,"code":null}}`, rec.Body.String())

	entryVal := c.Get(string(auditlog.LogEntryKey))
	entry, ok := entryVal.(*auditlog.LogEntry)
	require.True(t, ok)
	require.NotNil(t, entry)
	require.NotNil(t, entry.Data)
	assert.Equal(t, auditlog.AuthMethodAPIKey, entry.AuthMethod)
	assert.Equal(t, string(core.ErrorTypeAuthentication), entry.ErrorType)
	assert.Equal(t, "authentication unavailable", entry.Data.ErrorMessage)
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
			name:     "no match /admin/models",
			path:     "/admin/models",
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

func TestAuthMiddleware_SkipPathEnrichesNoKeyAuditMethod(t *testing.T) {
	handler := AuthMiddlewareWithAuthenticator("secret-key", nil, []string{"/health"})(func(c *echo.Context) error {
		entryVal := c.Get(string(auditlog.LogEntryKey))
		entry, ok := entryVal.(*auditlog.LogEntry)
		require.True(t, ok)
		require.NotNil(t, entry)
		require.Equal(t, auditlog.AuthMethodNoKey, entry.AuthMethod)

		return c.String(http.StatusOK, "ok")
	})

	c, rec := echotest.Get(t, "/health",
		echotest.WithValue(string(auditlog.LogEntryKey), &auditlog.LogEntry{Data: &auditlog.LogData{}}),
	)

	err := handler(c)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
}
