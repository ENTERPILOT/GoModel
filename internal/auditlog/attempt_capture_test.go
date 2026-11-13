package auditlog

import (
	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"net/http"
	"testing"
)

func TestCaptureAttemptResponseBody(t *testing.T) {
	got := CaptureAttemptResponseBody(nil)
	require.Nil(t, got)

	captured := CaptureAttemptResponseBody([]byte(`{"error":{"code":"model_not_found"}}`))
	_, ok := captured.(json.RawMessage)
	require.True(t, ok, "json body = %T, want json.RawMessage", captured)
	_, ok = BodyDocument(captured).(map[string]any)
	require.True(t, ok, "json body did not decode to a map: %#v", BodyDocument(captured))
	require.Equal(t, "upstream is down", CaptureAttemptResponseBody([]byte("upstream is down")))
}

func TestRedactAttemptResponseHeaders(t *testing.T) {
	headers := http.Header{
		"Authorization": []string{"Bearer secret-token"},
		"Retry-After":   []string{"30"},
		"X-Request-Id":  []string{"req-123"},
	}

	got := RedactAttemptResponseHeaders(headers)
	require.Equal(t, "[REDACTED]", got["Authorization"])
	require.Equal(t, "30", got["Retry-After"])
	require.Equal(t, "req-123", got["X-Request-Id"], "diagnostic headers were not preserved: %#v", got)
	require.Nil(t, RedactAttemptResponseHeaders(nil))
}

func TestGateAttemptCapture(t *testing.T) {
	base := func() []AttemptSnapshot {
		return []AttemptSnapshot{{
			Seq:             1,
			Kind:            AttemptKindPrimary,
			ErrorMessage:    "model is not available",
			ResponseBody:    map[string]any{"error": "nope"},
			ResponseHeaders: map[string]string{"Retry-After": "30"},
		}}
	}

	both := GateAttemptCapture(base(), Config{LogBodies: true, LogHeaders: true})
	require.NotNil(t, both[0].ResponseBody)
	require.NotNil(t, both[0].ResponseHeaders, "with bodies+headers enabled both captures should survive: %#v", both[0])

	neither := GateAttemptCapture(base(), Config{})
	require.Nil(t, neither[0].ResponseBody)
	require.Nil(t, neither[0].ResponseHeaders, "with logging disabled captures should be stripped: %#v", neither[0])

	bodyOnly := GateAttemptCapture(base(), Config{LogBodies: true})
	require.NotNil(t, bodyOnly[0].ResponseBody)
	require.Nil(t, bodyOnly[0].ResponseHeaders, "LogBodies-only should keep body and drop headers: %#v", bodyOnly[0])
	require.Equal(t, "model is not available", bodyOnly[0].ErrorMessage)
}

// An error message that came with an upstream response body is body content:
// it is withheld with the body. The error type, code and status stay, and so
// does a message the gateway wrote itself.
func TestGateAttemptCaptureWithholdsUpstreamErrorMessage(t *testing.T) {
	attempts := func() []AttemptSnapshot {
		return []AttemptSnapshot{
			{
				Seq: 1, Kind: AttemptKindPrimary, StatusCode: http.StatusBadRequest,
				ErrorType: "invalid_request_error", ErrorCode: "invalid_value",
				ErrorMessage: "Invalid input: the user's prompt",
				ResponseBody: map[string]any{"error": map[string]any{"message": "Invalid input: the user's prompt"}},
			},
			{
				Seq: 2, Kind: AttemptKindFailover, StatusCode: http.StatusBadGateway,
				ErrorType: "provider_error", ErrorMessage: "upstream connection failed",
			},
		}
	}

	tests := []struct {
		name         string
		cfg          Config
		wantUpstream string
	}{
		{name: "bodies off", cfg: Config{LogHeaders: true}, wantUpstream: UpstreamErrorMessageWithheld},
		{name: "config unresolved", cfg: Config{}, wantUpstream: UpstreamErrorMessageWithheld},
		{name: "bodies on", cfg: Config{LogBodies: true}, wantUpstream: "Invalid input: the user's prompt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Gating twice must not change the outcome.
			got := GateAttemptCapture(GateAttemptCapture(attempts(), tt.cfg), tt.cfg)

			assert.Equal(t, tt.wantUpstream, got[0].ErrorMessage)
			assert.Equal(t, tt.cfg.LogBodies, got[0].ResponseBody != nil)
			assert.Equal(t, "invalid_request_error", got[0].ErrorType)
			assert.Equal(t, "invalid_value", got[0].ErrorCode)
			assert.Equal(t, http.StatusBadRequest, got[0].StatusCode)
			assert.Equal(t, "upstream connection failed", got[1].ErrorMessage, "a gateway-written message is kept")
		})
	}
}
