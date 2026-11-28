package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/admin"
)

// loopbackPeer is the socket address of a caller on the gateway's machine.
const loopbackPeer = "127.0.0.1:52000"

func TestAdminOpenAPI_ServesOnlyLocalCallers(t *testing.T) {
	tests := []struct {
		name       string
		cfg        Config
		remoteAddr string
		headers    map[string]string
		wantStatus int
	}{
		{name: "IPv4 loopback", remoteAddr: loopbackPeer, wantStatus: http.StatusOK},
		{name: "IPv6 loopback", remoteAddr: "[::1]:52000", wantStatus: http.StatusOK},
		{name: "IPv4-mapped loopback", remoteAddr: "[::ffff:127.0.0.1]:52000", wantStatus: http.StatusOK},
		{name: "remote peer", remoteAddr: "192.0.2.1:52000", wantStatus: http.StatusForbidden},
		{name: "container bridge gateway", remoteAddr: "172.17.0.1:52000", wantStatus: http.StatusForbidden},
		{name: "remote peer claiming loopback", remoteAddr: "192.0.2.1:52000", headers: map[string]string{"X-Forwarded-For": "127.0.0.1", "X-Real-IP": "127.0.0.1"}, wantStatus: http.StatusForbidden},
		{name: "same-host proxy with X-Forwarded-For", remoteAddr: loopbackPeer, headers: map[string]string{"X-Forwarded-For": "198.51.100.7"}, wantStatus: http.StatusForbidden},
		{name: "same-host proxy with Forwarded", remoteAddr: loopbackPeer, headers: map[string]string{"Forwarded": "for=198.51.100.7"}, wantStatus: http.StatusForbidden},
		{name: "same-host proxy with X-Real-IP", remoteAddr: loopbackPeer, headers: map[string]string{"X-Real-IP": "198.51.100.7"}, wantStatus: http.StatusForbidden},
		{
			name:       "trusted same-host proxy with a custom client header",
			cfg:        Config{IPExtractor: ClientIPExtractor(mustClientIPPolicy(t, "CF-Connecting-IP"))},
			remoteAddr: loopbackPeer,
			headers:    map[string]string{"CF-Connecting-IP": "198.51.100.7"},
			wantStatus: http.StatusForbidden,
		},
		{name: "trusted proxies do not widen access", cfg: Config{IPExtractor: ClientIPExtractor(mustClientIPPolicy(t, ""))}, remoteAddr: loopbackPeer, headers: map[string]string{"X-Forwarded-For": "127.0.0.1"}, wantStatus: http.StatusForbidden},
		{name: "remote access opted into", cfg: Config{AdminAllowUnauthenticatedRemote: true}, remoteAddr: "192.0.2.1:52000", wantStatus: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			cfg.AdminEndpointsEnabled = true
			cfg.AdminHandler = admin.NewHandler(nil, nil)
			srv := New(&mockProvider{}, &cfg)

			for _, path := range []string{"/admin/models", "/admin/api/v1/models"} {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.RemoteAddr = tt.remoteAddr
				for name, value := range tt.headers {
					req.Header.Set(name, value)
				}
				rec := httptest.NewRecorder()
				srv.ServeHTTP(rec, req)

				require.Equal(t, tt.wantStatus, rec.Code, "%s: %s", path, rec.Body.String())
				if tt.wantStatus == http.StatusForbidden {
					var body struct {
						Error struct {
							Code string `json:"code"`
						} `json:"error"`
					}
					require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
					assert.Equal(t, "admin_remote_access_denied", body.Error.Code)
				}
			}
		})
	}
}

func TestAdminOpenAPI_GuardOnlyAppliesWithoutCredentials(t *testing.T) {
	tests := []struct {
		name   string
		cfg    Config
		bearer string
	}{
		{name: "master key", cfg: Config{MasterKey: "master-key"}, bearer: "master-key"},
		{
			name: "managed dashboard key",
			cfg: Config{Authenticator: mockAuthenticator{
				enabled:        true,
				tokenToID:      map[string]string{"sk_gom_admin": "key-admin"},
				tokenDashboard: map[string]bool{"sk_gom_admin": true},
			}},
			bearer: "sk_gom_admin",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			cfg.AdminEndpointsEnabled = true
			cfg.AdminHandler = admin.NewHandler(nil, nil)
			srv := New(&mockProvider{}, &cfg)

			req := httptest.NewRequest(http.MethodGet, "/admin/models", nil)
			req.RemoteAddr = "192.0.2.1:52000"
			req.Header.Set("Authorization", "Bearer "+tt.bearer)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		})
	}
}

func TestIsLoopbackAddress(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{value: "127.0.0.1:80", want: true},
		{value: "127.8.9.10:80", want: true},
		{value: "[::1]:80", want: true},
		{value: "::1", want: true},
		{value: "127.0.0.1", want: true},
		{value: "[::ffff:127.0.0.1]:80", want: true},
		{value: "0.0.0.0:80"},
		{value: "192.0.2.1:80"},
		{value: "localhost:80"},
		{value: ""},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			assert.Equal(t, tt.want, isLoopbackAddress(tt.value))
		})
	}
}

func mustClientIPPolicy(t *testing.T, header string) config.ClientIPPolicy {
	t.Helper()
	cfg := config.ServerConfig{TrustedProxies: []string{"loopback"}, ClientIPHeader: header}
	require.NoError(t, config.ResolveClientIPPolicy(&cfg))
	return cfg.ClientIP
}
