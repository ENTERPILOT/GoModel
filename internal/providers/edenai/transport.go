package edenai

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/enterpilot/gomodel/internal/httpclient"
)

// maxRedirects matches net/http's own default redirect budget. Installing a
// CheckRedirect replaces that default wholesale, so the cap has to be
// reasserted here or Eden requests would follow redirect chains forever.
const maxRedirects = 10

// credentialSafeURL reports whether Eden's bearer token may be put on the wire
// for u.
//
// Eden is a hosted service reached over TLS, so a cleartext destination would
// expose the gateway's Eden key to anyone on the path. Loopback is the one
// exemption: traffic to the local machine never reaches a network, and it is
// how a local Eden-compatible proxy — and this package's own tests — address
// the provider. A URL that cannot be parsed into a host is treated as unsafe.
func credentialSafeURL(u *url.URL) bool {
	if u == nil {
		return false
	}
	if strings.EqualFold(u.Scheme, "https") {
		return true
	}
	return isLoopbackHost(u.Hostname())
}

// isLoopbackHost reports whether host names the local machine. Both the
// literal addresses (127.0.0.1, ::1) and the conventional name are accepted,
// because httptest servers use the former and local proxies are usually
// configured with the latter.
func isLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// guardedHTTPClient returns the HTTP client Eden requests go out on, with a
// redirect policy that refuses to carry the bearer token into cleartext.
//
// Go's default policy drops Authorization only when the redirect target is a
// different host; it does not look at the scheme, so an HTTPS -> HTTP redirect
// back to the same host forwards the credential in the clear (verified against
// net/http, not assumed). Both of this provider's construction paths route
// through here so the guarantee does not depend on which one a caller used.
//
// base is the caller-supplied client, or nil for the gateway default client
// (the tuned transport and timeouts llmclient would otherwise install). It is
// never mutated: the policy goes on a shallow copy, so a client shared with
// other callers keeps its own redirect behavior and its own transport.
func guardedHTTPClient(base *http.Client) *http.Client {
	if base == nil {
		base = httpclient.NewDefaultHTTPClient()
	}
	guarded := *base
	guarded.CheckRedirect = checkRedirect
	return &guarded
}

// checkRedirect refuses any redirect that would send Eden's credential to a
// cleartext destination, and otherwise defers to the same budget net/http
// applies by default.
//
// HTTPS -> HTTPS redirects (same host or not) are followed normally. The
// refusal is deliberate rather than merely stripping the header: an
// unauthenticated retry against a downgraded endpoint can only fail, and a
// named error tells the operator why instead of surfacing an opaque 401. The
// message carries scheme and host only — never the URL's userinfo or query,
// which can hold credentials of their own.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	if credentialSafeURL(req.URL) {
		return nil
	}
	return fmt.Errorf("edenai: refusing redirect to insecure %s://%s: the API credential would be sent in cleartext", req.URL.Scheme, req.URL.Host)
}
