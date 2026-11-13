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

// scanAnyEOL feeds pieces to a scanner that knows every line ending and
// returns the events it found, each as "name|data|raw".
func scanAnyEOL(limit int, pieces ...string) []string {
	scanner := EventScanner{MaxEventBytes: limit, AnyLineEnding: true}
	var got []string
	keep := func(events []RawEvent) {
		for _, ev := range events {
			kind := ev.Name + "|" + string(ev.Data)
			switch {
			case ev.Oversized:
				kind = "oversized"
			case ev.Comment:
				kind = "comment"
			}
			got = append(got, kind+"|"+string(ev.Raw))
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
		want   []string
	}{
		{
			name:   "bare CR",
			stream: "event: x\rdata: {\"a\":1}\r\r: ping\r\rdata: [DONE]\r\r",
			want:   []string{"x|{\"a\":1}|event: x\rdata: {\"a\":1}\r\r", "comment|: ping\r\r", "|[DONE]|data: [DONE]\r\r"},
		},
		{
			name:   "LF then CRLF",
			stream: "data: 1\n\r\ndata: 2\n\r\n",
			want:   []string{"|1|data: 1\n\r\n", "|2|data: 2\n\r\n"},
		},
		{
			name:   "several data lines, each ending its own way",
			stream: "data: a\rdata: b\r\ndata: c\n\ndata: d",
			want:   []string{"|a\nb\nc|data: a\rdata: b\r\ndata: c\n\n", "|d|data: d"},
		},
		{
			name:   "CRLF",
			stream: "event: x\r\ndata: 1\r\n\r\n",
			want:   []string{"x|1|event: x\r\ndata: 1\r\n\r\n"},
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

// payloads returns the "name|data" of the events that carry data, and raws
// the bytes of all of them, of a scanAnyEOL result.
func payloads(events []string) []string {
	var out []string
	for _, ev := range events {
		if kind := ev[:strings.LastIndex(ev[:len(ev)-len(rawOf(ev))], "|")]; kind != "comment" && kind != "oversized" {
			out = append(out, kind)
		}
	}
	return out
}

func raws(events []string) string {
	var out strings.Builder
	for _, ev := range events {
		out.WriteString(rawOf(ev))
	}
	return out.String()
}

// rawOf returns the raw bytes of one scanAnyEOL result: what follows the
// second "|" of an event with data, the first of any other.
func rawOf(ev string) string {
	for _, kind := range []string{"comment|", "oversized|"} {
		if raw, ok := strings.CutPrefix(ev, kind); ok {
			return raw
		}
	}
	_, rest, _ := strings.Cut(ev, "|")
	_, raw, _ := strings.Cut(rest, "|")
	return raw
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
				require.Equal(t, []string{"|next"}, payloads(got), "%q | %q | %q", stream[:i], stream[i:j], stream[j:])
			}
		}
	}
}
