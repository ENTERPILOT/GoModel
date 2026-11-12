package core

import (
	"net/http"
	"strings"
)

// pluginUpstreamTransportHeaders control the transport, the framing of the
// body, or the compression of the response rather than the provider call, so a
// plugin must not set them: the HTTP client owns the connection and the gateway
// owns the body it built and the encoding it negotiates. Keys are canonical.
var pluginUpstreamTransportHeaders = map[string]struct{}{
	// Connection framing. The hop-by-hop set of RFC 9110, minus
	// Proxy-Authorization, which the credential list already refuses.
	"Host":               {},
	"Content-Length":     {},
	"Transfer-Encoding":  {},
	"Connection":         {},
	"Keep-Alive":         {},
	"Proxy-Connection":   {},
	"Proxy-Authenticate": {},
	"Te":                 {},
	"Trailer":            {},
	"Upgrade":            {},
	"Expect":             {},
	// The body itself: the gateway sends the JSON it encoded, uncompressed.
	"Content-Type":     {},
	"Content-Encoding": {},
	// Response compression. The transport decompresses gzip automatically only
	// while the request carries no Accept-Encoding of its own, so a plugin value
	// hands the gateway compressed bytes it cannot parse.
	"Accept-Encoding": {},
}

// pluginUpstreamReservedHeaders lists headers a provider negotiates with its
// own hook, or the gateway sets for the client, and that a plugin would
// silently break by replacing. Keys are canonical.
//
// Anthropic-Beta selects Anthropic features, and subscription credentials must
// carry oauth-2025-04-20 in it: the Anthropic provider adds that flag in its
// header hook, which the plugin header merge in llmclient runs after, so a
// plugin value would drop the flag and the request would be rejected.
//
// Idempotency-Key carries the client's own key, which llmclient forwards. A
// plugin value would replace it, and a provider that deduplicates on it would
// treat unrelated requests as retries of one call.
var pluginUpstreamReservedHeaders = map[string]struct{}{
	"Anthropic-Beta":  {},
	"Idempotency-Key": {},
}

// IsPluginUpstreamHeaderDenied reports whether a plugin may not add the header
// to a provider call through Exchange.Headers.Upstream: it carries a
// credential, it controls the transport or the body encoding, or the provider
// or the gateway owns it. Matching is case-insensitive and reads "_" as "-",
// like IsCredentialHeader: a proxy that folds Content_Type into Content-Type
// would otherwise receive a header the gateway owns.
func IsPluginUpstreamHeaderDenied(name string) bool {
	canonical := http.CanonicalHeaderKey(strings.ReplaceAll(strings.TrimSpace(name), "_", "-"))
	if _, denied := pluginUpstreamTransportHeaders[canonical]; denied {
		return true
	}
	if _, denied := pluginUpstreamReservedHeaders[canonical]; denied {
		return true
	}
	return IsCredentialHeader(canonical)
}
