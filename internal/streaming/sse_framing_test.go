package streaming

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx, sepLen := NextEventBoundary([]byte(tt.data))
			assert.Equal(t, tt.wantIdx, idx)
			assert.Equal(t, tt.wantSepLen, sepLen)
		})
	}
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
