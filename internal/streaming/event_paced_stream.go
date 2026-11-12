package streaming

import "io"

// eventPacedStream hands a stream out at most one complete event per Read.
type eventPacedStream struct {
	io.ReadCloser
	buf   []byte
	start int
	err   error
}

// NewEventPacedStream returns stream so that no Read returns bytes past the
// end of its first complete event. Wrapped beneath an ObservedSSEStream, it
// makes observers see each event just before the reader relays it, never
// together with the events that arrived in the same upstream read: a value an
// observer derives from the stream so far (the cost of the usage so far) is
// then current for the event being relayed.
func NewEventPacedStream(stream io.ReadCloser) io.ReadCloser {
	return &eventPacedStream{ReadCloser: stream}
}

func (s *eventPacedStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if s.start == len(s.buf) {
		if s.err != nil {
			return 0, s.err
		}
		if cap(s.buf) < len(p) {
			s.buf = make([]byte, len(p))
		}
		n, err := s.ReadCloser.Read(s.buf[:len(p)])
		s.buf, s.start, s.err = s.buf[:n], 0, err
		if n == 0 {
			return 0, err
		}
	}
	rest := s.buf[s.start:]
	end := len(rest)
	if idx, sepLen := NextEventBoundary(rest); idx >= 0 {
		end = idx + sepLen
	}
	n := copy(p, rest[:end])
	s.start += n
	if s.start == len(s.buf) {
		return n, s.err
	}
	return n, nil
}
