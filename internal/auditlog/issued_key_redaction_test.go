package auditlog

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRedactIssuedKey(t *testing.T) {
	const issued = `{"id":"k1","name":"ci","redacted_value":"sk_gom_...abcd","value":"sk_gom_plaintext"}`
	const masked = `{"id":"k1","name":"ci","redacted_value":"sk_gom_...abcd","value":"[REDACTED]"}`

	tests := []struct {
		name   string
		method string
		route  string
		body   string
		want   string
	}{
		{name: "issued key", method: http.MethodPost, route: "/admin/auth-keys", body: issued, want: masked},
		{name: "issued key on the legacy alias", method: http.MethodPost, route: "/admin/api/v1/auth-keys", body: issued, want: masked},
		{name: "issued key under another mount", method: http.MethodPost, route: "/gateway/auth-keys", body: issued, want: masked},
		{
			name:   "error response is kept as sent",
			method: http.MethodPost,
			route:  "/admin/auth-keys",
			body:   `{"error": {"type": "invalid_request_error", "message": "name is required"}}`,
			want:   `{"error": {"type": "invalid_request_error", "message": "name is required"}}`,
		},
		{
			name:   "truncated response is replaced whole",
			method: http.MethodPost,
			route:  "/admin/auth-keys",
			body:   `{"id":"k1","value":"sk_gom_plaintext","allowed_models":["openai/gp`,
			want:   `"[REDACTED]"`,
		},
		{name: "key list", method: http.MethodGet, route: "/admin/auth-keys", body: `[{"id":"k1"}]`, want: `[{"id":"k1"}]`},
		{name: "key import", method: http.MethodPost, route: "/admin/auth-keys/import", body: `{"id":"k1","value":"kept"}`, want: `{"id":"k1","value":"kept"}`},
		{name: "runtime setting", method: http.MethodPut, route: "/admin/runtime/settings/:key", body: `{"value":"kept"}`, want: `{"value":"kept"}`},
		{name: "model response", method: http.MethodPost, route: "/v1/chat/completions", body: `{"value":"kept"}`, want: `{"value":"kept"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactIssuedKey(tt.method, tt.route, []byte(tt.body))
			assert.Equal(t, tt.want, string(got))
		})
	}
}
