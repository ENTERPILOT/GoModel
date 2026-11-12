package server

import (
	"bytes"
	"io"
	"slices"
	"strings"

	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/streaming"
)

// chainedResponseStream names a chained response's predecessor on the
// streamed events that carry the response object (response.created,
// response.in_progress and the terminal event), as a buffered response does
// (see dispatchResponses). The provider never sees an id the gateway
// expanded into the input, so its stream cannot name it. Every other event
// passes through byte for byte, and so does a response object that already
// names a predecessor.
type chainedResponseStream struct {
	upstream io.ReadCloser
	id       string
	scanner  streaming.EventScanner
	buf      []byte
	out      []byte
	err      error
}

// withPreviousResponseID wraps stream so its response objects name id; an
// unchained request keeps its stream as it is.
func withPreviousResponseID(stream io.ReadCloser, id string) io.ReadCloser {
	if stream == nil || strings.TrimSpace(id) == "" {
		return stream
	}
	return &chainedResponseStream{
		upstream: stream,
		id:       id,
		scanner:  streaming.EventScanner{MaxEventBytes: streaming.MaxObservedEventBytes},
		buf:      make([]byte, 32*1024),
	}
}

func (s *chainedResponseStream) Read(p []byte) (int, error) {
	for len(s.out) == 0 {
		if s.err != nil {
			return 0, s.err
		}
		n, err := s.upstream.Read(s.buf)
		for _, ev := range s.scanner.Feed(s.buf[:n]) {
			s.out = s.appendEvent(s.out, ev)
		}
		if err != nil {
			for _, ev := range s.scanner.Flush() {
				s.out = s.appendEvent(s.out, ev)
			}
			s.err = err
		}
	}
	n := copy(p, s.out)
	s.out = s.out[n:]
	return n, nil
}

func (s *chainedResponseStream) Close() error {
	return s.upstream.Close()
}

// responseEventTypes are the events that carry the response object.
var responseEventTypes = []string{
	"response.created",
	"response.in_progress",
	"response.queued",
	"response.completed",
	"response.incomplete",
	"response.failed",
	"response.done",
}

// carriesResponse reads only the payload's type member, so text deltas are
// never decoded further.
func carriesResponse(data []byte) bool {
	return slices.Contains(responseEventTypes, gjson.GetBytes(data, "type").Str)
}

// appendEvent appends ev to out, naming the predecessor on the response
// object it carries.
func (s *chainedResponseStream) appendEvent(out []byte, ev streaming.RawEvent) []byte {
	if ev.Comment || ev.Oversized || !carriesResponse(ev.Data) {
		return append(out, ev.Raw...)
	}
	data, ok := s.nameResponse(ev.Data)
	if !ok {
		return append(out, ev.Raw...)
	}
	return appendWithData(out, ev.Raw, data)
}

// appendWithData appends the SSE block raw with its data lines replaced by
// data, one data line per line of it. Every other field (event, id, retry,
// comments) stays in place, so reconnection metadata survives the rewrite. A
// block framed with CRLF keeps CRLF, so a client that splits a stream on one
// kind of line ending still finds the block's end.
func appendWithData(out, raw, data []byte) []byte {
	eol := []byte("\n")
	if bytes.Contains(raw, []byte("\r\n")) {
		eol = []byte("\r\n")
	}
	written := false
	for line := range bytes.SplitSeq(bytes.TrimRight(raw, "\r\n"), []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if bytes.HasPrefix(line, []byte("data:")) {
			if !written {
				for part := range bytes.SplitSeq(data, []byte("\n")) {
					out = append(out, "data: "...)
					out = append(out, part...)
					out = append(out, eol...)
				}
				written = true
			}
			continue
		}
		out = append(out, line...)
		out = append(out, eol...)
	}
	return append(out, eol...)
}

// nameResponse returns the event payload with previous_response_id set on
// its response object; ok is false when the payload carries none or it
// already names a predecessor. The id is spliced into the payload's bytes
// rather than decoding and re-encoding it: a terminal event can carry
// megabytes of generated image, and the rest of the payload is left as the
// provider formatted it.
func (s *chainedResponseStream) nameResponse(data []byte) ([]byte, bool) {
	if !gjson.ValidBytes(data) {
		return nil, false
	}
	response := gjson.GetBytes(data, "response")
	if !response.IsObject() || response.Index == 0 {
		return nil, false
	}
	current := gjson.Get(response.Raw, "previous_response_id")
	if current.Type == gjson.String && current.Str != "" {
		return nil, false
	}
	id, err := json.Marshal(s.id)
	if err != nil {
		return nil, false
	}
	out := make([]byte, 0, len(data)+len(id)+len(`"previous_response_id":,`))
	if current.Exists() && current.Index > 0 {
		// A null or empty predecessor is replaced in place.
		start := response.Index + current.Index
		out = append(out, data[:start]...)
		out = append(out, id...)
		return append(out, data[start+len(current.Raw):]...), true
	}
	// Otherwise the member goes right after the object's opening brace.
	start := response.Index + 1
	out = append(out, data[:start]...)
	out = append(out, `"previous_response_id":`...)
	out = append(out, id...)
	if rest := strings.TrimLeft(response.Raw[1:], " \t\r\n"); rest != "" && rest[0] != '}' {
		out = append(out, ',')
	}
	return append(out, data[start:]...), true
}
