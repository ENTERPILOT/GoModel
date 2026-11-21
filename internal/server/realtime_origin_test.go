package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/echotest"
)

func TestRealtimeOriginGuard(t *testing.T) {
	tests := []struct {
		name    string
		allowed []string
		host    string
		origin  string
		refused bool
	}{
		{name: "no origin: SDKs, servers, CLI tools", host: "gateway.example.com"},
		{name: "page served from the gateway host", host: "gateway.example.com", origin: "https://gateway.example.com"},
		{name: "same host with a port", host: "localhost:8080", origin: "http://localhost:8080"},
		{name: "same host, explicit default port", host: "localhost:80", origin: "http://localhost"},
		{name: "same host, another scheme's default port", host: "localhost:443", origin: "http://localhost", refused: true},
		{name: "same host, different case", host: "Gateway.Example.com", origin: "https://gateway.example.COM"},
		{name: "another site", host: "gateway.example.com", origin: "https://app.example.net", refused: true},
		{name: "same host, another port", host: "localhost:8080", origin: "http://localhost:3000", refused: true},
		{name: "a subdomain of the gateway host", host: "example.com", origin: "https://app.example.com", refused: true},
		{name: "opaque origin", host: "gateway.example.com", origin: "null", refused: true},
		{name: "malformed origin", host: "gateway.example.com", origin: "gateway.example.com", refused: true},
		{name: "listed origin", allowed: []string{"https://app.example.net"}, host: "gateway.example.com", origin: "https://app.example.net"},
		{name: "listed origin, entry not normalized", allowed: []string{" HTTPS://App.Example.net:443 "}, host: "gateway.example.com", origin: "https://app.example.net"},
		{name: "listed origin, another scheme", allowed: []string{"https://app.example.net"}, host: "gateway.example.com", origin: "http://app.example.net", refused: true},
		{name: "listed origin, another port", allowed: []string{"http://localhost:3000"}, host: "localhost:8080", origin: "http://localhost:5173", refused: true},
		{name: "unlisted origin beside a listed one", allowed: []string{"https://app.example.net"}, host: "gateway.example.com", origin: "https://other.example.org", refused: true},
		{name: "an invalid entry matches nothing", allowed: []string{"app.example.net"}, host: "gateway.example.com", origin: "https://app.example.net", refused: true},
		{name: "any origin trusted", allowed: []string{"*"}, host: "gateway.example.com", origin: "https://app.example.net"},
		{name: "any origin trusted, opaque origin", allowed: []string{"*"}, host: "gateway.example.com", origin: "null"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+tt.host+"/v1/realtime", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			err := newRealtimeOriginGuard(tt.allowed).check(req)
			if !tt.refused {
				require.NoError(t, err)

				return
			}
			require.Error(t, err)
			assert.NotContains(t, err.Error(), tt.origin, "the refusal must not echo the origin")
		})
	}
}

// A handler built without configuration refuses cross-origin upgrades too.
func TestRealtimeOriginGuard_NilGuardIsStrict(t *testing.T) {
	var guard *realtimeOriginGuard

	req := httptest.NewRequest(http.MethodGet, "http://gateway.example.com/v1/realtime", nil)
	require.NoError(t, guard.check(req))

	req.Header.Set("Origin", "https://gateway.example.com")
	require.NoError(t, guard.check(req))

	req.Header.Set("Origin", "https://app.example.net")
	require.Error(t, guard.check(req))
}

// A refused origin is answered before the session is resolved or the upstream
// is dialed; an allowed one reaches the provider router.
func TestRealtime_ChecksOriginBeforeRouting(t *testing.T) {
	tests := []struct {
		name    string
		allowed []string
		origin  string
		routed  bool
	}{
		{name: "another site", origin: "https://app.example.net"},
		{name: "listed origin", allowed: []string{"https://app.example.net"}, origin: "https://app.example.net", routed: true},
		{name: "no origin", routed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &realtimeWebRTCMock{
				mockProvider: &mockProvider{supportedModels: []string{"gpt-realtime"}},
			}
			handler := newRealtimeTestHandler(mock, nil)
			handler.realtimeOrigins = newRealtimeOriginGuard(tt.allowed)

			var opts []echotest.Option
			if tt.origin != "" {
				opts = append(opts, echotest.WithHeader("Origin", tt.origin))
			}
			c, rec := echotest.Get(t, "/v1/realtime?model=gpt-realtime", opts...)

			_ = handler.Realtime(c)

			if tt.routed {
				require.NotNil(t, mock.capturedRealtime, "router received no request (status %d, body %s)", rec.Code, rec.Body.String())

				return
			}
			require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
			assert.Nil(t, mock.capturedRealtime, "a refused origin must not reach the provider router")
			assert.Contains(t, rec.Body.String(), "REALTIME_ALLOWED_ORIGINS")
		})
	}
}
