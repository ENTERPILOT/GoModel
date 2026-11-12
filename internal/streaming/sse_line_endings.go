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

// indexLineEnd returns the offset of the first CR or LF in data, or -1.
func indexLineEnd(data []byte) int {
	lf := bytes.IndexByte(data, '\n')
	if lf == -1 {
		return bytes.IndexByte(data, '\r')
	}
	if cr := bytes.IndexByte(data[:lf], '\r'); cr != -1 {
		return cr
	}
	return lf
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

// nextJoinedEventBoundaryAnyEOL is nextJoinedEventBoundary for lines ending
// in CRLF, LF or a bare CR: the first boundary in prefix followed by data,
// prefix being the last bytes before data. The two are joined to be read,
// which only the fragments of an event over the scanner's limit cost.
func nextJoinedEventBoundaryAnyEOL(prefix, data []byte) (idx int, sepLen int) {
	if len(prefix) == 0 {
		return NextEventBoundaryAnyEOL(data)
	}
	joined := make([]byte, 0, len(prefix)+len(data))
	return NextEventBoundaryAnyEOL(append(append(joined, prefix...), data...))
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
