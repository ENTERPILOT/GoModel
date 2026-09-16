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
// looking for its first data event. Comments and blank lines beyond it carry
// no data and are dropped instead of held; a single line or an event preamble
// longer than it streams through unchanged.
const maxStreamStartBytes = 64 * 1024

// errEmptyStream marks a 200 stream that ended before delivering any event.
var errEmptyStream = errors.New("provider returned an empty stream")

// interceptStreamStart holds back the start of a 200 stream until it proves
// usable, so a stream that is empty or opens with an in-band error fails
// before the gateway commits response headers and can still fail over. An
// empty body fails for any content type. For SSE, the first data event is
// read: an {"error": ...} payload (see core.ParseEmbeddedProviderError) fails
// with the status it carries, and a stream that ends with no data event
// fails as empty. Held-back bytes replay ahead of the live stream otherwise.
// Returns the error with resp.Body closed, or nil with resp.Body ready.
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

	head, payload, ended, readErr := readFirstSSEData(reader, maxStreamStartBytes)
	switch {
	case payload != nil:
		if embedded := core.ParseEmbeddedProviderError(provider, payload); embedded != nil {
			_ = resp.Body.Close()
			return embedded
		}
	case ended:
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
// carries data is complete, and returns the bytes to replay with that event's
// joined data payload. ended reports a stream that closed cleanly before any
// data. While no data is pending, comment and blank lines beyond limit are
// dropped, so a long run of keep-alives is still checked in bounded memory.
// A single line, or an event preamble, longer than limit stops the inspection
// with nothing decided.
func readFirstSSEData(r *bufio.Reader, limit int) (head, payload []byte, ended bool, err error) {
	var data [][]byte
	fields := false // the pending event carries a field other than a comment
	for {
		lineStart := len(head)
		line, readErr := r.ReadSlice('\n')
		for errors.Is(readErr, bufio.ErrBufferFull) && len(head)+len(line) < limit {
			head = append(head, line...)
			line, readErr = r.ReadSlice('\n')
		}
		head = append(head, line...)
		if errors.Is(readErr, bufio.ErrBufferFull) {
			return head, nil, false, nil
		}

		trimmed := bytes.TrimRight(head[lineStart:], "\r\n")
		if value, ok := bytes.CutPrefix(trimmed, []byte("data:")); ok {
			data = append(data, bytes.TrimPrefix(value, []byte(" ")))
		}

		if readErr != nil {
			if readErr != io.EOF {
				return head, nil, false, readErr
			}
			// A final event may end at EOF without its blank line.
			if len(data) > 0 {
				return head, bytes.Join(data, []byte("\n")), false, nil
			}
			return head, nil, true, nil
		}
		if len(data) > 0 {
			if len(trimmed) == 0 {
				return head, bytes.Join(data, []byte("\n")), false, nil
			}
			continue
		}
		switch {
		case len(trimmed) == 0:
			// A blank line ends a data-less event, so nothing held back so
			// far belongs to the event that carries the first data.
			fields = false
		case trimmed[0] != ':':
			fields = true
		}
		if len(head) > limit {
			if fields {
				// Holding back a preamble this long would grow without
				// bound, and its fields cannot be dropped.
				return head, nil, false, nil
			}
			head = head[:0]
		}
	}
}
