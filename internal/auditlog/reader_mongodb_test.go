package auditlog

import "testing"

func TestSanitizeLogDataRedactsHeaders(t *testing.T) {
	original := &LogData{
		RequestHeaders: map[string]string{
			"Authorization": "Bearer secret",
			"X-Test":        "ok",
		},
		ResponseHeaders: map[string]string{
			"Set-Cookie": "session=abc",
			"Server":     "gateway",
		},
	}

	sanitized := sanitizeLogData(original)
	if sanitized == nil {
		t.Fatalf("sanitizeLogData returned nil")
		return
	}

	if got := sanitized.RequestHeaders["Authorization"]; got != "[REDACTED]" {
		t.Fatalf("request Authorization not redacted: %q", got)
	}
	if got := sanitized.RequestHeaders["X-Test"]; got != "ok" {
		t.Fatalf("request non-sensitive header changed: %q", got)
	}
	if got := sanitized.ResponseHeaders["Set-Cookie"]; got != "[REDACTED]" {
		t.Fatalf("response Set-Cookie not redacted: %q", got)
	}
	if got := sanitized.ResponseHeaders["Server"]; got != "gateway" {
		t.Fatalf("response non-sensitive header changed: %q", got)
	}

	// Ensure original is not mutated.
	if got := original.RequestHeaders["Authorization"]; got != "Bearer secret" {
		t.Fatalf("original request headers mutated: %q", got)
	}
	if got := original.ResponseHeaders["Set-Cookie"]; got != "session=abc" {
		t.Fatalf("original response headers mutated: %q", got)
	}
}

func TestSanitizeLogDataNilSafe(t *testing.T) {
	if sanitizeLogData(nil) != nil {
		t.Fatalf("expected nil input to return nil")
	}
}
