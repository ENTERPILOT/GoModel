package mcpgateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOAuthDialGuardAllowed(t *testing.T) {
	tests := []struct {
		ip   string
		zone serverZone
		want bool
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
		// A loopback MCP server may use loopback OAuth endpoints, nothing else.
		{ip: "127.0.0.1", zone: zoneLoopback, want: true},
		{ip: "10.1.2.3", zone: zoneLoopback},
		// An MCP server on a private network signs in through an internal
		// identity provider; metadata and loopback stay out of reach.
		{ip: "10.1.2.3", zone: zonePrivate, want: true},
		{ip: "192.168.1.1", zone: zonePrivate, want: true},
		{ip: "100.64.0.1", zone: zonePrivate, want: true},
		{ip: "fd00::1", zone: zonePrivate, want: true},
		{ip: "93.184.216.34", zone: zonePrivate, want: true},
		{ip: "169.254.169.254", zone: zonePrivate},
		{ip: "fe80::1", zone: zonePrivate},
		{ip: "127.0.0.1", zone: zonePrivate},
		{ip: "0.0.0.0", zone: zonePrivate},
	}
	for _, tt := range tests {
		guard := &oauthDialGuard{zone: func() serverZone { return tt.zone }}
		assert.Equal(t, tt.want, guard.allowed(netip.MustParseAddr(tt.ip), false), "%s (server zone %d)", tt.ip, tt.zone)
	}
}

func TestServerZoneOnce(t *testing.T) {
	tests := []struct {
		host string
		want serverZone
	}{
		{host: "10.0.0.5", want: zonePrivate},
		{host: "192.168.0.10", want: zonePrivate},
		{host: "fd12::1", want: zonePrivate},
		{host: "localhost", want: zoneLoopback},
		{host: "127.0.0.1", want: zoneLoopback},
		{host: "127.0.0.2", want: zoneLoopback},
		{host: "::1", want: zoneLoopback},
		{host: "::ffff:127.0.0.1", want: zoneLoopback},
		{host: "93.184.216.34", want: zonePublic},
		{host: "169.254.169.254", want: zonePublic},
		{host: "", want: zonePublic},
		{host: "unresolvable.invalid", want: zonePublic},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, serverZoneOnce(tt.host)(), tt.host)
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

	// The server's own host is reachable wherever it resolves, here one that
	// was public when the guard was built; others are checked, redirects
	// included.
	own := &http.Client{Transport: &oauthGuardTransport{base: ts.Client(), guard: &oauthDialGuard{serverHost: "localhost", zone: func() serverZone { return zonePublic }}}}
	resp, err = own.Get(localhostURL + "/")
	require.NoError(t, err)
	_ = resp.Body.Close()
	_, err = own.Get(ts.URL + "/")
	require.ErrorIs(t, err, errOAuthNonPublicAddress)
	_, err = own.Get(localhostURL + "/redirect")
	require.ErrorIs(t, err, errOAuthNonPublicAddress, "a redirect to a non-public address is refused")
}

func TestOAuthDialGuardTrustedHostStillRefusesLinkLocal(t *testing.T) {
	guard := &oauthDialGuard{}
	for _, ip := range []string{"10.1.2.3", "127.0.0.1", "93.184.216.34", "fd00::1", "100.64.0.1"} {
		assert.True(t, guard.allowed(netip.MustParseAddr(ip), true), "%s is reachable for the server's own host", ip)
	}
	for _, ip := range []string{"169.254.169.254", "fe80::1", "0.0.0.0", "::", "224.0.0.1"} {
		assert.False(t, guard.allowed(netip.MustParseAddr(ip), true), "%s is refused for every host", ip)
	}
}

// opaqueTransport stands in for a transport the guard cannot copy.
type opaqueTransport struct{}

func (opaqueTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("must not be reached")
}

func TestOAuthHTTPClientRefusesUnsupportedTransport(t *testing.T) {
	client := oauthHTTPClient(&http.Client{Transport: opaqueTransport{}}, "https://mcp.example.com/mcp")
	_, err := client.Get("https://auth.example.com/.well-known/oauth-authorization-server")
	require.ErrorIs(t, err, errOAuthTransport)
}

func TestOAuthHTTPClientChecksTargetsBehindProxy(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Host)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(proxy.Close)
	proxyURL, err := url.Parse(proxy.URL)
	require.NoError(t, err)
	base := proxy.Client()
	transport, ok := base.Transport.(*http.Transport)
	require.True(t, ok)
	transport.Proxy = func(*http.Request) (*url.URL, error) { return proxyURL, nil }
	client := oauthHTTPClient(base, "https://mcp.example.com/mcp")

	// A public target reaches the proxy, which sits on loopback.
	resp, err := client.Get("http://93.184.216.34/.well-known/oauth-authorization-server")
	require.NoError(t, err)
	_ = resp.Body.Close()
	// A private target is refused before the proxy sees it.
	_, err = client.Get("http://10.255.255.1/token")
	require.ErrorIs(t, err, errOAuthNonPublicAddress)
	// A target on the proxy's own host is checked like any other.
	_, err = client.Get(proxy.URL + "/token")
	require.ErrorIs(t, err, errOAuthNonPublicAddress)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"93.184.216.34"}, seen)
}
