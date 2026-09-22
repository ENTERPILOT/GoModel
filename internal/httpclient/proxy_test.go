package httpclient

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseProxyURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr string
	}{
		{name: "http", raw: "http://proxy.internal:3128", want: "http://proxy.internal:3128"},
		{name: "https with trailing slash", raw: "https://proxy.internal:3129/", want: "https://proxy.internal:3129"},
		{name: "socks5 with credentials", raw: "socks5://user:pass@10.0.0.1:1080", want: "socks5://user:pass@10.0.0.1:1080"},
		{name: "socks5h", raw: "socks5h://proxy:1080", want: "socks5h://proxy:1080"},
		{name: "socks5 without a port gets 1080", raw: "socks5://user:pass@proxy", want: "socks5://user:pass@proxy:1080"},
		{name: "http without a port is left to net/http", raw: "http://proxy", want: "http://proxy"},
		{name: "scheme is case-insensitive", raw: "SOCKS5://proxy:1080", want: "socks5://proxy:1080"},
		{name: "surrounding whitespace", raw: "  http://proxy:3128  ", want: "http://proxy:3128"},
		{name: "empty", raw: "   ", wantErr: "empty"},
		{name: "unsupported scheme", raw: "ftp://proxy:21", wantErr: "scheme must be one of"},
		{name: "missing scheme", raw: "proxy.internal:3128", wantErr: "scheme must be one of"},
		{name: "missing host", raw: "http://", wantErr: "must include a host"},
		{name: "path is rejected", raw: "http://proxy:3128/upstream", wantErr: "path, query, or fragment"},
		{name: "query is rejected", raw: "http://proxy:3128?x=1", wantErr: "path, query, or fragment"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseProxyURL(tt.raw)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.NotContains(t, err.Error(), "pass", "error must not echo credentials")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.String())
		})
	}
}

func TestRedactProxyURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "", want: ""},
		{name: "no credentials", raw: "http://proxy:3128", want: "http://proxy:3128"},
		{name: "password masked", raw: "socks5://user:s3cret@proxy:1080", want: "socks5://user:xxxxx@proxy:1080"},
		{name: "user only kept", raw: "http://user@proxy:3128", want: "http://user@proxy:3128"},
		{name: "unparseable with userinfo is fully masked", raw: "http://user:p@ss@%zz", want: "***********"},
		// url.Parse reads this as scheme "user" with an opaque part, where
		// Redacted() would leave the password visible.
		{name: "scheme-less value is fully masked", raw: "user:pass@10.0.0.1:1080", want: "***********"},
		{name: "unsupported scheme is fully masked", raw: "ftp://user:pass@proxy:21", want: "***********"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, RedactProxyURL(tt.raw))
		})
	}
}

func TestNewHTTPClient_UsesConfiguredProxy(t *testing.T) {
	proxy, err := url.Parse("http://proxy.internal:3128")
	require.NoError(t, err)
	cfg := DefaultConfig()
	cfg.Proxy = http.ProxyURL(proxy)

	client := NewHTTPClient(&cfg)
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.Proxy)

	req, err := http.NewRequest(http.MethodGet, "https://api.example.com/v1/models", nil)
	require.NoError(t, err)
	got, err := transport.Proxy(req)
	require.NoError(t, err)
	assert.Equal(t, proxy.String(), got.String())
}

func TestNewHTTPClient_KeepsAnEnvironmentProxyByDefault(t *testing.T) {
	client := NewDefaultHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	// The environment is read once per process, so only the presence of the
	// fallback is asserted here; its behaviour is net/http's own.
	assert.NotNil(t, transport.Proxy)
}
