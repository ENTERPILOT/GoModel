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

// nonPublicHint explains a refused OAuth endpoint.
const nonPublicHint = "OAuth endpoints must be public unless they share the MCP server's host or the MCP server is on a private network too"

// oauthDialTimeout and oauthKeepAlive match the shared upstream client.
const (
	oauthDialTimeout = 30 * time.Second
	oauthKeepAlive   = 30 * time.Second
)

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
// whether it is on a private network.
const serverResolveTimeout = 5 * time.Second

// oauthHTTPClient returns base for the OAuth requests of the server at
// serverURL: discovery, client registration, and token requests. The server
// publishes the URLs those go to, so every connection is checked when it is
// dialed, redirects included: a host other than the server's own has to
// resolve to a public address, to a private one when the server itself is
// on a private network (an internal MCP server signing in through an internal
// identity provider), or to loopback when the server itself is on loopback
// for local development. Link-local addresses, cloud metadata endpoints
// among them, are never reached. Without the check a public, malicious
// server could point the gateway at internal services or a cloud metadata
// endpoint. Through a configured proxy, the target is resolved and checked
// before the proxy is used.
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
		guard: &oauthDialGuard{serverHost: host, allowLoopback: loopbackHost(host), serverPrivate: privateHostOnce(host)},
	}
	return &clone
}

// privateHostOnce reports whether host is a private address or resolves only
// to private addresses, resolving it on first use. A failed lookup counts as
// public, the stricter answer.
func privateHostOnce(host string) func() bool {
	return sync.OnceValue(func() bool {
		if ip, err := netip.ParseAddr(host); err == nil {
			return privateAddr(ip)
		}
		if host == "" {
			return false
		}
		ctx, cancel := context.WithTimeout(context.Background(), serverResolveTimeout)
		defer cancel()
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return false
		}
		for _, ip := range ips {
			if !privateAddr(ip) {
				return false
			}
		}
		return true
	})
}

func privateAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsPrivate() || carrierGradeNAT.Contains(ip)
}

func loopbackHost(host string) bool {
	if isLoopbackHost(host) {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}

// oauthGuardTransport sends through a copy of the base client's current
// transport that dials through the guard. The copy is rebuilt when the base
// transport changes, such as after a TLS trust reload. A base transport
// other than *http.Transport is replaced by a copy of the default one.
type oauthGuardTransport struct {
	base  *http.Client
	guard *oauthDialGuard

	mu      sync.Mutex
	source  *http.Transport
	guarded *http.Transport
}

func (t *oauthGuardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.transport().RoundTrip(req)
}

func (t *oauthGuardTransport) CloseIdleConnections() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.guarded != nil {
		t.guarded.CloseIdleConnections()
	}
}

func (t *oauthGuardTransport) transport() *http.Transport {
	source := baseTransport(t.base)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.guarded != nil && t.source == source {
		return t.guarded
	}
	if t.guarded != nil {
		t.guarded.CloseIdleConnections()
	}
	guarded := source.Clone()
	guarded.DialContext = t.guard.dial
	guarded.DialTLSContext = nil
	guarded.Proxy = t.guard.proxy(source.Proxy)
	t.source, t.guarded = source, guarded
	return guarded
}

func baseTransport(client *http.Client) *http.Transport {
	if transport, ok := httpclient.CurrentTransport(client); ok {
		return transport
	}
	if transport, ok := client.Transport.(*http.Transport); ok {
		return transport
	}
	return http.DefaultTransport.(*http.Transport)
}

// oauthDialGuard checks the addresses OAuth connections dial.
type oauthDialGuard struct {
	// serverHost is the MCP server's own host, which the operator chose and
	// is reachable wherever it resolves.
	serverHost    string
	allowLoopback bool
	// serverPrivate reports, resolved once, whether serverHost is on a
	// private network.
	serverPrivate func() bool
	// proxies holds the proxy hosts the transport picked for checked
	// targets; dials to them are not checked again.
	proxies sync.Map
}

func (g *oauthDialGuard) trusted(host string) bool {
	if strings.EqualFold(host, g.serverHost) {
		return true
	}
	_, ok := g.proxies.Load(strings.ToLower(host))
	return ok
}

// allowed reports whether ip may be dialed for a host other than the
// server's own.
func (g *oauthDialGuard) allowed(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() {
		return g.allowLoopback
	}
	if privateAddr(ip) {
		return g.serverPrivate != nil && g.serverPrivate()
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

// dial checks the address each connection actually goes to, after name
// resolution, so a host cannot resolve to a public address for a check and
// a private one for the connection.
func (g *oauthDialGuard) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: oauthDialTimeout, KeepAlive: oauthKeepAlive}
	if !g.trusted(host) {
		dialer.Control = func(_, resolved string, _ syscall.RawConn) error {
			addr, err := netip.ParseAddrPort(resolved)
			if err != nil {
				return fmt.Errorf("%w: %s", errOAuthNonPublicAddress, host)
			}
			if !g.allowed(addr.Addr()) {
				return fmt.Errorf("%w: %s resolves to %s; %s", errOAuthNonPublicAddress, host, addr.Addr(), nonPublicHint)
			}
			return nil
		}
	}
	return dialer.DialContext(ctx, network, address)
}

// proxy wraps the transport's proxy selection: a request that goes through
// a proxy has its target checked here, since the dial only sees the proxy.
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
		g.proxies.Store(strings.ToLower(proxyURL.Hostname()), struct{}{})
		return proxyURL, nil
	}
}

func (g *oauthDialGuard) checkTarget(ctx context.Context, host string) error {
	if g.trusted(host) {
		return nil
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", host, err)
	}
	for _, ip := range ips {
		if !g.allowed(ip) {
			return fmt.Errorf("%w: %s resolves to %s; %s", errOAuthNonPublicAddress, host, ip, nonPublicHint)
		}
	}
	return nil
}
