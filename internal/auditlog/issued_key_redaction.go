package auditlog

import (
	"net/http"
	"strings"

	"github.com/goccy/go-json"
)

// redactedValue stands in for a secret an audit entry must not store.
const redactedValue = "[REDACTED]"

// issuedKeyRouteSuffix ends the route that issues a managed API key, under
// /admin and under the legacy /admin/api/v1 alias. It is a suffix, not the two
// routes spelled out, for the reason header redaction is broad: a route that
// matches by accident loses a little detail, while one that is missed after a
// mount changes stores a working key.
const issuedKeyRouteSuffix = "/auth-keys"

// redactIssuedKey masks the plaintext token in the response to
// POST /admin/auth-keys. The gateway keeps only the hash of a managed key and
// that response shows the token once, so an audit entry holding it would be
// the one place the token can be read back. Every other response is returned
// unchanged. A response on that route that is not a JSON object (one cut at
// MaxBodyCapture, say) is replaced whole, since the token cannot be found in
// it to mask.
func redactIssuedKey(method, route string, body []byte) []byte {
	if method != http.MethodPost || !strings.HasSuffix(route, issuedKeyRouteSuffix) {
		return body
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return []byte(`"` + redactedValue + `"`)
	}
	if _, ok := fields["value"]; !ok {
		return body
	}
	fields["value"] = json.RawMessage(`"` + redactedValue + `"`)
	redacted, err := json.Marshal(fields)
	if err != nil {
		return []byte(`"` + redactedValue + `"`)
	}
	return redacted
}
