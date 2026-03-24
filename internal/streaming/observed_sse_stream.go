package streaming

import (
	"bytes"
	"encoding/json"
	"io"
)

const maxPendingEventBytes = 256 * 1024
const maxBoundaryTailBytes = 3

var (
	lfEventBoundary   = []byte("\n\n")
	crlfEventBoundary = []byte("\r\n\r\n")
	dataPrefix        = []byte("data:")
	donePayload       = []byte("[DONE]")
)

// Observer receives parsed JSON SSE payloads in stream order.
// Implementations must treat the payload as read-only.
type Observer interface {
	OnJSONEvent(payload map[string]any)
	OnStreamClose()
}

// ObservedSSEStream proxies bytes unchanged while parsing SSE JSON events once
// and fanning them out to observers.
type ObservedSSEStream struct {
	io.ReadCloser
	observers   []Observer
	pending     []byte
	discardTail []byte
	closed      bool
	discarding  bool
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
		pendingLen := len(s.pending)
		idx, sepLen := nextJoinedEventBoundary(s.pending, data)
		if idx == -1 {
			if len(data) > maxPendingEventBytes || pendingLen > maxPendingEventBytes-len(data) {
				s.startDiscarding(s.pending, data)
				return
			}

			combinedLen := pendingLen + len(data)
			combined := make([]byte, combinedLen)
			copy(combined, s.pending)
			copy(combined[pendingLen:], data)
			s.pending = combined
			return
		}

		if idx > maxPendingEventBytes {
			s.pending = nil
			data = data[dataOffsetAfterBoundary(pendingLen, idx, sepLen):]
		} else if idx < pendingLen {
			event := append([]byte(nil), s.pending[:idx]...)
			s.pending = nil
			s.processEvent(event)
			data = data[dataOffsetAfterBoundary(pendingLen, idx, sepLen):]
		} else {
			dataIdx := idx - pendingLen
			if pendingLen > maxPendingEventBytes-dataIdx {
				s.pending = nil
				data = data[dataIdx+sepLen:]
			} else {
				combinedLen := pendingLen + dataIdx
				event := make([]byte, combinedLen)
				copy(event, s.pending)
				copy(event[pendingLen:], data[:dataIdx])
				s.pending = nil
				s.processEvent(event)
				data = data[dataIdx+sepLen:]
			}
		}
	}

	for len(data) > 0 {
		if s.discarding {
			idx, sepLen := nextJoinedEventBoundary(s.discardTail, data)
			if idx == -1 {
				s.discardTail = joinedSuffix(s.discardTail, data, maxBoundaryTailBytes)
				return
			}
			data = data[dataOffsetAfterBoundary(len(s.discardTail), idx, sepLen):]
			s.discarding = false
			s.discardTail = nil
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

	var payload map[string]any
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
		s.startDiscarding(nil, data)
		return
	}
	s.pending = append(s.pending[:0], data...)
}

func (s *ObservedSSEStream) startDiscarding(prefix, data []byte) {
	s.pending = nil
	s.discarding = true
	s.discardTail = joinedSuffix(prefix, data, maxBoundaryTailBytes)
}

func nextJoinedEventBoundary(prefix, data []byte) (idx int, sepLen int) {
	idx = -1

	crossIdx, crossSepLen := nextBoundaryAcrossJoin(prefix, data)
	if crossIdx != -1 {
		idx, sepLen = crossIdx, crossSepLen
	}

	dataIdx, dataSepLen := nextEventBoundary(data)
	if dataIdx != -1 {
		combinedIdx := len(prefix) + dataIdx
		if idx == -1 || combinedIdx < idx {
			idx, sepLen = combinedIdx, dataSepLen
		}
	}

	return idx, sepLen
}

func nextBoundaryAcrossJoin(prefix, data []byte) (idx int, sepLen int) {
	idx = -1
	start := max(len(prefix)-maxBoundaryTailBytes, 0)

	for offset := start; offset < len(prefix); offset++ {
		for _, boundary := range [][]byte{lfEventBoundary, crlfEventBoundary} {
			if offset+len(boundary) <= len(prefix) {
				continue
			}
			if joinedBytesMatch(prefix, data, offset, boundary) {
				if idx == -1 || offset < idx {
					idx = offset
					sepLen = len(boundary)
				}
			}
		}
	}

	return idx, sepLen
}

func joinedBytesMatch(prefix, data []byte, start int, boundary []byte) bool {
	for i, want := range boundary {
		pos := start + i
		var got byte
		switch {
		case pos < len(prefix):
			got = prefix[pos]
		case pos-len(prefix) < len(data):
			got = data[pos-len(prefix)]
		default:
			return false
		}
		if got != want {
			return false
		}
	}
	return true
}

func dataOffsetAfterBoundary(prefixLen, idx, sepLen int) int {
	if idx >= prefixLen {
		return idx - prefixLen + sepLen
	}

	prefixConsumed := prefixLen - idx
	if prefixConsumed >= sepLen {
		return 0
	}
	return sepLen - prefixConsumed
}

func joinedSuffix(prefix, data []byte, n int) []byte {
	if n <= 0 {
		return nil
	}
	if len(data) >= n {
		return append([]byte(nil), data[len(data)-n:]...)
	}

	needPrefix := min(n-len(data), len(prefix))
	result := make([]byte, 0, n)
	result = append(result, prefix[len(prefix)-needPrefix:]...)
	result = append(result, data...)
	return result
}
