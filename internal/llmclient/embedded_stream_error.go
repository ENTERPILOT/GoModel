package llmclient

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// streamPeekBytes bounds the leading bytes inspected to classify a stream
// body; 512 clears any leading whitespace without buffering real streams.
const streamPeekBytes = 512

// interceptEmbeddedStreamError checks a 200 stream response whose body is a
// buffered JSON object (application/json, leading '{') for a bare embedded
// error (see core.ParseEmbeddedProviderError). Genuine streams pass through
// after a bounded peek. Candidates are read only to the end of their first
// top-level object, capped at maxErrorBodyBytes, so inspection never waits
// for EOF on an open stream. An error-shaped object is trusted only when
// nothing but whitespace follows it before EOF; a trailing JSONL frame or
// stray bytes mark a stream. Inspected bytes that are not an error replay
// ahead of the live stream, and a read failure is replayed after them.
// Returns the error with resp.Body closed, or nil with resp.Body ready.
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
			var trailer []byte
			var atEOF bool
			trailer, atEOF, readErr = readTrailer(reader, maxErrorBodyBytes-len(head))
			if atEOF {
				_ = resp.Body.Close()
				return embedded
			}
			head = append(head, trailer...)
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

// readFirstJSONObject consumes r until the first top-level JSON object closes
// (complete=true), limit bytes are read, or r ends. Braces inside strings do
// not count. A clean EOF is not an error; the truncated bytes simply replay.
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

// readTrailer consumes whitespace from r until a non-whitespace byte, limit
// bytes, a read failure, or a clean EOF, which atEOF reports: the JSON object
// read before it was the entire body.
func readTrailer(r *bufio.Reader, limit int) (trailer []byte, atEOF bool, err error) {
	for len(trailer) < limit {
		b, readErr := r.ReadByte()
		if readErr == io.EOF {
			return trailer, true, nil
		}
		if readErr != nil {
			return trailer, false, readErr
		}
		trailer = append(trailer, b)
		switch b {
		case ' ', '\t', '\r', '\n':
		default:
			return trailer, false, nil
		}
	}
	return trailer, false, nil
}

// firstNonSpaceByte peeks, without consuming, for the first non-whitespace
// byte within max bytes; it returns 0 when none is found or r fails.
func firstNonSpaceByte(r *bufio.Reader, max int) byte {
	for i := 1; i <= max; i++ {
		prefix, _ := r.Peek(i)
		if len(prefix) < i {
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

// wrappedStreamBody serves a replacement reader while closing the original body.
type wrappedStreamBody struct {
	io.Reader
	closer io.Closer
}

func (b *wrappedStreamBody) Close() error { return b.closer.Close() }

// errorReader replays a read failure captured during inspection.
type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }
