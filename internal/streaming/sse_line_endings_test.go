package streaming

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNextEventBoundaryAnyEOL(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		wantIdx int
		wantSep int
	}{
		{name: "LF", data: "data: 1\n\ndata: 2", wantIdx: 7, wantSep: 2},
		{name: "CRLF", data: "data: 1\r\n\r\ndata: 2", wantIdx: 7, wantSep: 4},
		{name: "bare CR", data: "data: 1\r\rdata: 2", wantIdx: 7, wantSep: 2},
		{name: "bare CR at the end of the data", data: "data: 1\r\r", wantIdx: 7, wantSep: 2},
		{name: "LF then CRLF", data: "data: 1\n\r\ndata: 2", wantIdx: 7, wantSep: 3},
		{name: "LF then a CR that ends the data", data: "data: 1\n\r", wantIdx: 7, wantSep: 2},
		{name: "CRLF then LF", data: "data: 1\r\n\ndata: 2", wantIdx: 7, wantSep: 3},
		{name: "CR then CRLF", data: "data: 1\r\r\ndata: 2", wantIdx: 7, wantSep: 3},
		{name: "CRLF then a bare CR", data: "data: 1\r\n\rdata: 2", wantIdx: 7, wantSep: 3},
		{name: "several lines", data: "event: x\rid: 1\rdata: 1\r\r", wantIdx: 22, wantSep: 2},
		{name: "blank line first", data: "\n\ndata: 1\n\n", wantIdx: 0, wantSep: 2},
		{name: "no line ending", data: "data: 1", wantIdx: -1},
		{name: "one line ending", data: "data: 1\n", wantIdx: -1},
		{name: "a CR that may start a CRLF", data: "data: 1\r", wantIdx: -1},
		{name: "half of a CRLF blank line", data: "data: 1\r\n\r", wantIdx: -1},
		{name: "empty", data: "", wantIdx: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx, sep := NextEventBoundaryAnyEOL([]byte(tt.data))

			assert.Equal(t, tt.wantIdx, idx)
			assert.Equal(t, tt.wantSep, sep)
		})
	}
}

// Streams framed the two ways providers frame them split exactly as
// NextEventBoundary splits them.
func TestNextEventBoundaryAnyEOL_AgreesOnProviderFramings(t *testing.T) {
	for _, stream := range []string{
		"data: 1\n\nevent: x\ndata: 2\n\n: ping\n\ndata: [DONE]\n\n",
		"data: 1\r\n\r\nevent: x\r\ndata: 2\r\n\r\n: ping\r\n\r\ndata: [DONE]\r\n\r\n",
	} {
		for end := 0; end <= len(stream); end++ {
			wantIdx, wantSep := NextEventBoundary([]byte(stream[:end]))

			idx, sep := NextEventBoundaryAnyEOL([]byte(stream[:end]))

			assert.Equal(t, wantIdx, idx, "%q", stream[:end])
			assert.Equal(t, wantSep, sep, "%q", stream[:end])
		}
	}
}

// The first line ending is found wherever it lies in the windows the search
// reads, and whichever of the two comes first.
func TestIndexLineEnd(t *testing.T) {
	const w = lineEndWindowBytes
	pad := func(n int) string { return strings.Repeat("x", n) }
	tests := []struct {
		name string
		data string
		want int
	}{
		{name: "empty", data: "", want: -1},
		{name: "no line ending", data: pad(3 * w), want: -1},
		{name: "LF", data: "a\nb", want: 1},
		{name: "CR", data: "a\rb", want: 1},
		{name: "CR before LF", data: "a\rb\nc", want: 1},
		{name: "LF before CR", data: "a\nb\rc", want: 1},
		{name: "CRLF", data: "a\r\nb", want: 1},
		{name: "LF that ends a window", data: pad(w-1) + "\n\r", want: w - 1},
		{name: "CR that starts the next window", data: pad(w) + "\r\n", want: w},
		{name: "CR a window before an LF", data: pad(w-1) + "\r" + pad(w) + "\n", want: w - 1},
		{name: "LF a window before a CR", data: pad(w-1) + "\n" + pad(w) + "\r", want: w - 1},
		{name: "CR in the last, short window", data: pad(2*w+3) + "\r", want: 2*w + 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, indexLineEnd([]byte(tt.data)))
		})
	}
}

// An event ends where an ObservedSSEStream ends it exactly when
// NextEventBoundary finds its first boundary at its end.
func TestEndsAtObservedBoundary(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "LF", raw: "data: 1\n\n", want: true},
		{name: "CRLF", raw: "event: x\r\ndata: 1\r\n\r\n", want: true},
		{name: "CRLF then LF", raw: "data: 1\r\n\n", want: true},
		{name: "bare CR", raw: "data: 1\r\r"},
		{name: "LF then CRLF", raw: "data: 1\n\r\n"},
		{name: "LF then CR", raw: "data: 1\n\r"},
		{name: "CR then CRLF", raw: "data: 1\r\r\n"},
		{name: "CRLF then a bare CR", raw: "data: 1\r\n\r"},
		{name: "unterminated", raw: "data: 1"},
		{name: "empty", raw: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx, sep := NextEventBoundary([]byte(tt.raw))

			assert.Equal(t, tt.want, EndsAtObservedBoundary([]byte(tt.raw)))
			assert.Equal(t, tt.want, idx != -1 && idx+sep == len(tt.raw), "NextEventBoundary")
		})
	}
}

func TestCutLine(t *testing.T) {
	tests := []struct {
		data, line, ending, rest string
	}{
		{data: "a\nb", line: "a", ending: "\n", rest: "b"},
		{data: "a\r\nb", line: "a", ending: "\r\n", rest: "b"},
		{data: "a\rb\n", line: "a", ending: "\r", rest: "b\n"},
		{data: "\r\r", line: "", ending: "\r", rest: "\r"},
		{data: "a", line: "a"},
		{data: ""},
	}
	for _, tt := range tests {
		line, ending, rest := CutLine([]byte(tt.data))

		assert.Equal(t, tt.line, string(line), "%q", tt.data)
		assert.Equal(t, tt.ending, string(ending), "%q", tt.data)
		assert.Equal(t, tt.rest, string(rest), "%q", tt.data)
	}
}

// The boundary of a prefix followed by data is found without joining the two:
// for every tail a scanner keeps and every start of a chunk, it is the one the
// joined bytes have.
func TestNextJoinedEventBoundaryAnyEOL_AgreesWithTheJoinedBytes(t *testing.T) {
	var sequences func(n int) []string
	sequences = func(n int) []string {
		if n == 0 {
			return []string{""}
		}
		var out []string
		for _, rest := range sequences(n - 1) {
			out = append(out, "\r"+rest, "\n"+rest, "a"+rest)
		}
		return out
	}
	for prefixLen := 0; prefixLen <= maxBoundaryTailBytes; prefixLen++ {
		for dataLen := 0; dataLen <= 7; dataLen++ {
			for _, prefix := range sequences(prefixLen) {
				for _, data := range sequences(dataLen) {
					wantIdx, wantSep := NextEventBoundaryAnyEOL([]byte(prefix + data))

					idx, sep := nextJoinedEventBoundaryAnyEOL([]byte(prefix), []byte(data))

					require.Equal(t, wantIdx, idx, "%q + %q", prefix, data)
					require.Equal(t, wantSep, sep, "%q + %q", prefix, data)
				}
			}
		}
	}
}

// Finding the end of an oversized event copies no chunk: what is joined to be
// read has a fixed size, however large the chunk is.
func TestNextJoinedEventBoundaryAnyEOL_DoesNotCopyTheChunk(t *testing.T) {
	chunk := []byte(strings.Repeat("a", 1<<20) + "\n\ndata: next\n\n")
	tail := []byte("aa\r")

	var idx, sep int
	allocs := testing.AllocsPerRun(10, func() {
		idx, sep = nextJoinedEventBoundaryAnyEOL(tail, chunk)
	})

	assert.Equal(t, len(tail)+1<<20, idx)
	assert.Equal(t, 2, sep)
	assert.Zero(t, allocs)
}

// scannedEvent is one event a scanner found: its name and data, or what kind
// of block it is when it carries none, and its raw bytes.
type scannedEvent struct {
	name, data string
	kind       string // "comment" or "oversized"; empty for an event with data
	raw        string
}

// scanAnyEOL feeds pieces to a scanner that knows every line ending and
// returns the events it found.
func scanAnyEOL(limit int, pieces ...string) []scannedEvent {
	scanner := EventScanner{MaxEventBytes: limit, AnyLineEnding: true}
	var got []scannedEvent
	keep := func(events []RawEvent) {
		for _, ev := range events {
			switch {
			case ev.Oversized:
				got = append(got, scannedEvent{kind: "oversized", raw: string(ev.Raw)})
			case ev.Comment:
				got = append(got, scannedEvent{kind: "comment", raw: string(ev.Raw)})
			default:
				got = append(got, scannedEvent{name: ev.Name, data: string(ev.Data), raw: string(ev.Raw)})
			}
		}
	}
	for _, piece := range pieces {
		keep(scanner.Feed([]byte(piece)))
	}
	keep(scanner.Flush())
	return got
}

func TestEventScanner_AnyLineEnding(t *testing.T) {
	tests := []struct {
		name   string
		stream string
		want   []scannedEvent
	}{
		{
			name:   "bare CR",
			stream: "event: x\rdata: {\"a\":1}\r\r: ping\r\rdata: [DONE]\r\r",
			want: []scannedEvent{
				{name: "x", data: "{\"a\":1}", raw: "event: x\rdata: {\"a\":1}\r\r"},
				{kind: "comment", raw: ": ping\r\r"},
				{data: "[DONE]", raw: "data: [DONE]\r\r"},
			},
		},
		{
			name:   "LF then CRLF",
			stream: "data: 1\n\r\ndata: 2\n\r\n",
			want:   []scannedEvent{{data: "1", raw: "data: 1\n\r\n"}, {data: "2", raw: "data: 2\n\r\n"}},
		},
		{
			name:   "several data lines, each ending its own way",
			stream: "data: a\rdata: b\r\ndata: c\n\ndata: d",
			want:   []scannedEvent{{data: "a\nb\nc", raw: "data: a\rdata: b\r\ndata: c\n\n"}, {data: "d", raw: "data: d"}},
		},
		{
			name:   "CRLF",
			stream: "event: x\r\ndata: 1\r\n\r\n",
			want:   []scannedEvent{{name: "x", data: "1", raw: "event: x\r\ndata: 1\r\n\r\n"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, scanAnyEOL(0, tt.stream))
			// Fed byte by byte, the same payloads are found and every byte
			// is relayed once, in order.
			bytewise := make([]string, 0, len(tt.stream))
			for _, c := range []byte(tt.stream) {
				bytewise = append(bytewise, string(c))
			}
			got := scanAnyEOL(0, bytewise...)
			assert.Equal(t, payloads(tt.want), payloads(got))
			assert.Equal(t, tt.stream, raws(got))
		})
	}
}

// payloads returns the name and data of the events that carry data, and raws
// the bytes of all of them, of a scanAnyEOL result.
func payloads(events []scannedEvent) []scannedEvent {
	var out []scannedEvent
	for _, ev := range events {
		if ev.kind == "" {
			out = append(out, scannedEvent{name: ev.name, data: ev.data})
		}
	}
	return out
}

func raws(events []scannedEvent) string {
	var out strings.Builder
	for _, ev := range events {
		out.WriteString(ev.raw)
	}
	return out.String()
}

// An event is found when its blank line arrives, before any byte after it.
func TestEventScanner_AnyLineEndingFindsABareCREventAtOnce(t *testing.T) {
	scanner := EventScanner{AnyLineEnding: true}

	events := scanner.Feed([]byte("data: {\"a\":1}\r\r"))

	require.Len(t, events, 1)
	assert.JSONEq(t, `{"a":1}`, string(events[0].Data))
}

// An event over the limit is relayed in fragments and the scanner finds the
// next event wherever the pieces split the blank line, in every framing.
func TestEventScanner_AnyLineEndingResumesAfterAnOversizedEvent(t *testing.T) {
	for _, blank := range []string{"\n\n", "\r\n\r\n", "\r\r", "\n\r\n"} {
		stream := "data: " + strings.Repeat("a", 64) + blank + "data: next" + blank
		for i := 0; i <= len(stream); i++ {
			for j := i; j <= len(stream); j++ {
				got := scanAnyEOL(16, stream[:i], stream[i:j], stream[j:])

				require.Equal(t, stream, raws(got), "every byte is relayed once: %q | %q | %q", stream[:i], stream[i:j], stream[j:])
				require.Equal(t, []scannedEvent{{data: "next"}}, payloads(got), "%q | %q | %q", stream[:i], stream[i:j], stream[j:])
			}
		}
	}
}
