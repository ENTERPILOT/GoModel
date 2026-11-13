package auditlog

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestIsSensitiveHeader(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		// The exact credential list still applies.
		{name: "Authorization", want: true},
		{name: "X-Api-Key", want: true},
		{name: "Set-Cookie", want: true},

		// Names that only look like a credential.
		{name: "X-Amz-Security-Token", want: true},
		{name: "X-Vault-Token", want: true},
		{name: "X-Csrf-Token", want: true},
		{name: "X-Amzn-Oidc-Accesstoken", want: true},
		{name: "Token", want: true},
		{name: "X-Acme-Key", want: true},
		{name: "X-Functions-Key", want: true},
		{name: "Key", want: true},
		{name: "X-Upstream-Auth", want: true},
		{name: "Auth", want: true},
		{name: "X-Client-Secret", want: true},
		{name: "X-Secret-V2", want: true},
		{name: "X-Forwarded-Authorization", want: true},
		{name: "X-Ms-Authorization-Auxiliary", want: true},
		{name: "X-Acme-Api-Key-Id", want: true},
		{name: "X-Acme-Apikey", want: true},
		{name: "X-Db-Password", want: true},
		{name: "X-Hub-Signature-256", want: true},
		{name: "Cf-Access-Jwt-Assertion", want: true},
		{name: "x_vault_token", want: true},
		{name: " X-VAULT-TOKEN ", want: true},
		{name: "X-" + strings.Repeat("Long-", 20) + "Token", want: true},

		// Well-known headers that carry no secret.
		{name: "Idempotency-Key", want: false},
		{name: "X-Idempotency-Key", want: false},
		{name: "idempotency_key", want: false},
		{name: "Sec-WebSocket-Key", want: false},
		{name: "X-Request-Id", want: false},
		{name: "X-Stainless-Retry-Count", want: false},
		{name: "X-Stainless-Timeout", want: false},
		{name: "X-Ratelimit-Remaining-Tokens", want: false},
		{name: "X-Ratelimit-Limit-Tokens", want: false},
		{name: "Anthropic-Ratelimit-Input-Tokens-Remaining", want: false},
		{name: "X-Amzn-Bedrock-Input-Token-Count", want: false},
		{name: "Www-Authenticate", want: false},
		{name: "Access-Control-Allow-Credentials", want: false},
		{name: "X-Gomodel-Auth-User", want: false},
		{name: "X-Monkey", want: false},
		{name: "Content-Type", want: false},
		{name: "User-Agent", want: false},
		{name: "", want: false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, isSensitiveHeader(tt.name), "isSensitiveHeader(%q)", tt.name)
	}
}

// The patterns widen audit redaction only: the exact list keeps deciding
// which headers the gateway forwards and which ones plugins and tagging
// rules may use.
func TestSensitiveHeaderPatternsStayOutOfCredentialList(t *testing.T) {
	for _, name := range []string{"X-Vault-Token", "X-Amz-Security-Token", "X-Acme-Key", "X-Client-Secret"} {
		assert.True(t, isSensitiveHeader(name), "isSensitiveHeader(%q)", name)
		assert.False(t, core.IsCredentialHeader(name), "IsCredentialHeader(%q)", name)
	}
}

func TestExtractHeadersRedactsCredentialLookingNames(t *testing.T) {
	got := extractHeaders(http.Header{
		"X-Amz-Security-Token":         {"aws-session-token"},
		"X-Vault-Token":                {"vault-token"},
		"X-Acme-Key":                   {"custom-key"},
		"Idempotency-Key":              {"retry-1"},
		"X-Ratelimit-Remaining-Tokens": {"1000"},
	})
	assert.Equal(t, map[string]string{
		"X-Amz-Security-Token":         "[REDACTED]",
		"X-Vault-Token":                "[REDACTED]",
		"X-Acme-Key":                   "[REDACTED]",
		"Idempotency-Key":              "retry-1",
		"X-Ratelimit-Remaining-Tokens": "1000",
	}, got)
}
