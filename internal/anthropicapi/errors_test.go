package anthropicapi

import (
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestErrorFromGateway(t *testing.T) {
	tests := []struct {
		name       string
		err        *core.GatewayError
		wantStatus int
		wantType   string
	}{
		{
			name:       "invalid request",
			err:        core.NewInvalidRequestError("bad", nil),
			wantStatus: http.StatusBadRequest,
			wantType:   "invalid_request_error",
		},
		{
			name:       "request too large",
			err:        core.NewInvalidRequestErrorWithStatus(http.StatusRequestEntityTooLarge, "payload too large", nil),
			wantStatus: http.StatusRequestEntityTooLarge,
			wantType:   "request_too_large",
		},
		{
			name:       "authentication",
			err:        core.NewAuthenticationError("p", "no key"),
			wantStatus: http.StatusUnauthorized,
			wantType:   "authentication_error",
		},
		{
			name:       "forbidden maps to permission error",
			err:        core.ParseProviderError("p", http.StatusForbidden, []byte("forbidden"), nil),
			wantStatus: http.StatusForbidden,
			wantType:   "permission_error",
		},
		{
			name:       "not found",
			err:        core.NewNotFoundError("missing model"),
			wantStatus: http.StatusNotFound,
			wantType:   "not_found_error",
		},
		{
			name:       "rate limit",
			err:        core.NewRateLimitError("p", "slow down"),
			wantStatus: http.StatusTooManyRequests,
			wantType:   "rate_limit_error",
		},
		{
			name:       "provider error",
			err:        core.NewProviderError("p", http.StatusBadGateway, "upstream down", nil),
			wantStatus: http.StatusBadGateway,
			wantType:   "api_error",
		},
		{
			name:       "provider overloaded",
			err:        core.NewProviderError("p", http.StatusServiceUnavailable, "overloaded", nil),
			wantStatus: http.StatusServiceUnavailable,
			wantType:   "overloaded_error",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, body := ErrorFromGateway(tc.err)
			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d", status, tc.wantStatus)
			}
			if body.Type != "error" {
				t.Errorf("envelope type = %q, want error", body.Type)
			}
			if body.Error.Type != tc.wantType {
				t.Errorf("error type = %q, want %q", body.Error.Type, tc.wantType)
			}
			if body.Error.Message != tc.err.Message {
				t.Errorf("message = %q, want %q", body.Error.Message, tc.err.Message)
			}
		})
	}
}

func TestErrorFromGatewayNil(t *testing.T) {
	status, body := ErrorFromGateway(nil)
	if status != http.StatusInternalServerError {
		t.Errorf("status = %d", status)
	}
	if body.Error.Type != "api_error" {
		t.Errorf("error type = %q", body.Error.Type)
	}
}
