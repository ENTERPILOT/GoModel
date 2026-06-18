package providers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
)

// chatDonePayload terminates a chat completions SSE stream.
var chatDonePayload = []byte("data: [DONE]\n\n")

// peekForNonSSE inspects up to this many leading bytes to classify the upstream
// response. SSE payloads begin with a field name (data:, event:, id:, retry:) or
// a ':' comment; a buffered JSON completion begins with '{'. 512 bytes comfortably
// clears any leading whitespace or comment lines without buffering real streams.
const peekForNonSSE = 512

// EnsureChatCompletionSSE normalizes a chat completions stream so the client
// always receives well-formed Server-Sent Events terminated by data: [DONE].
//
// Some OpenAI-compatible upstreams ignore stream:true and reply with a single
// buffered application/json completion (no data: framing, no [DONE]). Forwarding
// that verbatim under a text/event-stream content type leaves SSE clients waiting
// forever for an end-of-stream marker that never arrives. When the upstream body
// is detected as a buffered JSON object it is re-emitted as one SSE chunk plus a
// terminal [DONE]; genuine SSE streams pass through untouched with no buffering.
func EnsureChatCompletionSSE(stream io.ReadCloser) io.ReadCloser {
	if stream == nil {
		return nil
	}

	reader := bufio.NewReaderSize(stream, peekForNonSSE)
	prefix, _ := reader.Peek(peekForNonSSE)
	if firstNonSpace(prefix) != '{' {
		// Genuine SSE (or empty): stream through unchanged.
		return &bufferedReadCloser{Reader: reader, closer: stream}
	}

	body, err := io.ReadAll(reader)
	_ = stream.Close() //nolint:errcheck
	if err != nil {
		return io.NopCloser(bytes.NewReader(chatDonePayload))
	}
	return io.NopCloser(bytes.NewReader(bufferedCompletionToSSE(body)))
}

// firstNonSpace returns the first non-whitespace byte of data, or 0 if none.
func firstNonSpace(data []byte) byte {
	for _, b := range data {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return b
		}
	}
	return 0
}

// bufferedCompletionToSSE wraps a buffered chat completion JSON object as a
// single SSE chunk followed by the terminal [DONE] marker. The object field is
// rewritten to chat.completion.chunk and each choice's message is moved to delta
// so OpenAI SSE clients parse it as a streaming chunk. If the body does not parse
// as a JSON object it is forwarded as-is so no data is lost, still followed by
// [DONE] so the client stops waiting.
func bufferedCompletionToSSE(body []byte) []byte {
	payload := body
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err == nil {
		obj["object"] = "chat.completion.chunk"
		if choices, ok := obj["choices"].([]any); ok {
			for _, c := range choices {
				choice, ok := c.(map[string]any)
				if !ok {
					continue
				}
				if msg, ok := choice["message"]; ok {
					choice["delta"] = msg
					delete(choice, "message")
				}
			}
		}
		if encoded, err := json.Marshal(obj); err == nil {
			payload = encoded
		}
	}

	out := make([]byte, 0, len(payload)+len("data: \n\n")+len(chatDonePayload))
	out = append(out, "data: "...)
	out = append(out, payload...)
	out = append(out, '\n', '\n')
	out = append(out, chatDonePayload...)
	return out
}

// bufferedReadCloser pairs a buffered reader with the underlying stream's Close.
type bufferedReadCloser struct {
	*bufio.Reader
	closer io.Closer
}

func (b *bufferedReadCloser) Close() error { return b.closer.Close() }
