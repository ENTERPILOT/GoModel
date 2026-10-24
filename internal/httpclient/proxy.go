package httpclient

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
)

// proxySchemes lists the outbound proxy schemes net/http dials natively:
// plain and TLS HTTP CONNECT proxies, and SOCKS5 with optional user/password
// authentication. "socks5h" is accepted as a synonym of "socks5"; net/http
// resolves the upstream hostname on the proxy for both.
var proxySchemes = []string{"http", "https", "socks5", "socks5h"}

// ParseProxyURL validates an outbound proxy URL such as
// "http://proxy.internal:3128" or "socks5://user:pass@10.0.0.1:1080". The
// error message never echoes the URL, which may carry credentials.
func ParseProxyURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("proxy URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("proxy URL is not a valid URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if !slices.Contains(proxySchemes, scheme) {
		return nil, fmt.Errorf("proxy URL scheme must be one of %s", strings.Join(proxySchemes, ", "))
	}
	u.Scheme = scheme
	if u.Hostname() == "" {
		return nil, fmt.Errorf("proxy URL must include a host")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("proxy URL must not carry a path, query, or fragment")
	}
	u.Path = ""
	// net/http fills in 80/443 for HTTP proxies but dials a SOCKS5 host
	// verbatim, so give SOCKS5 its conventional port here.
	if u.Port() == "" && (scheme == "socks5" || scheme == "socks5h") {
		u.Host = net.JoinHostPort(u.Hostname(), "1080")
	}
	return u, nil
}

// redactedProxyURL stands in for any proxy value that does not validate: a
// misparsed value can hide credentials where url.URL.Redacted would not look
// (an opaque "user:pass@host:1080" parses with scheme "user"), so nothing of
// it is shown.
const redactedProxyURL = "***********"

// RedactProxyURL returns a valid proxy URL with any password replaced by
// "xxxxx", for logs and admin views, and a fixed mask for anything else.
func RedactProxyURL(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	u, err := ParseProxyURL(raw)
	if err != nil {
		return redactedProxyURL
	}
	return u.Redacted()
}
