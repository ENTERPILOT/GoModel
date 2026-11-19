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
	// KeyPosition tells which of the provider's API keys authenticates the
	// request, so a provider holding the keys of several accounts can send
	// each account through its own proxy. It is the key's 1-based position in
	// the provider's key list: 1 for `api_key` or `<PROVIDER>_API_KEY`, 2 for
	// the next one, counted after empty and duplicate entries are dropped.
	//
	// It is 0 when the request cannot be placed: the provider is keyless or
	// authenticates with cloud credentials or workload identity, the request
	// is unauthenticated, its key was rotated out while it was in flight, or
	// its credential headers carry more than one of the provider's keys. A
	// selector that must never send an account through the wrong egress
	// should return an error for 0 on the providers it maps by key. The key
	// itself is never exposed.
	KeyPosition int
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
