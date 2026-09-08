package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/ext"
)

func getAuthVerify(t *testing.T, cfg *Config, token string) (*httptest.ResponseRecorder, authVerifyResponse) {
	t.Helper()
	srv := New(&mockProvider{}, cfg)
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/verify", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	var body authVerifyResponse
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "body: %s", rec.Body.String())
	}
	return rec, body
}

func TestAuthVerify_RouteDisabledByDefault(t *testing.T) {
	rec, _ := getAuthVerify(t, &Config{MasterKey: "master"}, "master")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAuthVerify_ManagedKey(t *testing.T) {
	cfg := &Config{
		AuthVerifyEnabled: true,
		MasterKey:         "master",
		Authenticator: mockAuthenticator{
			enabled:   true,
			tokenToID: map[string]string{"sk_gom_token": "key-123"},
			tokenPath: map[string]string{"sk_gom_token": "/team"},
		},
	}

	rec, body := getAuthVerify(t, cfg, "sk_gom_token")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, authVerifyResponse{Valid: true, Method: "api_key", KeyID: "key-123", UserPath: "/team"}, body)
}

func TestAuthVerify_MasterKey(t *testing.T) {
	rec, body := getAuthVerify(t, &Config{AuthVerifyEnabled: true, MasterKey: "master"}, "master")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, authVerifyResponse{Valid: true, Method: "master_key"}, body)
}

func TestAuthVerify_ExtensionIdentity(t *testing.T) {
	cfg := &Config{
		AuthVerifyEnabled: true,
		RequestAuthenticators: []ext.RequestAuthenticator{&mockRequestAuthenticator{
			result: &ext.Authentication{PrincipalID: "principal-1", UserPath: "/team", Method: "oidc"},
		}},
	}

	rec, body := getAuthVerify(t, cfg, "")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, authVerifyResponse{Valid: true, Method: "oidc", UserPath: "/team"}, body)
}

func TestAuthVerify_InvalidKeyIsRejected(t *testing.T) {
	cfg := &Config{
		AuthVerifyEnabled: true,
		Authenticator: mockAuthenticator{
			enabled:   true,
			tokenToID: map[string]string{"sk_gom_token": "key-123"},
		},
	}

	rec, _ := getAuthVerify(t, cfg, "sk_gom_unknown")

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.NotContains(t, rec.Body.String(), "sk_gom_unknown")
}

func TestAuthVerify_MissingCredentialIsRejected(t *testing.T) {
	rec, _ := getAuthVerify(t, &Config{AuthVerifyEnabled: true, MasterKey: "master"}, "")

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// A gateway without any configured authentication accepts every request, so
// the endpoint has no credential to confirm and says so instead of claiming
// the presented key is valid.
func TestAuthVerify_NoAuthenticationConfigured(t *testing.T) {
	for name, cfg := range map[string]*Config{
		"no auth mechanism":         {AuthVerifyEnabled: true},
		"authenticator not enabled": {AuthVerifyEnabled: true, Authenticator: mockAuthenticator{}},
	} {
		t.Run(name, func(t *testing.T) {
			rec, body := getAuthVerify(t, cfg, "anything")

			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, authVerifyResponse{Valid: false, Method: "none"}, body)
		})
	}
}
