package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/admin"
)

func newAdminGateServer(masterKey string) *Server {
	return New(&mockProvider{}, &Config{
		MasterKey:             masterKey,
		AdminEndpointsEnabled: true,
		AdminHandler:          admin.NewHandler(nil, nil),
		Authenticator: mockAuthenticator{
			enabled: true,
			tokenToID: map[string]string{
				"sk_gom_plain": "key-plain",
				"sk_gom_admin": "key-admin",
			},
			tokenDashboard: map[string]bool{"sk_gom_admin": true},
		},
	})
}

func adminGateStatus(t *testing.T, srv *Server, path, bearer string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func TestAdminGate_ManagedKeyWithoutDashboardAccessIsDenied(t *testing.T) {
	srv := newAdminGateServer("master-key")

	for _, path := range []string{"/admin/auth-keys", "/admin/api/v1/auth-keys"} {
		status, body := adminGateStatus(t, srv, path, "sk_gom_plain")
		assert.Equal(t, http.StatusForbidden, status, "path %s", path)

		errObj, ok := body["error"].(map[string]any)
		require.True(t, ok, "path %s: error payload missing: %v", path, body)
		assert.Equal(t, "dashboard_access_denied", errObj["code"], "path %s", path)
	}
}

func TestAdminGate_AllowedCredentialsPass(t *testing.T) {
	srv := newAdminGateServer("master-key")

	// The auth-keys service is not wired in this test server, so passing the
	// gate surfaces the handler's 503 rather than the gate's 403.
	tests := []struct {
		name   string
		path   string
		bearer string
	}{
		{"dashboard key", "/admin/auth-keys", "sk_gom_admin"},
		{"dashboard key legacy alias", "/admin/api/v1/auth-keys", "sk_gom_admin"},
		{"master key", "/admin/auth-keys", "master-key"},
		{"master key legacy alias", "/admin/api/v1/auth-keys", "master-key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, _ := adminGateStatus(t, srv, tt.path, tt.bearer)
			assert.Equal(t, http.StatusServiceUnavailable, status)
		})
	}
}

func TestAdminGate_DoesNotCoverModelAndUsageRoutes(t *testing.T) {
	srv := newAdminGateServer("master-key")

	// /v1/usage stays open to every managed key regardless of dashboard
	// access; only 401/403 would indicate the gate leaked onto the route.
	status, _ := adminGateStatus(t, srv, "/v1/usage", "sk_gom_plain")
	assert.NotEqual(t, http.StatusForbidden, status)
	assert.NotEqual(t, http.StatusUnauthorized, status)

	status, _ = adminGateStatus(t, srv, "/v1/models", "sk_gom_plain")
	assert.Equal(t, http.StatusOK, status)
}

func TestAdminGate_LockoutRecoveryPathStaysOpen(t *testing.T) {
	// Without a master key the /admin/* routes deliberately skip auth so the
	// dashboard can recover managed-key access; the gate must not re-lock it.
	srv := newAdminGateServer("")

	status, _ := adminGateStatus(t, srv, "/admin/auth-keys", "")
	assert.Equal(t, http.StatusServiceUnavailable, status)
}

func TestAdminGate_RequestAuthenticatorDisablesAnonymousRecoveryBypass(t *testing.T) {
	tests := []struct {
		name       string
		identity   *ext.Authentication
		wantStatus int
	}{
		{name: "anonymous", wantStatus: http.StatusUnauthorized},
		{
			name:       "identity without dashboard access",
			identity:   &ext.Authentication{PrincipalID: "principal-1", UserPath: "/users/one", Method: "oidc"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "identity with dashboard access",
			identity:   &ext.Authentication{PrincipalID: "principal-1", UserPath: "/users/one", Method: "oidc", DashboardAccess: true},
			wantStatus: http.StatusServiceUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := New(&mockProvider{}, &Config{
				AdminEndpointsEnabled: true,
				AdminHandler:          admin.NewHandler(nil, nil),
				RequestAuthenticators: []ext.RequestAuthenticator{&mockRequestAuthenticator{result: tt.identity}},
			})
			status, body := adminGateStatus(t, srv, "/admin/auth-keys", "")
			assert.Equal(t, tt.wantStatus, status)
			if tt.wantStatus == http.StatusForbidden {
				errObj, ok := body["error"].(map[string]any)
				require.True(t, ok)
				assert.Equal(t, "identity does not have dashboard access", errObj["message"])
			}
		})
	}
}

func TestAdminGate_NilRequestAuthenticatorsKeepRecoveryBypass(t *testing.T) {
	var typedNil *mockRequestAuthenticator
	for _, authenticators := range [][]ext.RequestAuthenticator{{nil}, {typedNil}} {
		srv := New(&mockProvider{}, &Config{
			AdminEndpointsEnabled: true,
			AdminHandler:          admin.NewHandler(nil, nil),
			RequestAuthenticators: authenticators,
		})
		status, _ := adminGateStatus(t, srv, "/admin/auth-keys", "")
		assert.Equal(t, http.StatusServiceUnavailable, status)
	}
}
