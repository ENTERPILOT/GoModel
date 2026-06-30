package auditlog

import (
	"net/http"

	"github.com/goccy/go-json"
)

// CaptureAttemptResponseBody parses a raw upstream error body into a value
// suitable for audit storage: a JSON value when the body is valid JSON, a
// UTF-8 string otherwise, or nil when empty.
func CaptureAttemptResponseBody(body []byte) any {
	return captureLoggedBody(body)
}

// RedactAttemptResponseHeaders flattens and redacts the upstream response
// headers of a failed attempt for audit storage.
func RedactAttemptResponseHeaders(header http.Header) map[string]string {
	if len(header) == 0 {
		return nil
	}
	return extractHeaders(header)
}

// marshalAttemptColumn serializes a per-attempt capture field (body or headers)
// to a JSON string for SQL storage, returning nil (SQL NULL) when there is
// nothing to persist.
func marshalAttemptColumn(value any) any {
	if value == nil {
		return nil
	}
	if headers, ok := value.(map[string]string); ok && len(headers) == 0 {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil || len(data) == 0 || string(data) == "null" {
		return nil
	}
	return string(data)
}

// unmarshalAttemptBody decodes a stored attempt response body column back into a
// JSON value (object/array/string), or nil when absent.
func unmarshalAttemptBody(raw *string) any {
	if raw == nil || *raw == "" {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(*raw), &value); err != nil {
		return *raw
	}
	return value
}

// unmarshalAttemptHeaders decodes a stored attempt response headers column back
// into a redacted header map, or nil when absent.
func unmarshalAttemptHeaders(raw *string) map[string]string {
	if raw == nil || *raw == "" {
		return nil
	}
	var headers map[string]string
	if err := json.Unmarshal([]byte(*raw), &headers); err != nil || len(headers) == 0 {
		return nil
	}
	return headers
}

// GateAttemptCapture clears per-attempt response bodies and/or headers that the
// audit configuration did not opt into, leaving the structured error fields
// (type / code / status / message) untouched. It mutates and returns attempts.
func GateAttemptCapture(attempts []AttemptSnapshot, cfg Config) []AttemptSnapshot {
	if len(attempts) == 0 || (cfg.LogBodies && cfg.LogHeaders) {
		return attempts
	}
	for i := range attempts {
		if !cfg.LogBodies {
			attempts[i].ResponseBody = nil
		}
		if !cfg.LogHeaders {
			attempts[i].ResponseHeaders = nil
		}
	}
	return attempts
}
