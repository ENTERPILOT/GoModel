package responsecache

import (
	"bytes"
	"github.com/enterpilot/gomodel/internal/streaming"

	"github.com/goccy/go-json"
)

// responsesEventType returns a payload's Responses event type, or "" for a
// payload without one. The type field is decoded rather than substring-matched
// so whitespace or key order in the payload cannot smuggle a terminal event
// past validation.
func responsesEventType(payload []byte) string {
	var event struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		// json.Valid already accepted the payload; a non-object payload
		// simply has no event type.
		return ""
	}
	return event.Type
}

// validateCacheableSSE reports whether raw is a complete, cache-safe SSE body.
// Cacheable streams must be fully framed, contain at least one JSON data payload,
// and terminate with a final [DONE] event or, for a Responses stream from an
// upstream that sends no [DONE], with response.completed as the last payload.
// A response that ended incomplete (interrupted upstream) or failed is not a
// reusable answer even though it is fully framed, so a stream carrying one is
// never cacheable.
func validateCacheableSSE(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}

	sawJSONPayload := false
	sawDone := false
	endsCompleted := false

	for len(raw) > 0 {
		remaining := raw
		idx, sepLen := streaming.NextEventBoundary(raw)
		event := remaining
		raw = nil
		if idx != -1 {
			event = remaining[:idx]
			raw = remaining[idx+sepLen:]
		}

		payload, hasData := streaming.EventData(event)
		if sawDone {
			return false
		}
		// A named error event is never a reusable answer, whatever its data.
		if isErrorEvent(event) {
			return false
		}
		// Only keep-alive comments may follow a final response.completed:
		// any other event, even one without data, means the stream went on.
		if endsCompleted && !commentsOnly(event) {
			endsCompleted = false
		}
		if !hasData {
			continue
		}
		if len(bytes.TrimSpace(payload)) == 0 {
			continue
		}
		if bytes.Equal(payload, cacheDonePayload) {
			sawDone = true
			continue
		}
		if !json.Valid(payload) {
			return false
		}
		switch responsesEventType(payload) {
		case "response.incomplete", "response.failed":
			return false
		case "response.completed":
			// It ends the stream only once dispatched by a blank line: an
			// event cut before it may still have had more fields.
			endsCompleted = idx != -1
		default:
			endsCompleted = false
		}
		sawJSONPayload = true
	}

	return sawJSONPayload && (sawDone || endsCompleted)
}

// commentsOnly reports whether every non-blank line of an SSE event is a
// comment (starts with ':').
func commentsOnly(event []byte) bool {
	for line := range bytes.Lines(event) {
		// Only the terminator is stripped: a line starting with a space is
		// a field line, not a comment.
		line = bytes.TrimRight(line, "\r\n")
		if len(line) > 0 && line[0] != ':' {
			return false
		}
	}
	return true
}

// isErrorEvent reports whether an SSE event is named "error".
func isErrorEvent(event []byte) bool {
	for line := range bytes.Lines(event) {
		name, ok := bytes.CutPrefix(bytes.TrimRight(line, "\r\n"), []byte("event:"))
		if ok && string(bytes.TrimSpace(name)) == "error" {
			return true
		}
	}
	return false
}
