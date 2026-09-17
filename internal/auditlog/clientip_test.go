package auditlog

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseTrustedProxies covers how configured proxy networks are compiled,
// including the single-host form and the skipping of values the config loader
// is expected to have rejected already.
func TestParseTrustedProxies(t *testing.T) {
	t.Run("empty config trusts nothing", func(t *testing.T) {
		assert.Nil(t, ParseTrustedProxies(nil))
		assert.Nil(t, ParseTrustedProxies([]string{"  ", ""}))
	})

	t.Run("bare addresses become single-host networks", func(t *testing.T) {
		proxies := ParseTrustedProxies([]string{"10.0.0.5", " 2001:db8::5 "})
		require.NotNil(t, proxies)
		assert.True(t, proxies.Contains(parseTestIP(t, "10.0.0.5")))
		assert.False(t, proxies.Contains(parseTestIP(t, "10.0.0.6")))
		assert.True(t, proxies.Contains(parseTestIP(t, "2001:db8::5")))
	})

	t.Run("invalid entries are skipped", func(t *testing.T) {
		proxies := ParseTrustedProxies([]string{"not-an-ip", "10.0.0.0/8"})
		require.NotNil(t, proxies)
		assert.True(t, proxies.Contains(parseTestIP(t, "10.1.2.3")))
	})
}

// TestTrustedProxiesClientIP pins which address an audit entry is tagged with,
// given the socket peer and any X-Forwarded-For chain, so a value a client
// controls is never recorded in place of the address the operator's proxies
// observed.
func TestTrustedProxiesClientIP(t *testing.T) {
	tests := []struct {
		name   string
		cidrs  []string
		remote string
		xff    []string
		wantIP string
	}{
		{
			name:   "no trusted proxies ignores the header",
			remote: "203.0.113.7:4321",
			xff:    []string{"198.51.100.8"},
			wantIP: "203.0.113.7",
		},
		{
			name:   "untrusted socket peer ignores the header",
			cidrs:  []string{"10.0.0.0/8"},
			remote: "203.0.113.7:4321",
			xff:    []string{"198.51.100.8"},
			wantIP: "203.0.113.7",
		},
		{
			name:   "trusted peer without header keeps socket IP",
			cidrs:  []string{"127.0.0.0/8"},
			remote: "127.0.0.1:4321",
			wantIP: "127.0.0.1",
		},
		{
			name:   "nearest hop written by the operator wins",
			cidrs:  []string{"127.0.0.0/8"},
			remote: "127.0.0.1:4321",
			xff:    []string{"203.0.113.9, 198.51.100.7"},
			wantIP: "198.51.100.7",
		},
		{
			name:   "split header values are read in order",
			cidrs:  []string{"127.0.0.0/8"},
			remote: "127.0.0.1:4321",
			xff:    []string{"203.0.113.9", "198.51.100.7"},
			wantIP: "198.51.100.7",
		},
		{
			name:   "a client cannot overwrite the chain it sends",
			cidrs:  []string{"10.2.0.0/16"},
			remote: "10.2.0.5:4321",
			xff:    []string{"198.51.100.99, 203.0.113.9, 10.2.0.5"},
			wantIP: "203.0.113.9",
		},
		{
			name:   "an all-trusted chain cannot forge the recorded client",
			cidrs:  []string{"10.0.0.0/8"},
			remote: "10.0.0.9:4321",
			xff:    []string{"10.66.66.66, 10.2.2.2"},
			wantIP: "10.0.0.9",
		},
		{
			name:   "an internal client behind a narrowly listed proxy keeps its real address",
			cidrs:  []string{"10.0.0.0/24"},
			remote: "10.0.0.9:4321",
			xff:    []string{"10.20.30.40, 10.0.0.9"},
			wantIP: "10.20.30.40",
		},
		{
			name:   "unparseable hop falls back to the socket IP",
			cidrs:  []string{"127.0.0.0/8"},
			remote: "127.0.0.1:4321",
			xff:    []string{"203.0.113.9, spoofed"},
			wantIP: "127.0.0.1",
		},
		{
			name:   "bracketed IPv6 is normalised",
			cidrs:  []string{"127.0.0.0/8"},
			remote: "127.0.0.1:4321",
			xff:    []string{"2001:db8::1, [2001:db8::2]"},
			wantIP: "2001:db8::2",
		},
		{
			name:   "loopback proxy hop is only trusted when listed",
			cidrs:  []string{"127.0.0.0/8"},
			remote: "127.0.0.1:4321",
			xff:    []string{"203.0.113.9, 127.0.0.1"},
			wantIP: "203.0.113.9",
		},
		{
			name:   "a zone-bearing link-local proxy is still recognized",
			cidrs:  []string{"fe80::/10"},
			remote: "[fe80::1%eth0]:4321",
			xff:    []string{"203.0.113.9, fe80::1"},
			wantIP: "203.0.113.9",
		},
		{
			name:   "socket address without port is accepted",
			cidrs:  []string{"10.0.0.0/8"},
			remote: "10.0.0.3",
			xff:    []string{"198.51.100.4"},
			wantIP: "198.51.100.4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proxies := ParseTrustedProxies(tt.cidrs)
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			req.RemoteAddr = tt.remote
			for _, value := range tt.xff {
				req.Header.Add("X-Forwarded-For", value)
			}

			assert.Equal(t, tt.wantIP, proxies.ClientIP(req, socketIP(req).String()))
		})
	}
}

// TestMiddlewareClientIPTagging checks the resolver is actually wired into the
// audit middleware, on both a directly exposed gateway and one behind a trusted
// proxy network.
func TestMiddlewareClientIPTagging(t *testing.T) {
	tests := []struct {
		name   string
		cidrs  []string
		remote string
		xff    string
		wantIP string
	}{
		{
			name:   "direct exposure records the socket peer",
			remote: "203.0.113.7:4321",
			xff:    "198.51.100.8",
			wantIP: "203.0.113.7",
		},
		{
			name:   "behind a trusted proxy records the forwarded client",
			cidrs:  []string{"127.0.0.0/8"},
			remote: "127.0.0.1:4321",
			xff:    "203.0.113.9, 198.51.100.7",
			wantIP: "198.51.100.7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := &capturingLogger{cfg: Config{
				Enabled:               true,
				OnlyModelInteractions: true,
				TrustedProxies:        ParseTrustedProxies(tt.cidrs),
			}}

			handler := Middleware(logger)(func(*echo.Context) error {
				return nil
			})

			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			req.RemoteAddr = tt.remote
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			c := e.NewContext(req, httptest.NewRecorder())

			require.NoError(t, handler(c))
			require.Len(t, logger.entries, 1)
			assert.Equal(t, tt.wantIP, logger.entries[0].ClientIP)
		})
	}
}

// parseTestIP turns a literal address into the form a trusted proxy network is
// checked against.
// TestTrustedProxiesContainsGuards checks the resolver reports "not trusted"
// rather than panicking when asked about an address it cannot have, which is
// what an unconfigured deployment or an unparseable peer amounts to.
func TestTrustedProxiesContainsGuards(t *testing.T) {
	var unset *TrustedProxies
	assert.False(t, unset.Contains(parseTestIP(t, "10.0.0.1")))

	configured := ParseTrustedProxies([]string{"10.0.0.0/8"})
	require.NotNil(t, configured)
	assert.False(t, configured.Contains(nil))
}

func parseTestIP(t *testing.T, value string) net.IP {
	t.Helper()
	ip := net.ParseIP(value)
	require.NotNil(t, ip)
	return ip
}
