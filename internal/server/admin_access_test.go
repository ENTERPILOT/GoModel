package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

func TestAdminGate_ManagedKeyWithDashboardAccessPasses(t *testing.T) {
	srv := newAdminGateServer("master-key")

	// The auth-keys service is not wired in this test server, so passing the
	// gate surfaces the handler's 503 rather than the gate's 403.
	status, _ := adminGateStatus(t, srv, "/admin/auth-keys", "sk_gom_admin")
	assert.Equal(t, http.StatusServiceUnavailable, status)
}

func TestAdminGate_MasterKeyAlwaysPasses(t *testing.T) {
	srv := newAdminGateServer("master-key")

	status, _ := adminGateStatus(t, srv, "/admin/auth-keys", "master-key")
	assert.Equal(t, http.StatusServiceUnavailable, status)
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
