package streaming

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// NextEventBoundary and ParseDataLine are the package's SSE framing surface,
// shared with the response cache, so both forms of line ending and the
// optional space after "data:" are pinned here rather than only through the
// streams that happen to use them.
func TestNextEventBoundary(t *testing.T) {
	tests := []struct {
		name       string
		data       string
		wantIdx    int
		wantSepLen int
	}{
		{name: "no boundary yet", data: "data: {}", wantIdx: -1},
		{name: "lf", data: "data: {}\n\nrest", wantIdx: 8, wantSepLen: 2},
		{name: "crlf", data: "data: {}\r\n\r\nrest", wantIdx: 8, wantSepLen: 4},
		{name: "lf before crlf", data: "a\n\nb\r\n\r\n", wantIdx: 1, wantSepLen: 2},
		{name: "crlf before lf", data: "a\r\n\r\nb\n\n", wantIdx: 1, wantSepLen: 4},
		{name: "empty", data: "", wantIdx: -1},
		{name: "lf boundary at start", data: "\n\ndata: {}", wantIdx: 0, wantSepLen: 2},
		{name: "crlf boundary at start", data: "\r\n\r\ndata: {}", wantIdx: 0, wantSepLen: 4},
		{name: "crlf line then lf blank line", data: "data: {}\r\n\nrest", wantIdx: 9, wantSepLen: 2},
		{name: "lf line then crlf blank line", data: "data: {}\n\r\nrest", wantIdx: -1},
		{name: "crlf lines before lf boundary", data: "a\r\nb\r\nc\n\n", wantIdx: 7, wantSepLen: 2},
		{name: "lone cr is not a line end", data: "a\r\rb\r\n\r\n", wantIdx: 4, wantSepLen: 4},
		{name: "cr only blank line", data: "a\r\r\n", wantIdx: -1},
		{name: "trailing partial crlf boundary", data: "data: {}\r\n\r", wantIdx: -1},
		{name: "trailing single lf", data: "data: {}\n", wantIdx: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx, sepLen := NextEventBoundary([]byte(tt.data))
			assert.Equal(t, tt.wantIdx, idx)
			assert.Equal(t, tt.wantSepLen, sepLen)
		})
	}
}

func TestEventData(t *testing.T) {
	tests := []struct {
		name   string
		event  string
		want   string
		wantOK bool
	}{
		{name: "single data line", event: `data: {"a":1}`, want: `{"a":1}`, wantOK: true},
		{name: "event field before data", event: "event: response.created\ndata: {}", want: "{}", wantOK: true},
		{name: "comment lines are ignored", event: ": ping\ndata: {}\n: pong", want: "{}", wantOK: true},
		{name: "multiple data lines are joined", event: "data: {\"a\":\ndata:1}", want: "{\"a\":\n1}", wantOK: true},
		{name: "three data lines", event: "data: a\ndata: b\ndata: c", want: "a\nb\nc", wantOK: true},
		{name: "crlf lines", event: "event: x\r\ndata: a\r\ndata: b\r", want: "a\nb", wantOK: true},
		{name: "only the first space is dropped", event: "data:  a\ndata:b", want: " a\nb", wantOK: true},
		{name: "empty data line", event: "data:", want: "", wantOK: true},
		{name: "empty data lines still join", event: "data:\ndata:", want: "\n", wantOK: true},
		{name: "no data line", event: "event: done\n: comment", wantOK: false},
		{name: "empty event", event: "", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := []byte(tt.event)
			payload, ok := EventData(event)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, string(payload))
			assert.Equal(t, tt.event, string(event), "EventData must not modify the event")
		})
	}
}

func TestEventDataSingleLineAliasesEvent(t *testing.T) {
	event := []byte("event: x\ndata: {}\n")
	payload, ok := EventData(event)
	require.True(t, ok)
	require.Len(t, payload, 2)
	assert.Same(t, &event[len("event: x\ndata: ")], &payload[0])
}

func TestParseDataLine(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		want   string
		wantOK bool
	}{
		{name: "one space after the colon is dropped", line: "data: {}", want: "{}", wantOK: true},
		{name: "no space", line: "data:{}", want: "{}", wantOK: true},
		{name: "only the first space is dropped", line: "data:  x", want: " x", wantOK: true},
		{name: "trailing CR is trimmed", line: "data: x\r", want: "x", wantOK: true},
		{name: "another field", line: "event: done", wantOK: false},
		{name: "empty line", line: "", wantOK: false},
		{name: "payload may be empty", line: "data:", want: "", wantOK: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, ok := ParseDataLine([]byte(tt.line))
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.want, string(payload))
			}
		})
	}
}
