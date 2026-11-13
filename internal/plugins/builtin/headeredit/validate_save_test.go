package headeredit

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/pluginapi/plugintest"
)

// Saving is stricter than loading: a line Init skips with a warning is
// rejected, so a credential typed into the form is never stored.
func TestValidateSaveRejectsRefusedHeaders(t *testing.T) {
	tests := []struct {
		name string
		cfg  string
		want string
	}{
		{"credential", `{"request_set": "X-Team: a\nAuthorization: Bearer sk-secret"}`, `header_edit: request_set line 2: header "Authorization" carries credentials and cannot be edited`},
		{"credential remove", `{"request_remove": "Cookie"}`, `header_edit: request_remove line 1: header "Cookie" carries credentials and cannot be edited`},
		{"credential with underscores", `{"upstream_set": "X_Api_Key: sk-secret"}`, `header_edit: upstream_set line 1: header "X_Api_Key" carries credentials and cannot be edited`},
		{"credential-looking name", `{"response_add": "X-Session-Token: sk-secret"}`, `header_edit: response_add line 1: header "X-Session-Token" looks like a credential and cannot be set`},
		{"gateway-owned upstream header", `{"upstream_set": "X-Tenant: acme\nHost: evil.test"}`, `header_edit: upstream_set line 2: header "Host" is set by the gateway and cannot be sent upstream`},
		{"first refused line is reported", `{"request_set": "Cookie: a=sk-secret", "response_set": "Set-Cookie: b=sk-secret"}`, `header_edit: request_set line 1: header "Cookie" carries credentials and cannot be edited`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := json.RawMessage(tt.cfg)

			err := (&Plugin{}).ValidateSave(raw)
			require.EqualError(t, err, tt.want)
			assert.NotContains(t, err.Error(), "sk-secret", "the error names the header, never its value")

			// The same config, once stored, still loads.
			require.NoError(t, New().Init(context.Background(), raw, plugintest.NewHost()))
		})
	}
}

func TestValidateSave(t *testing.T) {
	tests := []struct {
		name    string
		cfg     string
		wantErr string
	}{
		{name: "empty config", cfg: `{}`},
		{name: "allowed edits", cfg: `{"request_set": "X-Team: a", "response_remove": "X-Debug", "upstream_set": "# note\nX-Tenant: acme"}`},
		{name: "removing a token-like header", cfg: `{"response_remove": "X-Session-Token"}`},
		{name: "malformed line", cfg: `{"request_set": "broken"}`, wantErr: `request_set line 1: expected "Name: value"`},
		{name: "unknown key", cfg: `{"nope": "x"}`, wantErr: "nope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (&Plugin{}).ValidateSave(json.RawMessage(tt.cfg))
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
