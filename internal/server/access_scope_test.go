package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/auditlog"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
)

type scopeRequestAuthenticator struct {
	result *ext.Authentication
}

func (a scopeRequestAuthenticator) Name() string { return "scope-test" }

func (a scopeRequestAuthenticator) AuthenticateRequest(_ context.Context, r *http.Request) (*ext.Authentication, error) {
	if r.Header.Get("Cookie") == "" {
		return nil, nil
	}
	return a.result, nil
}

// TestAuthMiddleware_AccessScopeFollowsCredential pins that the access scope
// is derived from the credential's bound user path only: the master key and
// unscoped keys stay global even when they send X-GoModel-User-Path, and a
// bound key or extension identity is confined to its path.
func TestAuthMiddleware_AccessScopeFollowsCredential(t *testing.T) {
	authenticator := mockAuthenticator{
		enabled: true,
		tokenToID: map[string]string{
			"sk_gom_scoped":   "key-scoped",
			"sk_gom_unscoped": "key-unscoped",
			"sk_gom_root":     "key-root",
		},
		tokenPath: map[string]string{
			"sk_gom_scoped": "/team/alpha",
			"sk_gom_root":   "/",
		},
	}
	extIdentity := scopeRequestAuthenticator{result: &ext.Authentication{
		PrincipalID: "alice",
		UserPath:    "/team/beta",
	}}

	tests := []struct {
		name       string
		bearer     string
		cookie     string
		pathHeader string
		// seedScope simulates an outer extension middleware that already
		// installed a scope before the auth middleware runs.
		seedScope  string
		wantScope  string
		wantGlobal bool
	}{
		{name: "master key is global", bearer: "master-key", wantGlobal: true},
		{name: "master key with header stays global", bearer: "master-key", pathHeader: "/team/alpha", wantGlobal: true},
		{name: "unscoped key is global", bearer: "sk_gom_unscoped", pathHeader: "/team/alpha", wantGlobal: true},
		{name: "root key is global", bearer: "sk_gom_root", wantGlobal: true},
		{name: "scoped key is confined", bearer: "sk_gom_scoped", wantScope: "/team/alpha"},
		{name: "scoped key header cannot widen", bearer: "sk_gom_scoped", pathHeader: "/", wantScope: "/team/alpha"},
		{name: "extension identity is confined", cookie: "session=1", wantScope: "/team/beta"},
		{name: "explicit bearer replaces extension scope", cookie: "session=1", bearer: "master-key", seedScope: "/team/beta", wantGlobal: true},
		{name: "explicit scoped bearer replaces extension scope", cookie: "session=1", bearer: "sk_gom_scoped", seedScope: "/team/beta", wantScope: "/team/alpha"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got core.AccessScope
			handler := AuthMiddlewareWithRequestAuthenticators("master-key", authenticator, []ext.RequestAuthenticator{extIdentity}, nil)(func(c *echo.Context) error {
				got = core.AccessScopeFromContext(c.Request().Context())
				return c.String(http.StatusOK, "ok")
			})

			opts := []echotest.Option{echotest.WithValue(string(auditlog.LogEntryKey), &auditlog.LogEntry{Data: &auditlog.LogData{}})}
			if tt.bearer != "" {
				opts = append(opts, echotest.WithHeader("Authorization", "Bearer "+tt.bearer))
			}
			if tt.cookie != "" {
				opts = append(opts, echotest.WithHeader("Cookie", tt.cookie))
			}
			if tt.pathHeader != "" {
				opts = append(opts, echotest.WithHeader(core.UserPathHeader, tt.pathHeader))
			}
			c, rec := echotest.Get(t, "/v1/models", opts...)
			if tt.seedScope != "" {
				c.SetRequest(c.Request().WithContext(core.WithAccessScope(c.Request().Context(), core.AccessScope{UserPath: tt.seedScope})))
			}

			require.NoError(t, handler(c))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, tt.wantGlobal, got.Global())
			if !tt.wantGlobal {
				assert.Equal(t, tt.wantScope, got.UserPath)
			}
		})
	}
}
