package server

import (
	"errors"
	"io"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// chunkedStream serves body one chunk per Read, then ends with err.
type chunkedStream struct {
	chunks []string
	err    error
}

func (s *chunkedStream) Read(p []byte) (int, error) {
	if len(s.chunks) == 0 {
		return 0, s.err
	}
	n := copy(p, s.chunks[0])
	s.chunks[0] = s.chunks[0][n:]
	if s.chunks[0] == "" {
		s.chunks = s.chunks[1:]
	}
	return n, nil
}

func (s *chunkedStream) Close() error { return nil }

func readGuarded(t *testing.T, path string, stream io.ReadCloser) (string, error) {
	t.Helper()
	guarded := guardStreamCompletion(path, stream)
	var out strings.Builder
	buf := make([]byte, 7)
	for {
		n, err := guarded.Read(buf)
		out.Write(buf[:n])
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			return out.String(), err
		}
	}
}

func TestGuardStreamCompletion_CompleteStreamsPassThrough(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "chat with DONE", path: "/v1/chat/completions", body: "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"},
		{name: "chat finish_reason without DONE", path: "/v1/chat/completions", body: "data: {\"choices\":[{\"delta\":{},\"finish_reason\": \"stop\"}]}\n\n"},
		{name: "chat usage chunk after finish", path: "/v1/chat/completions", body: "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"total_tokens\":3}}\n\n"},
		{name: "chat in-band error", path: "/v1/chat/completions", body: "data: {\"error\":{\"message\":\"boom\"}}\n\n"},
		{name: "chat DONE at EOF without newline", path: "/v1/chat/completions", body: "data: [DONE]"},
		{name: "responses completed", path: "/v1/responses", body: "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{}}\n\n"},
		{name: "responses incomplete", path: "/v1/responses", body: "event: response.incomplete\r\ndata: {\"type\":\"response.incomplete\"}\r\n\r\n"},
		{name: "messages stop", path: "/v1/messages", body: "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"},
		{name: "base path prefix", path: "/g/v1/chat/completions", body: "data: [DONE]\n\n"},
		{name: "unknown route is not guarded", path: "/v1/embeddings", body: "data: {\"x\":1}\n\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readGuarded(t, tt.path, &chunkedStream{chunks: []string{tt.body}, err: io.EOF})
			require.NoError(t, err)
			assert.Equal(t, tt.body, got)
		})
	}
}

func TestGuardStreamCompletion_TruncatedStreamsEndWithErrorEvent(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		chunks    []string
		readErr   error
		wantErr   error
		wantEvent string
	}{
		{
			name:      "chat EOF before DONE",
			path:      "/v1/chat/completions",
			chunks:    []string{"data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}]}\n\n"},
			readErr:   io.EOF,
			wantErr:   ErrStreamIncomplete,
			wantEvent: "\ndata: {\"error\":{\"code\":\"stream_incomplete\",\"message\":\"provider stream ended before completion\",\"param\":null,\"type\":\"provider_error\"}}\n\n",
		},
		{
			name:      "malformed chunk then EOF",
			path:      "/v1/chat/completions",
			chunks:    []string{"data: {\"id\":\"c1\",\"choices\":[{\"delta\":{\"content\":\"hel\n\n"},
			readErr:   io.EOF,
			wantErr:   ErrStreamIncomplete,
			wantEvent: "\"code\":\"stream_incomplete\"",
		},
		{
			name:      "partial tool call then connection reset",
			path:      "/v1/chat/completions",
			chunks:    []string{`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"ci`, `ty\":"}}]}}]}` + "\n\n"},
			readErr:   syscall.ECONNRESET,
			wantErr:   syscall.ECONNRESET,
			wantEvent: "\"code\":\"stream_incomplete\"",
		},
		{
			name:      "empty chat stream",
			path:      "/v1/chat/completions",
			readErr:   io.EOF,
			wantErr:   ErrStreamIncomplete,
			wantEvent: "\"code\":\"stream_incomplete\"",
		},
		{
			name:      "finish_reason null is not terminal",
			path:      "/v1/chat/completions",
			chunks:    []string{"data: {\"choices\":[{\"delta\":{\"content\":\"a\"},\"finish_reason\":null}]}\n\n"},
			readErr:   io.EOF,
			wantErr:   ErrStreamIncomplete,
			wantEvent: "\"code\":\"stream_incomplete\"",
		},
		{
			name:      "responses without terminal event",
			path:      "/v1/responses",
			chunks:    []string{"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"},
			readErr:   io.ErrUnexpectedEOF,
			wantErr:   io.ErrUnexpectedEOF,
			wantEvent: "\nevent: error\ndata: {\"code\":\"stream_incomplete\",\"message\":\"provider stream ended before completion\",\"param\":null,\"type\":\"error\"}\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readGuarded(t, tt.path, &chunkedStream{chunks: append([]string(nil), tt.chunks...), err: tt.readErr})
			require.Error(t, err)
			assert.True(t, errors.Is(err, tt.wantErr), "got %v, want %v", err, tt.wantErr)
			body := strings.Join(tt.chunks, "")
			require.True(t, strings.HasPrefix(got, body), "upstream bytes must pass through unchanged")
			assert.Contains(t, got[len(body):], tt.wantEvent)
		})
	}
}

func TestGuardStreamCompletion_LongContentLineIsNotTerminal(t *testing.T) {
	body := "data: {\"choices\":[{\"delta\":{\"content\":\"" + strings.Repeat("a", maxGuardLineBytes) + "\"},\"finish_reason\":\"stop\"}]}\n\n"
	got, err := readGuarded(t, "/v1/chat/completions", &chunkedStream{chunks: []string{body}, err: io.EOF})
	require.ErrorIs(t, err, ErrStreamIncomplete)
	assert.True(t, strings.HasPrefix(got, body))
}
