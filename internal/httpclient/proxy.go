package httpclient

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// proxyContextKey carries a per-request forward proxy through the request
// context. Provider adapters build requests with the context the gateway hands
// them, so attaching the proxy there reaches every client created by
// NewHTTPClient without each adapter knowing about proxies.
type proxyContextKey struct{}

// ContextWithProxy returns a context whose HTTP requests, when sent through a
// client built by NewHTTPClient, are routed through proxy. A nil proxy leaves
// ctx unchanged.
func ContextWithProxy(ctx context.Context, proxy *url.URL) context.Context {
	if proxy == nil {
		return ctx
	}
	return context.WithValue(ctx, proxyContextKey{}, proxy)
}

// ProxyFromContext returns the proxy attached by ContextWithProxy, or nil.
func ProxyFromContext(ctx context.Context) *url.URL {
	if ctx == nil {
		return nil
	}
	proxy, _ := ctx.Value(proxyContextKey{}).(*url.URL)
	return proxy
}

// proxyForRequest is the transport's Proxy callback: a proxy attached to the
// request context wins; otherwise the HTTP_PROXY / HTTPS_PROXY / NO_PROXY
// environment applies as before.
func proxyForRequest(req *http.Request) (*url.URL, error) {
	if proxy := ProxyFromContext(req.Context()); proxy != nil {
		return proxy, nil
	}
	return http.ProxyFromEnvironment(req)
}

// proxySchemes lists the forward-proxy schemes net/http can dial through.
var proxySchemes = []string{"http", "https", "socks5", "socks5h"}

// ParseProxyURL validates a forward proxy URL such as
// "http://proxy.internal:3128", "http://user:pass@proxy:3128" or
// "socks5://127.0.0.1:1080". An empty or whitespace-only value means "no
// proxy" and returns nil.
func ParseProxyURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	proxy, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy URL: %w", err)
	}
	scheme := strings.ToLower(proxy.Scheme)
	if !slices.Contains(proxySchemes, scheme) {
		return nil, fmt.Errorf("proxy URL scheme must be one of %s", strings.Join(proxySchemes, ", "))
	}
	if proxy.Host == "" {
		return nil, fmt.Errorf("proxy URL must include a host")
	}
	proxy.Scheme = scheme
	return proxy, nil
}

// RedactProxyURL returns raw with any password replaced by "xxxxx", for
// admin views and logs. Values that do not parse are returned unchanged
// (they carry no credential net/url can find).
func RedactProxyURL(raw string) string {
	proxy, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || proxy.User == nil {
		return raw
	}
	return proxy.Redacted()
}
