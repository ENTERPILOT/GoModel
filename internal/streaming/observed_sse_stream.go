package streaming

import (
	"bytes"
	"encoding/json"
	"io"
)

const maxPendingEventBytes = 256 * 1024

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
	observers []Observer
	pending   []byte
	closed    bool
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
		combined := make([]byte, len(s.pending)+len(data))
		copy(combined, s.pending)
		copy(combined[len(s.pending):], data)
		data = combined
		s.pending = nil
	}

	for {
		idx := bytes.Index(data, []byte("\n\n"))
		if idx == -1 {
			s.savePending(data)
			return
		}

		s.processEvent(data[:idx])
		data = data[idx+2:]
	}
}

func (s *ObservedSSEStream) processBufferedEvents(data []byte) {
	for _, event := range bytes.Split(data, []byte("\n\n")) {
		if len(event) == 0 {
			continue
		}
		s.processEvent(event)
	}
}

func (s *ObservedSSEStream) processEvent(event []byte) {
	lines := bytes.Split(event, []byte("\n"))
	for _, line := range lines {
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		jsonData := bytes.TrimPrefix(line, []byte("data: "))
		if bytes.Equal(jsonData, []byte("[DONE]")) {
			continue
		}

		var payload map[string]interface{}
		if err := json.Unmarshal(jsonData, &payload); err != nil {
			continue
		}
		for _, observer := range s.observers {
			observer.OnJSONEvent(payload)
		}
	}
}

func (s *ObservedSSEStream) savePending(data []byte) {
	if len(data) == 0 {
		return
	}
	if len(data) > maxPendingEventBytes {
		data = data[len(data)-maxPendingEventBytes:]
	}
	s.pending = append(s.pending[:0], data...)
}
