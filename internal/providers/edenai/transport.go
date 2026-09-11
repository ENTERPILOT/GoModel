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

// secureDestination reports whether an Eden request may be sent to u at all.
//
// Eden is a hosted service reached over TLS, so a cleartext destination would
// expose everything on the wire to anyone on the path: not only the gateway's
// Eden key, but the prompt, the embedding input, and the completion coming
// back. Withholding the credential alone would still ship the payload, so this
// predicate gates the request itself (see secureTransport) as well as the
// Authorization header.
//
// Loopback is the one exemption: traffic to the local machine never reaches a
// network, and it is how a local Eden-compatible proxy — and this package's own
// tests — address the provider. A URL that cannot be parsed into a host is
// treated as unsafe.
func secureDestination(u *url.URL) bool {
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

// guardedHTTPClient returns the HTTP client Eden requests go out on, carrying
// two policies that keep an Eden request off a cleartext connection: the
// transport refuses an insecure destination outright, and the redirect policy
// refuses to be steered onto one.
//
// The redirect half is not redundant with Go's own behavior. Go's default
// policy drops Authorization only when the redirect target is a different
// host; it does not look at the scheme, so an HTTPS -> HTTP redirect back to
// the same host forwards the credential in the clear (verified against
// net/http, not assumed). Both of this provider's construction paths route
// through here so the guarantee does not depend on which one a caller used.
//
// base is the caller-supplied client, or nil for the gateway default client
// (the tuned transport and timeouts llmclient would otherwise install). It is
// never mutated: both policies go on a shallow copy, so a client shared with
// other callers keeps its own redirect behavior and its own transport.
func guardedHTTPClient(base *http.Client) *http.Client {
	if base == nil {
		base = httpclient.NewDefaultHTTPClient()
	}
	guarded := *base
	guarded.Transport = &secureTransport{base: guarded.Transport}
	guarded.CheckRedirect = redirectPolicy(base.CheckRedirect)
	return &guarded
}

// redirectPolicy composes Eden's redirect rules with whatever policy the
// caller's client already carried.
//
// Eden's rules run first and a refusal is final, so a permissive caller policy
// cannot waive the cleartext or cross-host guarantees; a caller can only
// restrict further. When Eden allows the hop, the caller's callback decides,
// and its result is returned verbatim -- including http.ErrUseLastResponse,
// the sentinel that means "stop here and hand back the redirect response".
//
// Replacing the field outright would silently discard that policy (verified:
// an overwritten CheckRedirect never runs and the redirect is followed anyway),
// which is why the guard composes here the same way secureTransport wraps the
// caller's transport rather than replacing it.
//
// Eden's rules are checked twice, before and after the callback, because
// net/http hands CheckRedirect the very *http.Request it is about to send and
// honors any change made to it (verified: a callback that rewrites req.URL
// redirects the request to the rewritten target). Validating only up front
// would leave the checks describing a URL that is no longer the one going out,
// so a callback that rewrites the host -- a region or proxy rewrite as much as
// anything hostile -- would carry the request past them.
func redirectPolicy(caller func(*http.Request, []*http.Request) error) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if err := checkRedirect(req, via); err != nil {
			return err
		}
		if caller == nil {
			return nil
		}
		if err := caller(req, via); err != nil {
			// Returned unchanged: the caller may be signalling
			// http.ErrUseLastResponse, which net/http reads as "stop here and
			// hand back the redirect response" rather than as a failure.
			return err
		}
		return checkRedirect(req, via)
	}
}

// checkRedirect applies Eden's own redirect rules: stay encrypted, stay on the
// host the request was addressed to, and respect net/http's redirect budget.
//
// Only same-host redirects are followed. Go's default policy forwards
// Authorization to a subdomain of the original host (verified: a redirect from
// api.edenai.run to evil.api.edenai.run arrives carrying the bearer token; an
// unrelated host does not), so an upstream-controlled Location header would
// otherwise be enough to hand the Eden key to a neighbouring name. Refusing
// rather than merely stripping the header is deliberate for the same reason
// secureTransport refuses: following the hop would still ship the prompt or
// embedding input to a destination the operator never configured, which is the
// server-side request forgery half of the problem.
//
// A path-only redirect on the configured host -- the one shape a REST endpoint
// plausibly returns -- keeps working.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	if !secureDestination(req.URL) {
		return insecureDestinationError("follow redirect to", req.URL)
	}
	// via is ordered oldest first, so via[0] is the request the caller made.
	// Compare against that rather than the previous hop: a chain of same-step
	// redirects must not be able to walk away from the original host one label
	// at a time.
	if len(via) > 0 && via[0].URL != nil && !strings.EqualFold(via[0].URL.Host, req.URL.Host) {
		return fmt.Errorf("edenai: refusing cross-host redirect from %s to %s: the credential and the request body must not follow an upstream-chosen destination",
			via[0].URL.Host, req.URL.Host)
	}
	return nil
}

// secureTransport stops a request bound for a cleartext destination before any
// of it reaches the network.
//
// Withholding the Authorization header protects the credential but nothing
// else: the request body still carries the prompt or the embedding input, and
// it is written to the socket before the upstream ever gets to reject the call
// (verified: the payload arrives in full). Refusing the request is what
// actually prevents the disclosure, and it matches how checkRedirect already
// handles a downgraded redirect target.
//
// The check lives on the transport rather than in each provider method so it
// covers every path uniformly — chat, streaming, embeddings, model listing, and
// passthrough, which forwards an opaque caller-supplied body — and so it reads
// the endpoint in force at request time, which SetBaseURL can change after
// construction.
type secureTransport struct {
	base http.RoundTripper
}

func (t *secureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !secureDestination(req.URL) {
		return nil, insecureDestinationError("send request to", req.URL)
	}
	base := t.base
	if base == nil {
		// A nil Transport means http.DefaultTransport, the same default
		// net/http applies (http.DefaultClient leaves it nil).
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// insecureDestinationError describes a refused destination without quoting the
// URL wholesale: scheme and host only, never the userinfo or query string,
// which can hold credentials of their own.
func insecureDestinationError(action string, u *url.URL) error {
	return fmt.Errorf("edenai: refusing to %s insecure %s://%s: the request would travel in cleartext", action, u.Scheme, u.Host)
}
