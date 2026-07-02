package providers

import (
	"net/http"
	"strings"
)

// SkipPassthroughHeader reports whether a header key is excluded from provider
// passthrough. The always-on floor covers hop-by-hop, transport-managed,
// credential, cookie, and X-Forwarded-* headers; extras adds operator-defined
// keys on top. The key is trimmed and canonicalized before matching.
func SkipPassthroughHeader(key string, extras ...string) bool {
	canonicalKey := http.CanonicalHeaderKey(strings.TrimSpace(key))
	if canonicalKey == "" {
		return false
	}
	switch canonicalKey {
	case "Authorization", "X-Api-Key", "Host", "Content-Length", "Connection", "Keep-Alive",
		"Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
		"Cookie", "Forwarded", "Set-Cookie":
		return true
	}
	if strings.HasPrefix(canonicalKey, "X-Forwarded-") {
		return true
	}
	for _, raw := range extras {
		if raw == "" {
			continue
		}
		if http.CanonicalHeaderKey(strings.TrimSpace(raw)) == canonicalKey {
			return true
		}
	}
	return false
}
