package llmclient

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// maxStreamStartBytes bounds how much of an SSE stream is held back while
// looking for its first data event. Keep-alive comments and event lines
// before the first payload are small; a stream that sends more than this
// without a data line streams through unchanged.
const maxStreamStartBytes = 64 * 1024

// errEmptyStream marks a 200 stream that ended before delivering any event.
var errEmptyStream = errors.New("provider returned an empty stream")

// interceptStreamStart holds back the start of a 200 stream until it proves
// usable, so a stream that is empty or opens with an in-band error fails
// before the gateway commits response headers and can still fail over. An
// empty body fails for any content type. For SSE, the first data event is
// read: an {"error": ...} payload (see core.ParseEmbeddedProviderError) fails
// with the status it carries. Everything held back replays ahead of the live
// stream otherwise. Returns the error with resp.Body closed, or nil with
// resp.Body ready.
func interceptStreamStart(provider string, resp *http.Response) *core.GatewayError {
	reader := bufio.NewReaderSize(resp.Body, streamPeekBytes)
	if _, err := reader.Peek(1); err != nil {
		_ = resp.Body.Close()
		if err == io.EOF {
			return core.NewProviderError(provider, http.StatusBadGateway, errEmptyStream.Error(), errEmptyStream)
		}
		return core.NewProviderError(provider, providerErrorStatusCode(err), readErrorMessage(err), err)
	}

	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		resp.Body = &wrappedStreamBody{Reader: reader, closer: resp.Body}
		return nil
	}

	head, payload, readErr := readFirstSSEData(reader, maxStreamStartBytes)
	switch {
	case payload != nil:
		if embedded := core.ParseEmbeddedProviderError(provider, payload); embedded != nil {
			_ = resp.Body.Close()
			return embedded
		}
	case readErr == nil && len(head) < maxStreamStartBytes:
		// The stream ended after only comments or blank lines.
		_ = resp.Body.Close()
		return core.NewProviderError(provider, http.StatusBadGateway, errEmptyStream.Error(), errEmptyStream)
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

// readFirstSSEData consumes whole lines from r until the first event that
// carries data is complete, limit bytes are read, or r ends. It returns every
// consumed byte and, when found, that event's joined data payload. A clean
// EOF is not an error.
func readFirstSSEData(r *bufio.Reader, limit int) (head, payload []byte, err error) {
	var data [][]byte
	for len(head) < limit {
		lineStart := len(head)
		line, readErr := r.ReadSlice('\n')
		for errors.Is(readErr, bufio.ErrBufferFull) && len(head)+len(line) < limit {
			head = append(head, line...)
			line, readErr = r.ReadSlice('\n')
		}
		head = append(head, line...)
		trimmed := bytes.TrimRight(head[lineStart:], "\r\n")
		if value, ok := bytes.CutPrefix(trimmed, []byte("data:")); ok {
			data = append(data, bytes.TrimPrefix(value, []byte(" ")))
		}

		if readErr != nil && !errors.Is(readErr, bufio.ErrBufferFull) {
			if readErr != io.EOF {
				return head, nil, readErr
			}
			// A final event may end at EOF without its blank line.
			if len(data) > 0 {
				return head, bytes.Join(data, []byte("\n")), nil
			}
			return head, nil, nil
		}
		if len(trimmed) == 0 && len(data) > 0 {
			return head, bytes.Join(data, []byte("\n")), nil
		}
	}
	return head, nil, nil
}
