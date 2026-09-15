package auditlog

import (
	"github.com/goccy/go-json"
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
	require.Equal(t, "model is not available", neither[0].ErrorMessage, "structured error fields must be preserved when gating: %#v", neither[0])

	bodyOnly := GateAttemptCapture(base(), Config{LogBodies: true})
	require.NotNil(t, bodyOnly[0].ResponseBody)
	require.Nil(t, bodyOnly[0].ResponseHeaders, "LogBodies-only should keep body and drop headers: %#v", bodyOnly[0])
}
