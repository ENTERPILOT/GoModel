package streaming

import "bytes"

// The SSE specification ends a line with CRLF, LF or a bare CR, and an event
// with a blank line, so an event ends at any two line endings in a row.
// NextEventBoundary knows the two framings providers send, "\n\n" and
// "\r\n\r\n"; the functions here know them all, for the readers that must not
// miss an event whatever its framing (EventScanner.AnyLineEnding).

// NextEventBoundaryAnyEOL is NextEventBoundary for lines ending in CRLF, LF or
// a bare CR. A CR that ends data may be the first half of a CRLF. After a CRLF
// it is taken for one, and the boundary is not reported until the next byte
// tells: a stream framed with CRLF splits exactly as NextEventBoundary splits
// it. After an LF or a CR it ends the blank line at once, so an event framed
// with bare CRs is never held back for a byte that may not come.
func NextEventBoundaryAnyEOL(data []byte) (idx int, sepLen int) {
	for at := 0; ; {
		i := indexLineEnd(data[at:])
		if i == -1 {
			return -1, 0
		}
		at += i
		first := lineEndLen(data[at:])
		next := at + first
		if next >= len(data) {
			return -1, 0
		}
		switch data[next] {
		case '\n':
			return at, first + 1
		case '\r':
			switch {
			case next+1 < len(data) && data[next+1] == '\n':
				return at, first + 2
			case next+1 == len(data) && first == 2:
				return -1, 0
			}
			return at, first + 1
		}
		at = next
	}
}

// EndsAtObservedBoundary reports whether raw, an event split off at any line
// ending, ends with a boundary NextEventBoundary knows too. An
// ObservedSSEStream then ends the event where raw ends; after an event that
// ends another way it reads on, into the events that follow.
func EndsAtObservedBoundary(raw []byte) bool {
	return bytes.HasSuffix(raw, lfEventBoundary) || bytes.HasSuffix(raw, crlfEventBoundary)
}

// lineEndWindowBytes is how far indexLineEnd looks for one line ending before
// it looks for the other.
const lineEndWindowBytes = 256

// indexLineEnd returns the offset of the first CR or LF in data, or -1. It
// reads data once, a window at a time, so the lines of an event that has only
// one of the two are not each searched to the end of the event for the other.
func indexLineEnd(data []byte) int {
	for at := 0; at < len(data); at += lineEndWindowBytes {
		window := data[at:min(at+lineEndWindowBytes, len(data))]
		lf := bytes.IndexByte(window, '\n')
		if lf != -1 {
			window = window[:lf]
		}
		if cr := bytes.IndexByte(window, '\r'); cr != -1 {
			return at + cr
		}
		if lf != -1 {
			return at + lf
		}
	}
	return -1
}

// lineEndLen returns the length of the line ending data starts with: 2 for
// CRLF, 1 for an LF or a CR.
func lineEndLen(data []byte) int {
	if len(data) > 1 && data[0] == '\r' && data[1] == '\n' {
		return 2
	}
	return 1
}

// CutLine splits data after its first line: the line, the line ending that
// follows it (CRLF, LF or a bare CR; empty when data ends without one) and
// the rest.
func CutLine(data []byte) (line, ending, rest []byte) {
	i := indexLineEnd(data)
	if i == -1 {
		return data, nil, nil
	}
	end := i + lineEndLen(data[i:])
	return data[:i], data[i:end], data[end:]
}

// joinLookaheadBytes is how many bytes of data decide a boundary that starts
// in the maxBoundaryTailBytes before it: the rest of a CRLF, a second CRLF and
// the byte that tells a bare CR from one.
const joinLookaheadBytes = 4

// nextJoinedEventBoundaryAnyEOL is nextJoinedEventBoundary for lines ending
// in CRLF, LF or a bare CR: the first boundary in prefix followed by data,
// prefix being the last bytes before data. Only the end of prefix and the
// first bytes of data are joined to be read, in a buffer of a fixed size: a
// boundary that starts in prefix ends there, and one that starts in data is
// found in data alone.
func nextJoinedEventBoundaryAnyEOL(prefix, data []byte) (idx int, sepLen int) {
	skipped := max(len(prefix)-maxBoundaryTailBytes, 0)
	tail := prefix[skipped:]
	var joined [maxBoundaryTailBytes + joinLookaheadBytes]byte
	n := copy(joined[:], tail)
	n += copy(joined[n:], data)
	idx, sepLen = NextEventBoundaryAnyEOL(joined[:n])
	if n == len(tail)+len(data) || (idx != -1 && idx < len(tail)) {
		// All of data was read, or the boundary starts in prefix.
		if idx != -1 {
			idx += skipped
		}
		return idx, sepLen
	}
	if idx, sepLen = NextEventBoundaryAnyEOL(data); idx != -1 {
		idx += len(prefix)
	}
	return idx, sepLen
}

// parseRawEventAnyEOL is parseRawEvent for lines ending in CRLF, LF or a bare
// CR.
func parseRawEventAnyEOL(raw, body []byte) RawEvent {
	ev := RawEvent{Raw: raw}
	var more [][]byte // the data lines after the first
	for len(body) > 0 {
		var line []byte
		line, _, body = CutLine(body)
		data, ok := ParseDataLine(line)
		switch {
		case !ok:
			parseEventLine(&ev, line)
		case ev.Data == nil:
			ev.Data = data
		default:
			more = append(more, data)
		}
	}
	if len(more) > 0 {
		ev.Data = bytes.Join(append([][]byte{ev.Data}, more...), []byte("\n"))
	}
	ev.Comment = ev.Data == nil
	return ev
}
