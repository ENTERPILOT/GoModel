package responsecache

import (
	"bytes"

	"github.com/goccy/go-json"
)

// isNonCacheableTerminalEvent reports whether a payload is a terminal
// Responses event that must never be replayed from cache: a response that
// ended incomplete (interrupted upstream) or failed is not a reusable answer
// even though it is fully framed and ends with [DONE]. The type field is
// decoded rather than substring-matched so whitespace or key order in the
// payload cannot smuggle such an event past validation.
func isNonCacheableTerminalEvent(payload []byte) bool {
	var event struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		// json.Valid already accepted the payload; a non-object payload
		// simply has no event type.
		return false
	}
	return event.Type == "response.incomplete" || event.Type == "response.failed"
}

// validateCacheableSSE reports whether raw is a complete, cache-safe SSE body.
// Cacheable streams must be fully framed, contain at least one JSON data payload,
// terminate with a final [DONE] event, and not carry an incomplete or failed
// terminal Responses event.
func validateCacheableSSE(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}

	sawJSONPayload := false
	sawDone := false

	for len(raw) > 0 {
		remaining := raw
		idx, sepLen := nextCacheEventBoundary(raw)
		event := remaining
		raw = nil
		if idx != -1 {
			event = remaining[:idx]
			raw = remaining[idx+sepLen:]
		}

		payload, hasData := sseEventPayload(event)
		if sawDone {
			return false
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
		if isNonCacheableTerminalEvent(payload) {
			return false
		}
		sawJSONPayload = true
	}

	return sawJSONPayload && sawDone
}

func sseEventPayload(event []byte) ([]byte, bool) {
	lines := bytes.Split(event, []byte("\n"))
	payloadLines := make([][]byte, 0, len(lines))
	for _, line := range lines {
		data, ok := parseCacheDataLine(line)
		if !ok {
			continue
		}
		payloadLines = append(payloadLines, data)
	}
	if len(payloadLines) == 0 {
		return nil, false
	}
	return bytes.Join(payloadLines, []byte("\n")), true
}
