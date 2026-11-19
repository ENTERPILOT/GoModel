package core

import (
	"net/http"
	"strings"
)

// edgeCredentialHeaders are the credential headers that authenticate the
// gateway to a proxy in front of a provider (an AI gateway or an API manager)
// rather than to the provider. They stay in credentialHeaders, so they are
// redacted and refused everywhere else, but an operator may set them in a
// provider's `headers`: that setting exists to carry them. Keys are canonical.
var edgeCredentialHeaders = map[string]struct{}{
	"Helicone-Auth":             {},
	"Cf-Aig-Authorization":      {},
	"X-Portkey-Api-Key":         {},
	"Ocp-Apim-Subscription-Key": {},
}

// IsProviderCredentialHeader reports whether a provider's configured `headers`
// may not set the header because it carries the credential the gateway sends
// itself from api_key or api_keys. Configured headers are applied last, so
// accepting one would give a provider two credentials and bypass key rotation.
// Matching follows IsCredentialHeader.
func IsProviderCredentialHeader(name string) bool {
	if !IsCredentialHeader(name) {
		return false
	}
	_, edge := edgeCredentialHeaders[canonicalHeaderName(name)]
	return !edge
}

// IsProviderHeaderReserved reports whether a provider's configured `headers`
// may not set the header because the HTTP client or the gateway owns it: it
// frames the connection, describes the body the gateway built, negotiates the
// response encoding, addresses a proxy hop, or carries the client's
// Idempotency-Key. Matching follows IsPluginUpstreamHeaderDenied.
func IsProviderHeaderReserved(name string) bool {
	canonical := canonicalHeaderName(name)
	if _, reserved := pluginUpstreamTransportHeaders[canonical]; reserved {
		return true
	}
	return canonical == IdempotencyKeyHeader || strings.HasPrefix(canonical, "Proxy-")
}

// CanonicalProviderHeaderName returns the form two names of a provider's
// `headers` share when they reach a proxy as the same header. See
// canonicalHeaderName.
func CanonicalProviderHeaderName(name string) string {
	return canonicalHeaderName(name)
}

// canonicalHeaderName returns the canonical form of a header name, reading
// "_" as "-" like IsCredentialHeader: a proxy that folds one into the other
// would otherwise receive a header the gateway refuses under its usual name.
func canonicalHeaderName(name string) string {
	return http.CanonicalHeaderKey(strings.ReplaceAll(strings.TrimSpace(name), "_", "-"))
}
