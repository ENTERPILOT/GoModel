package providers

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetAuthHeaders(t *testing.T) {
	const longID = "this-request-id-is-rejected" // gated by the validator below

	tests := []struct {
		name      string
		apiKey    string
		requestID string
		cfg       AuthHeaderConfig
		wantAuth  string // value of the auth header; "" means header must be absent
		authKey   string // header name to read the credential from
		wantReqID string // expected request-id header value; "" means absent
		reqIDKey  string
	}{
		{
			name:     "default authorization header",
			apiKey:   "secret",
			cfg:      AuthHeaderConfig{AuthScheme: "Bearer "},
			wantAuth: "Bearer secret",
			authKey:  "Authorization",
		},
		{
			name:     "custom auth header without scheme",
			apiKey:   "secret",
			cfg:      AuthHeaderConfig{AuthHeader: "api-key"},
			wantAuth: "secret",
			authKey:  "api-key",
		},
		{
			name:      "forwards request id",
			apiKey:    "secret",
			requestID: "req-123",
			cfg:       AuthHeaderConfig{AuthScheme: "Bearer ", RequestIDHeader: "X-Request-ID"},
			wantAuth:  "Bearer secret",
			authKey:   "Authorization",
			wantReqID: "req-123",
			reqIDKey:  "X-Request-ID",
		},
		{
			name:      "no request id header when not configured",
			apiKey:    "secret",
			requestID: "req-123",
			cfg:       AuthHeaderConfig{AuthScheme: "Bearer "},
			wantAuth:  "Bearer secret",
			authKey:   "Authorization",
			reqIDKey:  "X-Request-ID",
		},
		{
			name:      "validator rejects request id",
			apiKey:    "secret",
			requestID: longID,
			cfg: AuthHeaderConfig{
				AuthScheme:        "Bearer ",
				RequestIDHeader:   "X-Request-ID",
				ValidateRequestID: func(string) bool { return false },
			},
			wantAuth: "Bearer secret",
			authKey:  "Authorization",
			reqIDKey: "X-Request-ID",
		},
		{
			name:     "optional api key skips auth header when empty",
			apiKey:   "",
			cfg:      AuthHeaderConfig{AuthScheme: "Bearer ", OptionalAPIKey: true},
			authKey:  "Authorization",
			wantAuth: "",
		},
		{
			name:     "required api key still sets header when empty",
			apiKey:   "",
			cfg:      AuthHeaderConfig{AuthScheme: "Bearer "},
			authKey:  "Authorization",
			wantAuth: "Bearer ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.requestID != "" {
				ctx = core.WithRequestID(ctx, tt.requestID)
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.com", nil)
			require.NoError(t, err)

			SetAuthHeaders(req, tt.apiKey, tt.cfg)
			got := req.Header.Get(tt.authKey)
			assert.Equal(t, tt.wantAuth, got, "auth header %q", tt.authKey)

			if tt.reqIDKey != "" {
				got := req.Header.Get(tt.reqIDKey)
				assert.Equal(t, tt.wantReqID, got, "request id header %q", tt.reqIDKey)
			}
		})
	}
}

func TestIsValidClientRequestID(t *testing.T) {
	tests := []struct {
		name  string
		id    string
		valid bool
	}{
		{
			name:  "valid UUID",
			id:    "123e4567-e89b-12d3-a456-426614174000",
			valid: true,
		},
		{
			name:  "valid short ID",
			id:    "req-123",
			valid: true,
		},
		{
			name:  "valid empty string",
			id:    "",
			valid: true,
		},
		{
			name:  "valid 512 chars",
			id:    strings.Repeat("a", 512),
			valid: true,
		},
		{
			name:  "invalid - 513 chars (too long)",
			id:    strings.Repeat("a", 513),
			valid: false,
		},
		{
			name:  "invalid - non-ASCII character",
			id:    "req-123-日本語",
			valid: false,
		},
		{
			name:  "invalid - control character",
			id:    "req-123\n456",
			valid: false,
		},
		{
			name:  "invalid - NUL byte",
			id:    "req-123\x00",
			valid: false,
		},
		{
			name:  "invalid - tab",
			id:    "req\t123",
			valid: false,
		},
		{
			name:  "invalid - DEL",
			id:    "req-123\x7f",
			valid: false,
		},
		{
			name:  "invalid - emoji",
			id:    "req-123-🎉",
			valid: false,
		},
		{
			name:  "valid - all printable ASCII",
			id:    "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.",
			valid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsValidClientRequestID(tt.id)
			assert.Equal(t, tt.valid, got, "IsValidClientRequestID(%q)", tt.id)
		})
	}
}
