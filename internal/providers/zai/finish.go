package zai

import (
	"bufio"
	"bytes"
	"io"

	"github.com/goccy/go-json"
)

// finishReasons maps Z.ai's own finish_reason values onto the OpenAI
// vocabulary clients understand. "network_error" has no OpenAI counterpart and
// is left as is.
var finishReasons = map[string]string{
	"model_context_window_exceeded": "length",
	"sensitive":                     "content_filter",
}

// mapFinishReasons rewrites choices[].finish_reason in a chat completion
// payload (buffered or single SSE chunk). It returns the original data
// untouched when nothing applies, and reports whether a rewrite happened.
func mapFinishReasons(data []byte) ([]byte, bool) {
	// Cheap scan that keeps ordinary chunks off the decode path.
	if !mentionsFinishReason(data) {
		return data, false
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return data, false
	}
	var choices []map[string]json.RawMessage
	if err := json.Unmarshal(top["choices"], &choices); err != nil {
		return data, false
	}
	changed := false
	for _, choice := range choices {
		var reason string
		if err := json.Unmarshal(choice["finish_reason"], &reason); err != nil {
			continue
		}
		if mapped, ok := finishReasons[reason]; ok {
			choice["finish_reason"] = json.RawMessage(`"` + mapped + `"`)
			changed = true
		}
	}
	if !changed {
		return data, false
	}
	encodedChoices, err := json.Marshal(choices)
	if err != nil {
		return data, false
	}
	top["choices"] = encodedChoices
	out, err := json.Marshal(top)
	if err != nil {
		return data, false
	}
	return out, true
}

func mentionsFinishReason(data []byte) bool {
	for reason := range finishReasons {
		if bytes.Contains(data, []byte(reason)) {
			return true
		}
	}
	return false
}

func normalizeChatResponse(raw []byte) []byte {
	mapped, _ := mapFinishReasons(raw)
	return mapped
}

// finishReasonStream relays a chat completions SSE stream line by line,
// rewriting only the data lines that carry a Z.ai finish_reason.
type finishReasonStream struct {
	src     *bufio.Reader
	closer  io.Closer
	pending bytes.Buffer
	err     error
}

func newFinishReasonStream(stream io.ReadCloser) io.ReadCloser {
	return &finishReasonStream{src: bufio.NewReader(stream), closer: stream}
}

func (s *finishReasonStream) Read(p []byte) (int, error) {
	for s.pending.Len() == 0 {
		if s.err != nil {
			return 0, s.err
		}
		line, err := s.src.ReadBytes('\n')
		s.err = err
		if len(line) > 0 {
			s.pending.Write(mapFinishReasonLine(line))
		}
	}
	return s.pending.Read(p)
}

func (s *finishReasonStream) Close() error { return s.closer.Close() }

// mapFinishReasonLine rewrites one SSE line, keeping its line ending, and
// returns the input unchanged whenever the mapping does not apply.
func mapFinishReasonLine(line []byte) []byte {
	payload, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok {
		return line
	}
	body := bytes.TrimRight(payload, "\r\n")
	mapped, changed := mapFinishReasons(bytes.TrimLeft(body, " "))
	if !changed {
		return line
	}
	out := append([]byte("data: "), mapped...)
	return append(out, payload[len(body):]...)
}
