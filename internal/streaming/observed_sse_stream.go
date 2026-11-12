package streaming

import (
	"bytes"
	"io"

	"github.com/goccy/go-json"
)

// maxPendingEventBytes is the default event limit for EventScanner, and the
// pending buffer size an ObservedSSEStream keeps between events.
const maxPendingEventBytes = 256 * 1024

// MaxObservedEventBytes bounds one event an ObservedSSEStream buffers for its
// observers, and the event a stream rewriter holds whole. It is far above
// maxPendingEventBytes because a terminal Responses event carries the whole
// response: a streamed image generation repeats every image's base64 in
// response.completed, and usage, audit and the stored response all come from
// that event. Memory is only taken when such an event arrives; a larger one
// is relayed to the client but not observed.
const MaxObservedEventBytes = 64 << 20
const maxBoundaryTailBytes = 3

var (
	lfEventBoundary   = []byte("\n\n")
	crlfEventBoundary = []byte("\r\n\r\n")
	crlfLineEnd       = []byte("\r\n")
	dataPrefix        = []byte("data:")
	donePayload       = []byte("[DONE]")
)

// Observer receives parsed JSON SSE payloads in stream order.
// Implementations must treat the payload as read-only.
type Observer interface {
	OnJSONEvent(payload map[string]any)
	OnStreamClose()
}

// EventFilter is an optional Observer extension. Observers that consume only
// specific payloads can report disinterest from the raw event bytes; when no
// observer wants an event, the stream skips JSON decoding entirely. Filters
// must under-approximate disinterest only: an observer may still receive
// events it did not ask for when another observer wants them.
type EventFilter interface {
	WantsJSONEvent(raw []byte) bool
}

// ObservedSSEStream proxies bytes unchanged while parsing SSE JSON events once
// and fanning them out to observers.
type ObservedSSEStream struct {
	io.ReadCloser
	observers []Observer
	// filters holds each observer's EventFilter, resolved once at
	// construction. It is authoritative only when every observer contributed
	// one (len(filters) == len(observers)); otherwise every event is decoded,
	// which also keeps directly-constructed zero-value streams safe.
	filters     []EventFilter
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

	observed := &ObservedSSEStream{
		ReadCloser: stream,
		observers:  filtered,
	}
	for _, observer := range filtered {
		filter, ok := observer.(EventFilter)
		if !ok {
			// An observer without a filter wants every event; leave filters
			// short so payloadWanted always decodes.
			observed.filters = nil
			break
		}
		observed.filters = append(observed.filters, filter)
	}
	return observed
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
			if len(data) > MaxObservedEventBytes || pendingLen > MaxObservedEventBytes-len(data) {
				s.startDiscarding(s.pending, data)
				return
			}
			s.pending = append(s.pending, data...)
			return
		}

		offset := dataOffsetAfterBoundary(pendingLen, idx, sepLen)
		if idx <= MaxObservedEventBytes {
			if idx > pendingLen {
				s.pending = append(s.pending, data[:idx-pendingLen]...)
			}
			// Observers receive decoded payloads only, so the event may
			// alias pending for the duration of this call.
			s.processEvent(s.pending[:idx])
		}
		s.resetPending()
		data = data[offset:]
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

		idx, sepLen := NextEventBoundary(data)
		if idx == -1 {
			s.savePending(data)
			return
		}

		if idx > MaxObservedEventBytes {
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
		idx, sepLen := NextEventBoundary(data)
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
	if jsonData, ok := EventData(event); ok {
		s.dispatchPayload(jsonData)
	}
}

func (s *ObservedSSEStream) dispatchPayload(jsonData []byte) {
	if bytes.Equal(jsonData, donePayload) {
		return
	}
	if !s.payloadWanted(jsonData) {
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

func (s *ObservedSSEStream) payloadWanted(jsonData []byte) bool {
	if len(s.filters) != len(s.observers) {
		return true
	}
	for _, filter := range s.filters {
		if filter.WantsJSONEvent(jsonData) {
			return true
		}
	}
	return false
}

// NextEventBoundary returns the offset of the next SSE event boundary in data
// and the length of the separator found there, or -1 when data holds no
// boundary yet. Both the LF and CRLF forms are recognized, and the earlier one
// wins when both appear.
func NextEventBoundary(data []byte) (idx int, sepLen int) {
	// Every boundary holds a line feed: "\n\n" starts with one and
	// "\r\n\r\n" has one at offset 1. Checking the bytes around each line
	// feed in order finds the earliest boundary in a single pass.
	for start := 0; ; {
		i := bytes.IndexByte(data[start:], '\n')
		if i == -1 {
			return -1, 0
		}
		i += start
		if i+1 < len(data) && data[i+1] == '\n' {
			return i, len(lfEventBoundary)
		}
		if i > 0 && data[i-1] == '\r' && bytes.HasPrefix(data[i+1:], crlfLineEnd) {
			return i - 1, len(crlfEventBoundary)
		}
		start = i + 1
	}
}

// ParseDataLine returns the payload of one SSE "data:" line, dropping the
// optional single space after the colon, and reports whether line was a data
// line at all. A trailing CR is trimmed, so CRLF framing needs no special
// handling from the caller.
func ParseDataLine(line []byte) ([]byte, bool) {
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

// EventData returns the payload of one SSE event, its data lines joined with
// "\n", and reports whether the event had any data line. Other fields and
// comment lines are ignored. A single data line, the common shape, is returned
// as a subslice of event without copying.
func EventData(event []byte) ([]byte, bool) {
	var payload []byte
	found, joined := false, false
	for len(event) > 0 {
		line := event
		event = nil
		if i := bytes.IndexByte(line, '\n'); i != -1 {
			line, event = line[:i], line[i+1:]
		}
		data, ok := ParseDataLine(line)
		switch {
		case !ok:
		case !found:
			payload, found = data, true
		case !joined:
			// payload aliases event, so the first join must copy.
			payload = append(append(append(make([]byte, 0, len(payload)+1+len(data)), payload...), '\n'), data...)
			joined = true
		default:
			payload = append(append(payload, '\n'), data...)
		}
	}
	return payload, found
}

// resetPending empties pending for reuse, releasing it instead when one large
// event grew it past the size kept between events.
func (s *ObservedSSEStream) resetPending() {
	if cap(s.pending) > maxPendingEventBytes {
		s.pending = nil
		return
	}
	s.pending = s.pending[:0]
}

func (s *ObservedSSEStream) savePending(data []byte) {
	if len(data) == 0 {
		return
	}
	if len(data) > MaxObservedEventBytes {
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

	dataIdx, dataSepLen := NextEventBoundary(data)
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
