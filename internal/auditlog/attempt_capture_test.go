package auditlog

import (
	"github.com/goccy/go-json"

	"net/http"
	"testing"
)

func TestCaptureAttemptResponseBody(t *testing.T) {
	if got := CaptureAttemptResponseBody(nil); got != nil {
		t.Fatalf("empty body = %#v, want nil", got)
	}

	captured := CaptureAttemptResponseBody([]byte(`{"error":{"code":"model_not_found"}}`))
	if _, ok := captured.(json.RawMessage); !ok {
		t.Fatalf("json body = %T, want json.RawMessage", captured)
	}
	if _, ok := BodyDocument(captured).(map[string]any); !ok {
		t.Fatalf("json body did not decode to a map: %#v", BodyDocument(captured))
	}

	if got := CaptureAttemptResponseBody([]byte("upstream is down")); got != "upstream is down" {
		t.Fatalf("non-json body = %#v, want raw string", got)
	}
}

func TestRedactAttemptResponseHeaders(t *testing.T) {
	headers := http.Header{
		"Authorization": []string{"Bearer secret-token"},
		"Retry-After":   []string{"30"},
		"X-Request-Id":  []string{"req-123"},
	}

	got := RedactAttemptResponseHeaders(headers)
	if got["Authorization"] != "[REDACTED]" {
		t.Fatalf("Authorization = %q, want redacted", got["Authorization"])
	}
	if got["Retry-After"] != "30" || got["X-Request-Id"] != "req-123" {
		t.Fatalf("diagnostic headers were not preserved: %#v", got)
	}

	if RedactAttemptResponseHeaders(nil) != nil {
		t.Fatalf("nil headers should map to nil")
	}
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
	if both[0].ResponseBody == nil || both[0].ResponseHeaders == nil {
		t.Fatalf("with bodies+headers enabled both captures should survive: %#v", both[0])
	}

	neither := GateAttemptCapture(base(), Config{})
	if neither[0].ResponseBody != nil || neither[0].ResponseHeaders != nil {
		t.Fatalf("with logging disabled captures should be stripped: %#v", neither[0])
	}
	if neither[0].ErrorMessage != "model is not available" {
		t.Fatalf("structured error fields must be preserved when gating: %#v", neither[0])
	}

	bodyOnly := GateAttemptCapture(base(), Config{LogBodies: true})
	if bodyOnly[0].ResponseBody == nil || bodyOnly[0].ResponseHeaders != nil {
		t.Fatalf("LogBodies-only should keep body and drop headers: %#v", bodyOnly[0])
	}
}
