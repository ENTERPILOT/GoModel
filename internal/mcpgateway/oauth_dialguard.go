package mcpgateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/enterpilot/gomodel/internal/httpclient"
)

// errOAuthNonPublicAddress means an OAuth endpoint resolved to an address
// the gateway refuses to reach for a server's sign-in.
var errOAuthNonPublicAddress = errors.New("refusing to connect to a non-public address")

// errOAuthTransport means the upstream HTTP client sends through a transport
// the guard cannot wrap, so OAuth requests are refused rather than sent
// unchecked.
var errOAuthTransport = errors.New("mcp oauth requests need an *http.Transport to check the addresses they dial")

// nonPublicHint explains a refused OAuth endpoint.
const nonPublicHint = "OAuth endpoints must be public unless they share the MCP server's host or the MCP server is on a private network too"

// oauthDialDefaults are the dialer settings of the shared upstream client,
// whose dialer the guarded copy replaces with its own.
var oauthDialDefaults = httpclient.DefaultConfig()

// nonPublicPrefixes are ranges netip's predicates do not cover that must
// not be reachable through server-published URLs.
var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

// carrierGradeNAT is shared address space, private like RFC 1918 ranges.
var carrierGradeNAT = netip.MustParsePrefix("100.64.0.0/10")

// serverResolveTimeout bounds resolving the MCP server's own host to learn
// which zone it is in.
const serverResolveTimeout = 5 * time.Second

// serverZone is where an MCP server's host sits, which decides where its
// OAuth endpoints may sit too.
type serverZone int

const (
	zonePublic serverZone = iota
	zonePrivate
	zoneLoopback
)

// oauthHTTPClient returns base for the OAuth requests of the server at
// serverURL: discovery, client registration, and token requests. The server
// publishes the URLs those go to, so every connection is checked when it is
// dialed, redirects included: a host other than the server's own has to
// resolve to a public address, to a private one when the server itself is
// on a private network (an internal MCP server signing in through an internal
// identity provider), or to loopback when the server itself is on loopback
// for local development. Link-local addresses, cloud metadata endpoints
// among them, are refused for every host, the server's own included, so a
// DNS answer that changes after sign-in cannot reach them. Without the check
// a public, malicious server could point the gateway at internal services or
// a cloud metadata endpoint. Through a configured proxy, the target is
// resolved and checked before the proxy is used.
//
// The go-sdk's own discovery guard turns itself off for a wrapped transport
// such as the gateway's and never covers token requests, so the check lives
// here.
func oauthHTTPClient(base *http.Client, serverURL string) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	var host string
	if parsed, err := url.Parse(serverURL); err == nil {
		host = parsed.Hostname()
	}
	clone := *base
	clone.Transport = &oauthGuardTransport{
		base:  base,
		guard: &oauthDialGuard{serverHost: host, zone: serverZoneOnce(host)},
	}
	return &clone
}

// serverZoneOnce reports host's zone, resolving it on first use: loopback by
// name or address, private when it is a private address or resolves only to
// private addresses, else public. A failed lookup counts as public, the
// stricter answer.
func serverZoneOnce(host string) func() serverZone {
	return sync.OnceValue(func() serverZone {
		if isLoopbackHost(host) {
			return zoneLoopback
		}
		if ip, err := netip.ParseAddr(host); err == nil {
			if privateAddr(ip) {
				return zonePrivate
			}
			return zonePublic
		}
		if host == "" {
			return zonePublic
		}
		ctx, cancel := context.WithTimeout(context.Background(), serverResolveTimeout)
		defer cancel()
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return zonePublic
		}
		for _, ip := range ips {
			if !privateAddr(ip) {
				return zonePublic
			}
		}
		return zonePrivate
	})
}

func privateAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsPrivate() || carrierGradeNAT.Contains(ip)
}

// oauthGuardTransport sends through a copy of the base client's current
// transport that dials through the guard. The copy is rebuilt when the base
// transport changes, such as after a TLS trust reload. A base transport the
// guard cannot copy fails every request instead of sending it unchecked.
type oauthGuardTransport struct {
	base  *http.Client
	guard *oauthDialGuard

	mu      sync.Mutex
	source  *http.Transport
	guarded *http.Transport
}

func (t *oauthGuardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	transport, err := t.transport()
	if err != nil {
		return nil, err
	}
	return transport.RoundTrip(req)
}

func (t *oauthGuardTransport) CloseIdleConnections() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.guarded != nil {
		t.guarded.CloseIdleConnections()
	}
}

func (t *oauthGuardTransport) transport() (*http.Transport, error) {
	source, err := baseTransport(t.base)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.guarded != nil && t.source == source {
		return t.guarded, nil
	}
	if t.guarded != nil {
		t.guarded.CloseIdleConnections()
	}
	guarded := source.Clone()
	guarded.DialContext = t.guard.dial
	guarded.DialTLS = nil //nolint:staticcheck // SA1019: a legacy TLS dialer would bypass the guard.
	guarded.DialTLSContext = nil
	guarded.Proxy = t.guard.proxy(source.Proxy)
	t.source, t.guarded = source, guarded
	return guarded, nil
}

// baseTransport returns the *http.Transport client sends through: the one a
// gateway client currently uses, a plain one, or the default for a client
// without one.
func baseTransport(client *http.Client) (*http.Transport, error) {
	if transport, ok := httpclient.CurrentTransport(client); ok {
		return transport, nil
	}
	switch transport := client.Transport.(type) {
	case nil:
		if def, ok := http.DefaultTransport.(*http.Transport); ok {
			return def, nil
		}
	case *http.Transport:
		return transport, nil
	}
	return nil, fmt.Errorf("%w: %T", errOAuthTransport, client.Transport)
}

// oauthDialGuard checks the addresses OAuth connections dial.
type oauthDialGuard struct {
	// serverHost is the MCP server's own host, which the operator chose and
	// is reachable wherever it resolves, link-local addresses excepted.
	serverHost string
	// zone is the server host's zone, resolved once on first use.
	zone func() serverZone
	// proxies holds the host:port of each proxy the transport picked for a
	// checked target, so the dial to the proxy itself passes like the
	// server's own host. A target never passes for sharing a proxy's
	// hostname: only the dial address is compared.
	proxies sync.Map
}

// allowed reports whether ip may be dialed. trusted marks the server's own
// host and a configured proxy, which sit wherever the operator put them;
// link-local addresses, cloud metadata endpoints among them, are refused for
// every host.
func (g *oauthDialGuard) allowed(ip netip.Addr, trusted bool) bool {
	ip = ip.Unmap()
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	if trusted {
		return true
	}
	if ip.IsLoopback() {
		return g.zone() == zoneLoopback
	}
	if privateAddr(ip) {
		return g.zone() == zonePrivate
	}
	if !ip.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

// ownHost reports whether host is the MCP server's own.
func (g *oauthDialGuard) ownHost(host string) bool {
	return strings.EqualFold(host, g.serverHost)
}

func refusedAddress(host string, ip netip.Addr) error {
	return fmt.Errorf("%w: %s resolves to %s; %s", errOAuthNonPublicAddress, host, ip, nonPublicHint)
}

// dial checks the address each connection actually goes to, after name
// resolution, so a host cannot resolve to a public address for a check and
// a private one for the connection.
func (g *oauthDialGuard) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	_, viaProxy := g.proxies.Load(strings.ToLower(address))
	trusted := viaProxy || g.ownHost(host)
	dialer := &net.Dialer{Timeout: oauthDialDefaults.DialTimeout, KeepAlive: oauthDialDefaults.KeepAlive}
	dialer.Control = func(_, resolved string, _ syscall.RawConn) error {
		addr, err := netip.ParseAddrPort(resolved)
		if err != nil {
			return fmt.Errorf("%w: %s", errOAuthNonPublicAddress, host)
		}
		if !g.allowed(addr.Addr(), trusted) {
			return refusedAddress(host, addr.Addr())
		}
		return nil
	}
	return dialer.DialContext(ctx, network, address)
}

// proxy wraps the transport's proxy selection: a request that goes through
// a proxy has its target checked here, since the dial only sees the proxy.
// The proxy resolves the target on its own, so the check reflects the
// gateway's view of DNS; a proxy that reaches internal networks should not
// front a gateway that signs in to servers it does not trust.
func (g *oauthDialGuard) proxy(next func(*http.Request) (*url.URL, error)) func(*http.Request) (*url.URL, error) {
	if next == nil {
		return nil
	}
	return func(req *http.Request) (*url.URL, error) {
		proxyURL, err := next(req)
		if err != nil || proxyURL == nil {
			return proxyURL, err
		}
		if err := g.checkTarget(req.Context(), req.URL.Hostname()); err != nil {
			return nil, err
		}
		g.proxies.Store(proxyDialAddress(proxyURL), struct{}{})
		return proxyURL, nil
	}
}

// proxyDialAddress is the host:port the transport dials for proxyURL.
func proxyDialAddress(proxyURL *url.URL) string {
	port := proxyURL.Port()
	if port == "" {
		switch proxyURL.Scheme {
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		default:
			port = "80"
		}
	}
	return strings.ToLower(net.JoinHostPort(proxyURL.Hostname(), port))
}

// checkTarget resolves host and checks every address, as dial would.
func (g *oauthDialGuard) checkTarget(ctx context.Context, host string) error {
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", host, err)
	}
	trusted := g.ownHost(host)
	for _, ip := range ips {
		if !g.allowed(ip, trusted) {
			return refusedAddress(host, ip)
		}
	}
	return nil
}
