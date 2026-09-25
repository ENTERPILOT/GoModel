package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/admin"
)

func masterKeyDisabledStatus(t *testing.T, srv *Server, path, bearer string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec.Code
}

// A gateway that disabled its master key authenticates managed keys only, even
// when the key is still present in the configuration an embedder passed.
func TestMasterKeyDisabledRejectsTheMasterKey(t *testing.T) {
	srv := New(&mockProvider{}, &Config{
		MasterKey:         "master-key",
		MasterKeyDisabled: true,
		Authenticator: mockAuthenticator{
			enabled:   true,
			tokenToID: map[string]string{"sk_gom_plain": "key-plain"},
		},
	})

	assert.Equal(t, http.StatusUnauthorized, masterKeyDisabledStatus(t, srv, "/v1/models", "master-key"))
	assert.Equal(t, http.StatusOK, masterKeyDisabledStatus(t, srv, "/v1/models", "sk_gom_plain"))
}

// Without managed keys the disabled master key must fail closed: an absent
// master key opens the gateway, a disabled one closes it.
func TestMasterKeyDisabledRequiresACredentialWithNoOtherMechanism(t *testing.T) {
	srv := New(&mockProvider{}, &Config{MasterKeyDisabled: true})

	assert.Equal(t, http.StatusUnauthorized, masterKeyDisabledStatus(t, srv, "/v1/models", ""))
	assert.Equal(t, http.StatusUnauthorized, masterKeyDisabledStatus(t, srv, "/v1/models", "master-key"))

	open := New(&mockProvider{}, &Config{})
	require.Equal(t, http.StatusOK, masterKeyDisabledStatus(t, open, "/v1/models", ""))
}

// The anonymous /admin/* recovery bypass exists for deployments that never
// configured a master key; one that turned it off wants the admin API gated
// behind its managed keys too.
func TestMasterKeyDisabledClosesTheAdminRecoveryBypass(t *testing.T) {
	srv := New(&mockProvider{}, &Config{
		MasterKeyDisabled:     true,
		AdminEndpointsEnabled: true,
		AdminHandler:          admin.NewHandler(nil, nil),
	})

	assert.Equal(t, http.StatusUnauthorized, masterKeyDisabledStatus(t, srv, "/admin/auth-keys", ""))
}

// GET /v1/auth/verify attests authentication from evidence, so a disabled
// master key must not authenticate there either.
func TestMasterKeyDisabledIsNotAcceptedByAuthVerify(t *testing.T) {
	srv := New(&mockProvider{}, &Config{
		MasterKey:         "master-key",
		MasterKeyDisabled: true,
		AuthVerifyEnabled: true,
	})

	assert.Equal(t, http.StatusUnauthorized, masterKeyDisabledStatus(t, srv, "/v1/auth/verify", "master-key"))
}
