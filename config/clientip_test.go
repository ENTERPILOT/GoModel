package config

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveClientIPPolicy covers the operator-facing shape of the settings:
// off unless configured, normalized when set, presets expanded, and a startup
// error on a combination that cannot describe a deployment.
func TestResolveClientIPPolicy(t *testing.T) {
	tests := []struct {
		name       string
		cfg        ServerConfig
		wantNets   []string
		wantHeader string
		wantHops   int
		wantErr    string
	}{
		{
			name:     "unset trusts nothing",
			cfg:      ServerConfig{},
			wantNets: nil,
		},
		{
			name:       "networks are normalized and deduplicated",
			cfg:        ServerConfig{TrustedProxies: []string{" 10.0.0.0/8 ", "127.0.0.1", "10.0.0.0/8", ""}},
			wantNets:   []string{"10.0.0.0/8", "127.0.0.1/32"},
			wantHeader: ClientIPHeaderForwardedFor,
		},
		{
			name:       "a bare IPv6 address becomes a single host",
			cfg:        ServerConfig{TrustedProxies: []string{"2001:db8::5"}},
			wantNets:   []string{"2001:db8::5/128"},
			wantHeader: ClientIPHeaderForwardedFor,
		},
		{
			name:       "a network is masked to its base address",
			cfg:        ServerConfig{TrustedProxies: []string{"10.42.7.9/16"}},
			wantNets:   []string{"10.42.0.0/16"},
			wantHeader: ClientIPHeaderForwardedFor,
		},
		{
			name:       "presets expand and compose with explicit networks",
			cfg:        ServerConfig{TrustedProxies: []string{"Loopback", "10.42.0.0/16", "private"}},
			wantNets:   []string{"127.0.0.0/8", "::1/128", "10.42.0.0/16", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"},
			wantHeader: ClientIPHeaderForwardedFor,
		},
		{
			name:       "a single-address header is canonicalized",
			cfg:        ServerConfig{TrustedProxies: []string{"10.0.0.0/8"}, ClientIPHeader: "cf-connecting-ip"},
			wantNets:   []string{"10.0.0.0/8"},
			wantHeader: "Cf-Connecting-Ip",
		},
		{
			name:       "hops compose with the network list",
			cfg:        ServerConfig{TrustedProxies: []string{"10.0.0.0/8"}, TrustedHops: 2},
			wantNets:   []string{"10.0.0.0/8"},
			wantHeader: ClientIPHeaderForwardedFor,
			wantHops:   2,
		},
		{
			name:    "an unusable network is rejected",
			cfg:     ServerConfig{TrustedProxies: []string{"10.0.0.0/33"}},
			wantErr: "trusted_proxies",
		},
		{
			name:    "a header without trusted networks is rejected",
			cfg:     ServerConfig{ClientIPHeader: "X-Real-IP"},
			wantErr: "server.trusted_proxies is empty",
		},
		{
			name:    "hops without trusted networks are rejected",
			cfg:     ServerConfig{TrustedHops: 1},
			wantErr: "server.trusted_proxies is empty",
		},
		{
			name:    "hops on a single-address header are rejected",
			cfg:     ServerConfig{TrustedProxies: []string{"10.0.0.0/8"}, ClientIPHeader: "X-Real-IP", TrustedHops: 1},
			wantErr: "carries a single address",
		},
		{
			name:    "negative hops are rejected",
			cfg:     ServerConfig{TrustedProxies: []string{"10.0.0.0/8"}, TrustedHops: -1},
			wantErr: "trusted_hops must be 0",
		},
		{
			name:    "the RFC 7239 header is rejected rather than misread",
			cfg:     ServerConfig{TrustedProxies: []string{"10.0.0.0/8"}, ClientIPHeader: "forwarded"},
			wantErr: "not supported",
		},
		{
			name:    "an invalid header name is rejected",
			cfg:     ServerConfig{TrustedProxies: []string{"10.0.0.0/8"}, ClientIPHeader: "X Real IP"},
			wantErr: "client_ip_header",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			err := ResolveClientIPPolicy(&cfg)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantNets, cfg.TrustedProxies)
			assert.Equal(t, tt.wantHeader, cfg.ClientIP.Header)
			assert.Equal(t, tt.wantHops, cfg.ClientIP.Hops)
			assert.Equal(t, len(tt.wantNets) > 0, cfg.ClientIP.Enabled())
		})
	}
}

// TestResolveClientIPPolicyMappedIPv6 pins the handling of IPv4-mapped
// networks, which are the ones an operator can write believing a proxy network
// is trusted while it silently matches nothing.
func TestResolveClientIPPolicyMappedIPv6(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		want    []string
		wantErr string
	}{
		{
			name: "mapped prefix inside the embedded address becomes the IPv4 network",
			in:   []string{"::ffff:10.0.0.0/120"},
			want: []string{"10.0.0.0/24"},
		},
		{
			name: "mapped /96 is the whole IPv4 space",
			in:   []string{"::ffff:0:0/96"},
			want: []string{"0.0.0.0/0"},
		},
		{
			name: "a mapped bare address becomes an IPv4 host",
			in:   []string{"::ffff:10.0.0.1"},
			want: []string{"10.0.0.1/32"},
		},
		{
			name:    "mapped prefix below /96 reaches past the embedded address",
			in:      []string{"::ffff:10.0.0.0/8"},
			wantErr: "shorter than /96",
		},
		{
			name: "plain IPv4 and IPv6 networks are untouched",
			in:   []string{"10.0.0.0/8", "2001:db8::/32", "::1"},
			want: []string{"10.0.0.0/8", "2001:db8::/32", "::1/128"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ServerConfig{TrustedProxies: tt.in}
			err := ResolveClientIPPolicy(&cfg)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.TrustedProxies)
		})
	}
}

// TestClientIPPolicyResolve pins which address a request is attributed to,
// given the socket peer and whatever the configured header carries, so a value
// a client controls is never recorded in place of the address the operator's
// proxies observed.
func TestClientIPPolicyResolve(t *testing.T) {
	tests := []struct {
		name    string
		proxies []string
		header  string
		hops    int
		remote  string
		values  []string
		wantIP  string
	}{
		{
			name:   "no trusted proxies ignores the header",
			remote: "203.0.113.7:4321",
			values: []string{"198.51.100.8"},
			wantIP: "203.0.113.7",
		},
		{
			name:    "untrusted socket peer ignores the header",
			proxies: []string{"10.0.0.0/8"},
			remote:  "203.0.113.7:4321",
			values:  []string{"198.51.100.8"},
			wantIP:  "203.0.113.7",
		},
		{
			name:    "trusted peer without header keeps socket IP",
			proxies: []string{"127.0.0.0/8"},
			remote:  "127.0.0.1:4321",
			wantIP:  "127.0.0.1",
		},
		{
			name:    "nearest hop written by the operator wins",
			proxies: []string{"127.0.0.0/8"},
			remote:  "127.0.0.1:4321",
			values:  []string{"203.0.113.9, 198.51.100.7"},
			wantIP:  "198.51.100.7",
		},
		{
			name:    "split header values are read in order",
			proxies: []string{"127.0.0.0/8"},
			remote:  "127.0.0.1:4321",
			values:  []string{"203.0.113.9", "198.51.100.7"},
			wantIP:  "198.51.100.7",
		},
		{
			name:    "a client cannot overwrite the chain it sends",
			proxies: []string{"10.2.0.0/16"},
			remote:  "10.2.0.5:4321",
			values:  []string{"198.51.100.99, 203.0.113.9, 10.2.0.5"},
			wantIP:  "203.0.113.9",
		},
		{
			name:    "an all-trusted chain cannot forge the recorded client",
			proxies: []string{"10.0.0.0/8"},
			remote:  "10.0.0.9:4321",
			values:  []string{"10.66.66.66, 10.2.2.2"},
			wantIP:  "10.0.0.9",
		},
		{
			name:    "an internal client behind a narrowly listed proxy keeps its real address",
			proxies: []string{"10.0.0.0/24"},
			remote:  "10.0.0.9:4321",
			values:  []string{"10.20.30.40, 10.0.0.9"},
			wantIP:  "10.20.30.40",
		},
		{
			name:    "a home lab trusts its cluster without trusting its clients",
			proxies: []string{"10.42.0.0/16"},
			remote:  "10.42.0.7:4321",
			values:  []string{"192.168.1.50, 10.42.0.7"},
			wantIP:  "192.168.1.50",
		},
		{
			name:    "unparseable hop falls back to the socket IP",
			proxies: []string{"127.0.0.0/8"},
			remote:  "127.0.0.1:4321",
			values:  []string{"203.0.113.9, spoofed"},
			wantIP:  "127.0.0.1",
		},
		{
			name:    "bracketed IPv6 is normalised",
			proxies: []string{"127.0.0.0/8"},
			remote:  "127.0.0.1:4321",
			values:  []string{"2001:db8::1, [2001:db8::2]"},
			wantIP:  "2001:db8::2",
		},
		{
			name:    "an IPv4-mapped hop is reported in its IPv4 form",
			proxies: []string{"127.0.0.0/8"},
			remote:  "127.0.0.1:4321",
			values:  []string{"::ffff:198.51.100.7"},
			wantIP:  "198.51.100.7",
		},
		{
			name:    "loopback proxy hop is only trusted when listed",
			proxies: []string{"127.0.0.0/8"},
			remote:  "127.0.0.1:4321",
			values:  []string{"203.0.113.9, 127.0.0.1"},
			wantIP:  "203.0.113.9",
		},
		{
			name:    "a zone-bearing link-local proxy is still recognized",
			proxies: []string{"fe80::/10"},
			remote:  "[fe80::1%eth0]:4321",
			values:  []string{"203.0.113.9, fe80::1"},
			wantIP:  "203.0.113.9",
		},
		{
			name:    "socket address without port is accepted",
			proxies: []string{"10.0.0.0/8"},
			remote:  "10.0.0.3",
			values:  []string{"198.51.100.4"},
			wantIP:  "198.51.100.4",
		},
		{
			name:    "the private preset covers an unpredictable ingress address",
			proxies: []string{"private"},
			remote:  "172.20.4.9:4321",
			values:  []string{"203.0.113.9"},
			wantIP:  "203.0.113.9",
		},
		{
			name:    "a single-address header is taken verbatim",
			proxies: []string{"10.0.0.0/8"},
			header:  "CF-Connecting-IP",
			remote:  "10.0.0.3:4321",
			values:  []string{"203.0.113.9"},
			wantIP:  "203.0.113.9",
		},
		{
			name:    "a single-address header inside the trusted network is still the client",
			proxies: []string{"10.0.0.0/8"},
			header:  "X-Real-IP",
			remote:  "10.0.0.3:4321",
			values:  []string{"10.9.9.9"},
			wantIP:  "10.9.9.9",
		},
		{
			name:    "a missing single-address header falls back to the socket IP",
			proxies: []string{"10.0.0.0/8"},
			header:  "X-Real-IP",
			remote:  "10.0.0.3:4321",
			wantIP:  "10.0.0.3",
		},
		{
			name:    "one hop selects the rightmost entry",
			proxies: []string{"10.0.0.0/8"},
			hops:    1,
			remote:  "10.0.0.3:4321",
			values:  []string{"198.51.100.99, 203.0.113.9"},
			wantIP:  "203.0.113.9",
		},
		{
			name:    "two hops reach past a rotating edge inside the trusted range",
			proxies: []string{"10.0.0.0/8"},
			hops:    2,
			remote:  "10.0.0.3:4321",
			values:  []string{"203.0.113.9, 10.7.7.7"},
			wantIP:  "203.0.113.9",
		},
		{
			name:    "a chain shorter than the configured depth falls back to the socket IP",
			proxies: []string{"10.0.0.0/8"},
			hops:    3,
			remote:  "10.0.0.3:4321",
			values:  []string{"203.0.113.9, 10.7.7.7"},
			wantIP:  "10.0.0.3",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ServerConfig{TrustedProxies: tt.proxies, ClientIPHeader: tt.header, TrustedHops: tt.hops}
			require.NoError(t, ResolveClientIPPolicy(&cfg))

			header := tt.header
			if header == "" {
				header = ClientIPHeaderForwardedFor
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			req.RemoteAddr = tt.remote
			for _, value := range tt.values {
				req.Header.Add(header, value)
			}

			assert.Equal(t, tt.wantIP, cfg.ClientIP.Resolve(req))
		})
	}
}

// TestClientIPPolicyGuards checks the policy reports an address rather than
// panicking when asked about a request it cannot read, which is what an
// unconfigured deployment or a peer that is not an IP amounts to.
func TestClientIPPolicyGuards(t *testing.T) {
	var unset ClientIPPolicy
	assert.False(t, unset.Enabled())
	assert.False(t, unset.Trusts(netip.MustParseAddr("10.0.0.1")))
	assert.False(t, unset.Trusts(netip.Addr{}))
	assert.Empty(t, unset.Resolve(nil))

	cfg := ServerConfig{TrustedProxies: []string{"10.0.0.0/8"}}
	require.NoError(t, ResolveClientIPPolicy(&cfg))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.RemoteAddr = "@"
	req.Header.Set(ClientIPHeaderForwardedFor, "203.0.113.9")
	assert.Equal(t, "@", cfg.ClientIP.Resolve(req), "a peer that is not an address cannot be trusted with a header")
}

// TestLoadClientIPSettings checks the settings reach the resolved policy
// through the environment, since that is how a container deployment sets them.
func TestLoadClientIPSettings(t *testing.T) {
	clearAllConfigEnvVars(t)

	withTempDir(t, func(string) {
		result, err := Load()
		require.NoError(t, err)
		require.Empty(t, result.Config.Server.TrustedProxies, "forwarding headers must be ignored unless the operator lists proxy networks")
		require.False(t, result.Config.Server.ClientIP.Enabled())

		t.Setenv("SERVER_TRUSTED_PROXIES", " 10.42.0.0/16 , loopback ")
		t.Setenv("SERVER_CLIENT_IP_HEADER", "CF-Connecting-IP")
		result, err = Load()
		require.NoError(t, err)
		require.Equal(t, []string{"10.42.0.0/16", "127.0.0.0/8", "::1/128"}, result.Config.Server.TrustedProxies)
		require.Equal(t, "Cf-Connecting-Ip", result.Config.Server.ClientIP.Header)

		t.Setenv("SERVER_TRUSTED_PROXIES", "not-a-network")
		_, err = Load()
		require.Error(t, err)
		require.Contains(t, err.Error(), "trusted_proxies")
	})
}
