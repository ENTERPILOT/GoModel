package anthropicapi

import (
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			assert.Equal(t, tc.wantStatus, status)
			assert.Equal(t, "error", body.Type)
			assert.Equal(t, tc.wantType, body.Error.Type)
			assert.Equal(t, tc.err.Message, body.Error.Message)
		})
	}
}

func TestErrorFromGatewayNil(t *testing.T) {
	status, body := ErrorFromGateway(nil)
	assert.Equal(t, http.StatusInternalServerError, status)
	assert.Equal(t, "api_error", body.Error.Type)
}

func TestErrorFromGateway_ProviderOnlyWhenUpstream(t *testing.T) {
	_, upstream := ErrorFromGateway(core.ParseProviderError("anthropic", http.StatusUnauthorized, []byte("bad key"), nil))
	require.Equal(t, "anthropic", upstream.Error.Provider)

	_, gateway := ErrorFromGateway(core.NewAuthenticationError("", "invalid API key"))
	require.Empty(t, gateway.Error.Provider)
}
