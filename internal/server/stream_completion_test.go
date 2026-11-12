package server

import (
	"context"
	"fmt"
	"io"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/streaming"
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
			wantErr:   io.ErrUnexpectedEOF,
			wantEvent: "\ndata: {\"error\":{\"code\":\"stream_incomplete\",\"message\":\"provider stream ended before completion\",\"param\":null,\"type\":\"provider_error\"}}\n\n",
		},
		{
			name:      "malformed chunk then EOF",
			path:      "/v1/chat/completions",
			chunks:    []string{"data: {\"id\":\"c1\",\"choices\":[{\"delta\":{\"content\":\"hel\n\n"},
			readErr:   io.EOF,
			wantErr:   io.ErrUnexpectedEOF,
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
			wantErr:   io.ErrUnexpectedEOF,
			wantEvent: "\"code\":\"stream_incomplete\"",
		},
		{
			name:      "finish_reason null is not terminal",
			path:      "/v1/chat/completions",
			chunks:    []string{"data: {\"choices\":[{\"delta\":{\"content\":\"a\"},\"finish_reason\":null}]}\n\n"},
			readErr:   io.EOF,
			wantErr:   io.ErrUnexpectedEOF,
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
			require.ErrorIs(t, err, streaming.ErrStreamIncomplete)
			require.ErrorIs(t, err, tt.wantErr)
			body := strings.Join(tt.chunks, "")
			require.True(t, strings.HasPrefix(got, body), "upstream bytes must pass through unchanged")
			assert.Contains(t, got[len(body):], tt.wantEvent)
		})
	}
}

// terminalPayloadCases are SSE data payloads with the guard's verdict and
// whether the byte prefilter must already rule them out without parsing.
var terminalPayloadCases = []struct {
	name        string
	kind        streamKind
	payload     string
	terminal    bool
	prefiltered bool
}{
	{name: "chat content with null finish_reason", kind: chatStream, payload: `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`, prefiltered: true},
	{name: "chat content without finish_reason", kind: chatStream, payload: `{"choices":[{"delta":{"content":"hi"}}]}`, prefiltered: true},
	{name: "chat content naming finish_reason", kind: chatStream, payload: `{"choices":[{"delta":{"content":"set finish_reason to stop"},"finish_reason":null}]}`, prefiltered: true},
	{name: "chat content quoting a finish_reason member", kind: chatStream, payload: `{"choices":[{"delta":{"content":"{\"finish_reason\":\"stop\"}"},"finish_reason":null}]}`, prefiltered: true},
	{name: "chat tool arguments quoting an error member", kind: chatStream, payload: `{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"{\"error\":1}"}}]},"finish_reason":null}]}`, prefiltered: true},
	{name: "chat nested finish_reason member", kind: chatStream, payload: `{"choices":[{"delta":{"finish_reason":"stop"},"finish_reason":null}]}`},
	{name: "chat terminal chunk", kind: chatStream, payload: `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, terminal: true},
	{name: "chat terminal chunk with spaced member", kind: chatStream, payload: `{"choices":[{"delta":{},"finish_reason" : "length"}]}`, terminal: true},
	{name: "chat second choice finishes", kind: chatStream, payload: `{"choices":[{"index":0,"finish_reason":null},{"index":1,"finish_reason":"stop"}]}`, terminal: true},
	{name: "chat non-string finish_reason", kind: chatStream, payload: `{"choices":[{"finish_reason":1}]}`, terminal: true},
	{name: "chat empty finish_reason", kind: chatStream, payload: `{"choices":[{"finish_reason":""}]}`},
	{name: "chat in-band error", kind: chatStream, payload: `{"error":{"message":"boom","type":"server_error"}}`, terminal: true},
	{name: "chat null error member", kind: chatStream, payload: `{"error":null,"choices":[{"delta":{"content":"hi"}}]}`},
	{name: "chat nested error member", kind: chatStream, payload: `{"choices":[{"delta":{"content":"hi"},"logprobs":{"error":"x"}}]}`},
	{name: "chat escaped error key", kind: chatStream, payload: `{"\u0065rror":{"message":"boom"}}`, terminal: true},
	{name: "chat escaped finish_reason key", kind: chatStream, payload: `{"choices":[{"finish_\u0072eason":"stop"}]}`, terminal: true},
	{name: "chat invalid JSON", kind: chatStream, payload: `{"choices":[{"finish_reason":"stop"}]`},
	{name: "responses delta", kind: responsesStream, payload: `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"hi"}`, prefiltered: true},
	{name: "responses item done", kind: responsesStream, payload: `{"type":"response.output_item.done","item":{"type":"message"}}`, prefiltered: true},
	{name: "responses delta quoting response.completed", kind: responsesStream, payload: `{"type":"response.output_text.delta","delta":"\"response.completed\""}`, prefiltered: true},
	{name: "responses delta ending in a quoted type", kind: responsesStream, payload: `{"type":"response.output_text.delta","delta":"\"response.completed"}`},
	{name: "responses completed", kind: responsesStream, payload: `{"type":"response.completed","response":{"id":"resp_1","status":"completed"}}`, terminal: true},
	{name: "responses incomplete", kind: responsesStream, payload: `{"type":"response.incomplete","response":{}}`, terminal: true},
	{name: "responses failed", kind: responsesStream, payload: `{"type":"response.failed","response":{}}`, terminal: true},
	{name: "responses done", kind: responsesStream, payload: `{"type":"response.done"}`, terminal: true},
	{name: "responses error event", kind: responsesStream, payload: `{"type":"error","code":"server_error","message":"boom"}`, terminal: true},
	{name: "responses escaped type", kind: responsesStream, payload: `{"t\u0079pe":"response.\u0063ompleted"}`, terminal: true},
	{name: "responses finish_reason is not terminal", kind: responsesStream, payload: `{"type":"response.output_text.delta","finish_reason":"stop"}`, prefiltered: true},
}

func TestIsTerminalPayload(t *testing.T) {
	for _, tt := range terminalPayloadCases {
		t.Run(tt.name, func(t *testing.T) {
			payload := []byte(tt.payload)
			assert.Equal(t, tt.terminal, isTerminalPayload(tt.kind, payload))
			assert.Equal(t, tt.terminal, isTerminalObject(tt.kind, payload), "prefilter must not change the verdict")
			assert.Equal(t, tt.prefiltered, !mayBeTerminal(tt.kind, payload), "prefiltered")
		})
	}
}

// FuzzMayBeTerminal checks the prefilter never rules out a payload the full
// parse treats as terminal.
func FuzzMayBeTerminal(f *testing.F) {
	for _, tt := range terminalPayloadCases {
		f.Add(tt.kind == responsesStream, []byte(tt.payload))
	}
	f.Fuzz(func(t *testing.T, responses bool, payload []byte) {
		kind := chatStream
		if responses {
			kind = responsesStream
		}
		assert.True(t, mayBeTerminal(kind, payload) || !isTerminalObject(kind, payload), "prefilter hid a terminal payload: %q", payload)
	})
}

func TestGuardStreamCompletion_LargeEvents(t *testing.T) {
	large := strings.Repeat("a", maxGuardLineBytes)
	tests := []struct {
		name     string
		path     string
		body     string
		complete bool
	}{
		{
			name:     "chat final chunk with finish_reason",
			path:     "/v1/chat/completions",
			body:     "data: {\"choices\":[{\"delta\":{\"content\":\"" + large + "\"},\"finish_reason\":\"stop\"}]}\n\n",
			complete: true,
		},
		{
			name:     "responses completed carrying the whole response",
			path:     "/v1/responses",
			body:     "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"" + large + "\"}]}]}}\n\n",
			complete: true,
		},
		{
			name:     "in-band error with a long message",
			path:     "/v1/chat/completions",
			body:     "data: {\"error\":{\"message\":\"" + large + "\"}}\n\n",
			complete: true,
		},
		{
			name: "chat content delta without finish",
			path: "/v1/chat/completions",
			body: "data: {\"choices\":[{\"delta\":{\"content\":\"" + large + "\"},\"finish_reason\":null}]}\n\n",
		},
		{
			name: "chat content that quotes a finish_reason",
			path: "/v1/chat/completions",
			body: "data: {\"choices\":[{\"delta\":{\"content\":\"" + large + `\"finish_reason\":\"stop\"` + "\"}}]}\n\n",
		},
		{
			name: "responses delta whose text quotes response.completed",
			path: "/v1/responses",
			body: "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"" + `\"type\":\"response.completed\"` + large + "\"}\n\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readGuarded(t, tt.path, &chunkedStream{chunks: []string{tt.body}, err: io.EOF})
			require.True(t, strings.HasPrefix(got, tt.body), "upstream bytes must pass through unchanged")
			if tt.complete {
				require.NoError(t, err)
				assert.Equal(t, tt.body, got)
				return
			}
			require.ErrorIs(t, err, streaming.ErrStreamIncomplete)
			assert.Contains(t, got[len(tt.body):], "stream_incomplete")
		})
	}
}

func TestClassifyStreamError_ProviderStreamFailures(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want string
	}{
		{name: "provider reset", ctx: context.Background(), err: streaming.IncompleteStreamError(syscall.ECONNRESET), want: "stream_error"},
		{name: "provider closed early", ctx: context.Background(), err: streaming.IncompleteStreamError(io.EOF), want: "stream_error"},
		{name: "client write reset", ctx: context.Background(), err: syscall.ECONNRESET, want: "client_disconnected"},
		{name: "client went away mid provider read", ctx: canceled, err: streaming.IncompleteStreamError(context.Canceled), want: "client_disconnected"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, classifyStreamError(tt.ctx, tt.err))
		})
	}
}

func BenchmarkStreamCompletionGuard(b *testing.B) {
	var chat strings.Builder
	for i := range 100 {
		fmt.Fprintf(&chat, "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"token %d \"},\"finish_reason\":null}]}\n\n", i)
	}
	chat.WriteString("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")

	var responses strings.Builder
	responses.WriteString("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\"}}\n\n")
	for i := range 100 {
		fmt.Fprintf(&responses, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"token %d \"}\n\n", i)
	}
	responses.WriteString("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\"}}\n\n")

	cases := []struct {
		name string
		path string
		body string
	}{
		{name: "chat_100_chunks", path: "/v1/chat/completions", body: chat.String()},
		{name: "responses_100_deltas", path: "/v1/responses", body: responses.String()},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			buf := make([]byte, 32*1024)
			b.SetBytes(int64(len(tc.body)))
			b.ReportAllocs()
			for b.Loop() {
				guarded := guardStreamCompletion(tc.path, io.NopCloser(strings.NewReader(tc.body)))
				for {
					_, err := guarded.Read(buf)
					if err == io.EOF {
						break
					}
					if err != nil {
						b.Fatalf("read guarded stream: %v", err)
					}
				}
			}
		})
	}
}
