package streaming

import (
	"bytes"
	"encoding/json"
	"io"
)

const maxPendingEventBytes = 256 * 1024

var (
	lfEventBoundary   = []byte("\n\n")
	crlfEventBoundary = []byte("\r\n\r\n")
	dataPrefix        = []byte("data:")
	donePayload       = []byte("[DONE]")
)

// Observer receives parsed JSON SSE payloads in stream order.
// Implementations must treat the payload as read-only.
type Observer interface {
	OnJSONEvent(payload map[string]interface{})
	OnStreamClose()
}

// ObservedSSEStream proxies bytes unchanged while parsing SSE JSON events once
// and fanning them out to observers.
type ObservedSSEStream struct {
	io.ReadCloser
	observers  []Observer
	pending    []byte
	closed     bool
	discarding bool
}

// NewObservedSSEStream returns the original stream when there are no observers.
func NewObservedSSEStream(stream io.ReadCloser, observers ...Observer) io.ReadCloser {
	filtered := make([]Observer, 0, len(observers))
	for _, observer := range observers {
		if observer != nil {
			filtered = append(filtered, observer)
		}
	}
	if len(filtered) == 0 {
		return stream
	}
	return &ObservedSSEStream{
		ReadCloser: stream,
		observers:  filtered,
	}
}

func (s *ObservedSSEStream) Read(p []byte) (n int, err error) {
	n, err = s.ReadCloser.Read(p)
	if n > 0 {
		s.processChunk(p[:n])
	}
	return n, err
}

func (s *ObservedSSEStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true

	if len(s.pending) > 0 {
		s.processBufferedEvents(s.pending)
		s.pending = nil
	}

	for _, observer := range s.observers {
		observer.OnStreamClose()
	}
	return s.ReadCloser.Close()
}

func (s *ObservedSSEStream) processChunk(data []byte) {
	if len(s.pending) > 0 {
		idx, sepLen := nextEventBoundary(data)
		if idx == -1 {
			if len(data) > maxPendingEventBytes || len(s.pending) > maxPendingEventBytes-len(data) {
				s.pending = nil
				s.discarding = true
				return
			}

			combinedLen := len(s.pending) + len(data)
			combined := make([]byte, combinedLen)
			copy(combined, s.pending)
			copy(combined[len(s.pending):], data)
			s.pending = combined
			return
		}

		if idx > maxPendingEventBytes || len(s.pending) > maxPendingEventBytes-idx {
			s.pending = nil
			data = data[idx+sepLen:]
		} else {
			combinedLen := len(s.pending) + idx
			event := make([]byte, combinedLen)
			copy(event, s.pending)
			copy(event[len(s.pending):], data[:idx])
			s.pending = nil
			s.processEvent(event)
			data = data[idx+sepLen:]
		}
	}

	for len(data) > 0 {
		if s.discarding {
			idx, sepLen := nextEventBoundary(data)
			if idx == -1 {
				return
			}
			data = data[idx+sepLen:]
			s.discarding = false
			continue
		}

		idx, sepLen := nextEventBoundary(data)
		if idx == -1 {
			s.savePending(data)
			return
		}

		if idx > maxPendingEventBytes {
			data = data[idx+sepLen:]
			continue
		}
		if idx > 0 {
			s.processEvent(data[:idx])
		}
		data = data[idx+sepLen:]
	}
}

func (s *ObservedSSEStream) processBufferedEvents(data []byte) {
	for len(data) > 0 {
		idx, sepLen := nextEventBoundary(data)
		if idx == -1 {
			s.processEvent(data)
			return
		}
		if idx > 0 {
			s.processEvent(data[:idx])
		}
		data = data[idx+sepLen:]
	}
}

func (s *ObservedSSEStream) processEvent(event []byte) {
	lines := bytes.Split(event, []byte("\n"))
	payloadLines := make([][]byte, 0, len(lines))
	for _, line := range lines {
		jsonData, ok := parseDataLine(line)
		if !ok {
			continue
		}
		payloadLines = append(payloadLines, jsonData)
	}
	if len(payloadLines) == 0 {
		return
	}

	jsonData := bytes.Join(payloadLines, []byte("\n"))
	if bytes.Equal(jsonData, donePayload) {
		return
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(jsonData, &payload); err != nil {
		return
	}
	for _, observer := range s.observers {
		observer.OnJSONEvent(payload)
	}
}

func nextEventBoundary(data []byte) (idx int, sepLen int) {
	lfIdx := bytes.Index(data, lfEventBoundary)
	crlfIdx := bytes.Index(data, crlfEventBoundary)

	switch {
	case lfIdx == -1:
		if crlfIdx == -1 {
			return -1, 0
		}
		return crlfIdx, len(crlfEventBoundary)
	case crlfIdx == -1 || lfIdx < crlfIdx:
		return lfIdx, len(lfEventBoundary)
	default:
		return crlfIdx, len(crlfEventBoundary)
	}
}

func parseDataLine(line []byte) ([]byte, bool) {
	line = bytes.TrimSuffix(line, []byte("\r"))
	if !bytes.HasPrefix(line, dataPrefix) {
		return nil, false
	}
	payload := bytes.TrimPrefix(line, dataPrefix)
	if len(payload) > 0 && payload[0] == ' ' {
		payload = payload[1:]
	}
	return payload, true
}

func (s *ObservedSSEStream) savePending(data []byte) {
	if len(data) == 0 {
		return
	}
	if len(data) > maxPendingEventBytes {
		s.pending = nil
		s.discarding = true
		return
	}
	s.pending = append(s.pending[:0], data...)
}
