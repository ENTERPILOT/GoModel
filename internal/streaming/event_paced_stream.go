package streaming

import "io"

// eventPacedStream hands a stream out at most one complete event per Read.
type eventPacedStream struct {
	io.ReadCloser
	buf   []byte
	start int
	err   error
	ends  eventEndFinder
}

// NewEventPacedStream returns stream so that no Read returns bytes past the
// end of its first complete event, wherever the upstream's own reads split
// the stream. Wrapped beneath an ObservedSSEStream, it makes observers see
// each event just before the reader relays it, never together with the events
// that arrived in the same upstream read: a value an observer derives from
// the stream so far (the cost of the usage so far) is then current for the
// event being relayed.
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
	if len(rest) > len(p) {
		rest = rest[:len(p)]
	}
	n := copy(p, rest[:s.ends.next(rest)])
	s.start += n
	if s.start == len(s.buf) {
		return n, s.err
	}
	return n, nil
}

// eventEndFinder finds the ends of the events of a stream handed to it in
// pieces. It remembers the line ending the last piece ended with, so an event
// whose blank line was split between two pieces ends where it does in the
// stream, not with the event after it. Lines end as NextEventBoundaryAnyEOL
// ends them.
type eventEndFinder struct {
	state lineEndState
}

// lineEndState is where the bytes read so far stand in a line.
type lineEndState uint8

const (
	inLine      lineEndState = iota // in a line, or at the start of an event
	afterLF                         // a line just ended with an LF
	afterCR                         // a line just ended with a CR, the first half of a CRLF if an LF follows
	afterCRLF                       // a line just ended with a CRLF
	afterCRLFCR                     // a CRLF, then a CR: the blank line of a CRLF stream once its LF comes
)

// next returns how many bytes of data end with the first event that ends in
// data, or len(data) when none does. data continues where the previous call's
// answer ended.
func (f *eventEndFinder) next(data []byte) int {
	for i := 0; i < len(data); i++ {
		c := data[i]
		if f.state == afterCRLFCR {
			// Only ever the state a piece ended in: with an LF this one
			// completes the blank line, and without, the CR alone was it.
			f.state = inLine
			if c == '\n' {
				return i + 1
			}
		}
		if f.state == inLine {
			// Nothing but a line ending changes the state of a line.
			skip := indexLineEnd(data[i:])
			if skip == -1 {
				return len(data)
			}
			i += skip
			c = data[i]
		}
		switch {
		case c != '\r' && c != '\n':
			f.state = inLine
		case f.state == inLine && c == '\n':
			f.state = afterLF
		case f.state == inLine:
			f.state = afterCR
		case f.state == afterCR && c == '\n':
			f.state = afterCRLF
		case c == '\n':
			f.state = inLine
			return i + 1
		case i+1 < len(data) && data[i+1] == '\n':
			f.state = inLine
			return i + 2
		case i+1 == len(data) && f.state == afterCRLF:
			f.state = afterCRLFCR
		default:
			f.state = inLine
			return i + 1
		}
	}
	return len(data)
}
