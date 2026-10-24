package ext

import "net/url"

// ProxyRequest describes one upstream request about to leave the gateway, so
// a ProxySelector can choose its egress. Provider is the configured provider
// instance name ("openai-eu"), ProviderType its adapter ("openai"), and URL
// the upstream endpoint being called.
type ProxyRequest struct {
	Provider     string
	ProviderType string
	URL          *url.URL
}

// ProxySelector chooses the outbound proxy for upstream provider requests.
// Core consults it on every request of a provider that has no explicit
// `proxy_url` of its own; a provider's own setting always wins. Returning a
// nil URL and nil error means "no opinion", and core then falls back to the
// process-wide HTTP_PROXY / HTTPS_PROXY / NO_PROXY environment. Returning an
// error fails the request before it is sent.
//
// Selection runs on the request path, so implementations must be safe for
// concurrent use and return quickly. The returned URL may carry credentials;
// core never logs it.
type ProxySelector interface {
	Name() string
	SelectProxy(req ProxyRequest) (*url.URL, error)
}
