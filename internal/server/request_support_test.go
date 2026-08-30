package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestClientRequestID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   string
		want string
	}{
		{name: "uuid", id: "0f8fad5b-d9cb-469f-a165-70867728950e", want: "0f8fad5b-d9cb-469f-a165-70867728950e"},
		{name: "trimmed", id: "  req-123  ", want: "req-123"},
		{name: "empty", id: "", want: ""},
		{name: "blank", id: "   ", want: ""},
		{name: "inner space", id: "req 123", want: ""},
		{name: "tab", id: "req\t123", want: ""},
		{name: "newline", id: "req-123\nINFO forged", want: ""},
		{name: "escape sequence", id: "\x1b[31mreq", want: ""},
		{name: "non-ascii", id: "réq-123", want: ""},
		{name: "max length", id: strings.Repeat("a", maxClientRequestIDLength), want: strings.Repeat("a", maxClientRequestIDLength)},
		{name: "too long", id: strings.Repeat("a", maxClientRequestIDLength+1), want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			header := http.Header{}
			header.Set(core.RequestIDHeader, tt.id)
			if got := clientRequestID(header); got != tt.want {
				t.Fatalf("clientRequestID(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}
