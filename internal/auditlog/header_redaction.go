package auditlog

import (
	"bytes"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// Audit redaction is deliberately broader than core.IsCredentialHeader. That
// list also decides which headers the gateway forwards and which ones plugins
// and tagging rules may use, so it names exact headers. An audit entry has no
// such constraint: redacting one header too many costs a little detail, while
// storing one secret too few leaks it. So a header whose name merely looks
// like a credential is redacted here as well.

// credentialHeaderLastWords redact a header whose name is one of them or ends
// in "-word": X-Acme-Key, Upstream-Auth.
var credentialHeaderLastWords = [][]byte{
	[]byte("key"),
	[]byte("auth"),
}

// credentialHeaderTokenSuffix redacts X-Vault-Token, X-Amz-Security-Token and
// X-Amzn-Oidc-Accesstoken. It is a suffix, not a word to look for anywhere,
// so token counts (X-Ratelimit-Remaining-Tokens) stay readable.
var credentialHeaderTokenSuffix = []byte("token")

// credentialHeaderWords redact a header whose name contains one of them:
// X-Forwarded-Authorization, X-Acme-Api-Key-Id, X-Client-Secret-V2,
// X-Hub-Signature-256, Cf-Access-Jwt-Assertion.
var credentialHeaderWords = [][]byte{
	[]byte("authorization"),
	[]byte("api-key"),
	[]byte("apikey"),
	[]byte("secret"),
	[]byte("password"),
	[]byte("signature"),
	[]byte("jwt"),
}

// nonCredentialHeaders are well-known headers the patterns above would catch
// although they carry no secret.
var nonCredentialHeaders = map[string]struct{}{
	"idempotency-key":   {},
	"x-idempotency-key": {},
	"sec-websocket-key": {}, // handshake nonce
}

// headerNameScratchLen covers every header name seen in practice, so the
// folded copy below stays on the stack; a longer name allocates.
const headerNameScratchLen = 64

// isSensitiveHeader reports whether an audit entry must not store the
// header's value. Like core.IsCredentialHeader it ignores case and
// surrounding whitespace and reads "_" as "-". It runs for every header of
// every audited request.
func isSensitiveHeader(name string) bool {
	if core.IsCredentialHeader(name) {
		return true
	}
	name = strings.TrimSpace(name)
	var scratch [headerNameScratchLen]byte
	lower := scratch[:0]
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case 'A' <= c && c <= 'Z':
			c += 'a' - 'A'
		case c == '_':
			c = '-'
		}
		lower = append(lower, c)
	}
	if _, ok := nonCredentialHeaders[string(lower)]; ok {
		return false
	}
	if bytes.HasSuffix(lower, credentialHeaderTokenSuffix) {
		return true
	}
	for _, word := range credentialHeaderLastWords {
		if rest, ok := bytes.CutSuffix(lower, word); ok && (len(rest) == 0 || rest[len(rest)-1] == '-') {
			return true
		}
	}
	for _, word := range credentialHeaderWords {
		if bytes.Contains(lower, word) {
			return true
		}
	}
	return false
}
