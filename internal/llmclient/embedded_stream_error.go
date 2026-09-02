package llmclient

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// streamPeekBytes bounds how many leading bytes are inspected to classify a
// 200 stream response body. SSE begins with a field name or a ':' comment and
// a buffered JSON object begins with '{'; 512 bytes comfortably clears any
// leading whitespace without buffering real streams.
const streamPeekBytes = 512

// interceptEmbeddedStreamError inspects a 200 streaming response that
// presents as a buffered JSON object — Content-Type application/json with a
// leading '{' — for a bare embedded error payload (see
// core.ParseEmbeddedProviderError). Every genuine stream shape (SSE, NDJSON,
// chunked JSON arrays) fails one of the two guards and passes through with at
// most a bounded peek. Candidate bodies are read only to the close of their
// first top-level object, capped at maxErrorBodyBytes — a genuine error
// payload is exactly one small object, so inspection never waits for EOF on a
// stream that stays open, never buffers without bound, and a mid-object stall
// blocks no one because no usable data exists yet. Whatever was inspected and
// not classified as an error replays ahead of the live stream, and a failed
// read replays the bytes already taken and then surfaces the failure, so
// truncation is never presented as a clean stream. Returns the embedded error
// with resp.Body closed, or nil with resp.Body ready for streaming.
func interceptEmbeddedStreamError(provider string, resp *http.Response) *core.GatewayError {
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") {
		return nil
	}

	reader := bufio.NewReaderSize(resp.Body, streamPeekBytes)
	if firstNonSpaceByte(reader, streamPeekBytes) != '{' {
		resp.Body = &wrappedStreamBody{Reader: reader, closer: resp.Body}
		return nil
	}

	head, complete, readErr := readFirstJSONObject(reader, maxErrorBodyBytes)
	if complete {
		if embedded := core.ParseEmbeddedProviderError(provider, head); embedded != nil {
			_ = resp.Body.Close()
			return embedded
		}
	}

	tail := io.Reader(reader)
	if readErr != nil {
		tail = errorReader{err: readErr}
	}
	resp.Body = &wrappedStreamBody{
		Reader: io.MultiReader(bytes.NewReader(head), tail),
		closer: resp.Body,
	}
	return nil
}

// readFirstJSONObject consumes bytes from r until the first top-level JSON
// object closes, the size cap is reached, the source fails, or it ends.
// complete reports whether the object closed within the cap. The scan is
// string- and escape-aware so braces inside string values do not count. A
// clean EOF before the object closes is not a failure — the truncated bytes
// simply replay as-is — so err carries genuine read failures only.
func readFirstJSONObject(r *bufio.Reader, limit int) (head []byte, complete bool, err error) {
	head = make([]byte, 0, 512)
	depth := 0
	inString := false
	escaped := false
	for len(head) < limit {
		b, readErr := r.ReadByte()
		if readErr != nil {
			if readErr == io.EOF {
				readErr = nil
			}
			return head, false, readErr
		}
		head = append(head, b)
		switch {
		case escaped:
			escaped = false
		case inString:
			switch b {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
		case b == '"':
			inString = true
		case b == '{':
			depth++
		case b == '}':
			depth--
			if depth == 0 {
				return head, true, nil
			}
		}
	}
	return head, false, nil
}

// firstNonSpaceByte reports the first non-whitespace byte buffered by reader,
// peeking one byte further at a time so a genuine stream is classified from
// its first token without blocking until a full buffer fills. It never
// consumes input. Returns 0 when the stream ends, errors, or yields only
// whitespace within max bytes.
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

// wrappedStreamBody pairs a replacement reader with the original body's
// Close, so no inspected byte is lost and the upstream connection is still
// released.
type wrappedStreamBody struct {
	io.Reader
	closer io.Closer
}

func (b *wrappedStreamBody) Close() error { return b.closer.Close() }

// errorReader replays a read failure captured during inspection, so the
// caller sees the stream fail exactly where it did.
type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }
