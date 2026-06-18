package providers

import (
	"bufio"
	"bytes"
	"io"

	"github.com/goccy/go-json"
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
	if firstNonSpaceByte(reader, peekForNonSSE) != '{' {
		// Genuine SSE (or empty): stream through unchanged, no buffering.
		return &bufferedReadCloser{Reader: reader, closer: stream}
	}

	// The '{' that classified this body is already buffered, so io.ReadAll
	// always returns at least that byte; a mid-read failure still yields the
	// partial bytes. Either way bufferedCompletionToSSE forwards what arrived
	// (raw when the JSON is truncated) and appends [DONE], so generated content
	// is never dropped and the client always receives a terminator.
	body, _ := io.ReadAll(reader)
	_ = stream.Close() //nolint:errcheck
	return io.NopCloser(bytes.NewReader(bufferedCompletionToSSE(body)))
}

// firstNonSpaceByte reports the first non-whitespace byte buffered by reader,
// peeking one byte further at a time so a genuine SSE stream is classified from
// its first token without blocking until a full buffer fills. It never consumes
// input, so a passed-through stream is forwarded byte-for-byte. Returns 0 when
// the stream ends, errors, or yields only whitespace within max bytes.
func firstNonSpaceByte(r *bufio.Reader, max int) byte {
	for i := 1; i <= max; i++ {
		prefix, err := r.Peek(i)
		if len(prefix) < i {
			_ = err // EOF or error before any non-space byte was found
			return 0
		}
		switch b := prefix[i-1]; b {
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

	var out bytes.Buffer
	out.WriteString("data: ")
	out.Write(payload)
	out.WriteString("\n\n")
	out.Write(chatDonePayload)
	return out.Bytes()
}

// bufferedReadCloser pairs a buffered reader with the underlying stream's Close.
type bufferedReadCloser struct {
	*bufio.Reader
	closer io.Closer
}

func (b *bufferedReadCloser) Close() error { return b.closer.Close() }
