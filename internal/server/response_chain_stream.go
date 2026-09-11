package server

import (
	"bytes"
	"io"
	"strings"

	"github.com/goccy/go-json"

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
	return &chainedResponseStream{upstream: stream, id: id, buf: make([]byte, 32*1024)}
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

var responseMemberKey = []byte(`"response":`)

// appendEvent appends ev to out, naming the predecessor on the response
// object it carries.
func (s *chainedResponseStream) appendEvent(out []byte, ev streaming.RawEvent) []byte {
	if ev.Comment || ev.Oversized || !bytes.Contains(ev.Data, responseMemberKey) {
		return append(out, ev.Raw...)
	}
	data, ok := s.nameResponse(ev.Data)
	if !ok {
		return append(out, ev.Raw...)
	}
	named := streaming.Event{Name: ev.Name, Data: data}
	return append(out, named.Encode()...)
}

// nameResponse returns the event payload with previous_response_id set on
// its response object; ok is false when the payload carries none or it
// already names a predecessor.
func (s *chainedResponseStream) nameResponse(data []byte) ([]byte, bool) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, false
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(payload["response"], &response); err != nil || response == nil {
		return nil, false
	}
	var current string
	if raw, ok := response["previous_response_id"]; ok && json.Unmarshal(raw, &current) == nil && current != "" {
		return nil, false
	}
	id, err := json.Marshal(s.id)
	if err != nil {
		return nil, false
	}
	response["previous_response_id"] = id
	encodedResponse, err := json.Marshal(response)
	if err != nil {
		return nil, false
	}
	payload["response"] = encodedResponse
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, false
	}
	return encoded, true
}
