package mcpgateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOAuthDialGuardAllowed(t *testing.T) {
	tests := []struct {
		ip            string
		allowLoopback bool
		serverPrivate bool
		want          bool
	}{
		{ip: "93.184.216.34", want: true},
		{ip: "2606:4700::1111", want: true},
		{ip: "10.1.2.3"},
		{ip: "172.16.0.1"},
		{ip: "192.168.1.1"},
		{ip: "169.254.169.254"},
		{ip: "100.64.0.1"},
		{ip: "0.0.0.0"},
		{ip: "224.0.0.1"},
		{ip: "fd00::1"},
		{ip: "fe80::1"},
		{ip: "::ffff:10.0.0.1"},
		{ip: "127.0.0.1"},
		{ip: "::1"},
		{ip: "127.0.0.1", allowLoopback: true, want: true},
		{ip: "10.1.2.3", allowLoopback: true},
		// An MCP server on a private network signs in through an internal
		// identity provider; metadata and loopback stay out of reach.
		{ip: "10.1.2.3", serverPrivate: true, want: true},
		{ip: "192.168.1.1", serverPrivate: true, want: true},
		{ip: "100.64.0.1", serverPrivate: true, want: true},
		{ip: "fd00::1", serverPrivate: true, want: true},
		{ip: "93.184.216.34", serverPrivate: true, want: true},
		{ip: "169.254.169.254", serverPrivate: true},
		{ip: "fe80::1", serverPrivate: true},
		{ip: "127.0.0.1", serverPrivate: true},
		{ip: "0.0.0.0", serverPrivate: true},
	}
	for _, tt := range tests {
		guard := &oauthDialGuard{allowLoopback: tt.allowLoopback, serverPrivate: func() bool { return tt.serverPrivate }}
		assert.Equal(t, tt.want, guard.allowed(netip.MustParseAddr(tt.ip)), "%s (loopback allowed: %v, private server: %v)", tt.ip, tt.allowLoopback, tt.serverPrivate)
	}
}

func TestPrivateHostOnce(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{host: "10.0.0.5", want: true},
		{host: "192.168.0.10", want: true},
		{host: "fd12::1", want: true},
		{host: "93.184.216.34"},
		{host: "127.0.0.1"},
		{host: "169.254.169.254"},
		{host: ""},
		{host: "unresolvable.invalid"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, privateHostOnce(tt.host)(), tt.host)
	}
}

func newMetadataServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://"+strings.Replace(r.Host, "localhost", "127.0.0.1", 1)+"/ok", http.StatusFound)
			return
		}
		writeTestJSON(w, http.StatusOK, map[string]any{"resource": "https://mcp.example.com/mcp", "authorization_servers": []string{"https://auth.example.com"}})
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestOAuthHTTPClientRefusesNonPublicAddresses(t *testing.T) {
	ts := newMetadataServer(t)
	parsed, err := url.Parse(ts.URL)
	require.NoError(t, err)
	port := parsed.Port()
	client := oauthHTTPClient(ts.Client(), "https://mcp.example.com/mcp")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, target := range []string{
		"http://localhost:" + port + "/.well-known/oauth-protected-resource", // a name resolving to loopback
		"http://127.0.0.1:" + port + "/",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.255.255.1/",
	} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		require.NoError(t, err)
		_, err = client.Do(req)
		require.ErrorIs(t, err, errOAuthNonPublicAddress, target)
	}

	// The go-sdk discovery helpers send through the guard too.
	_, err = oauthex.GetProtectedResourceMetadata(ctx, "http://localhost:"+port+"/.well-known/oauth-protected-resource", "https://mcp.example.com/mcp", client)
	require.ErrorContains(t, err, errOAuthNonPublicAddress.Error())
}

func TestOAuthHTTPClientAllowsServerHostAndLocalLoopback(t *testing.T) {
	ts := newMetadataServer(t)
	parsed, err := url.Parse(ts.URL)
	require.NoError(t, err)
	localhostURL := "http://localhost:" + parsed.Port()

	// A loopback MCP server may use loopback OAuth endpoints.
	client := oauthHTTPClient(ts.Client(), ts.URL+"/mcp")
	resp, err := client.Get(localhostURL + "/")
	require.NoError(t, err)
	_ = resp.Body.Close()

	// The server's own host is reachable wherever it resolves; others are
	// checked, redirects included.
	own := &http.Client{Transport: &oauthGuardTransport{base: ts.Client(), guard: &oauthDialGuard{serverHost: "localhost"}}}
	resp, err = own.Get(localhostURL + "/")
	require.NoError(t, err)
	_ = resp.Body.Close()
	_, err = own.Get(ts.URL + "/")
	require.ErrorIs(t, err, errOAuthNonPublicAddress)
	_, err = own.Get(localhostURL + "/redirect")
	require.ErrorIs(t, err, errOAuthNonPublicAddress, "a redirect to a non-public address is refused")
}
