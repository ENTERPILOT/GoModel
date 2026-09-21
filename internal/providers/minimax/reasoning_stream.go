package minimax

import (
	"bufio"
	"bytes"
	"io"

	"github.com/goccy/go-json"
)

// sseDataPrefix introduces the JSON payload of an SSE event.
var sseDataPrefix = []byte("data: ")

// reasoningDetailsMarker gates the per-chunk decode: content deltas, the
// terminal usage chunk and [DONE] never carry it and are relayed with their
// original bytes, so only reasoning turns pay for a re-encode.
var reasoningDetailsMarker = []byte(`"reasoning_details"`)

// normalizeChatStream strips the redundant reasoning_details member from
// choices[].delta on a chat completions SSE stream. The canonical member
// reasoning_content is already incremental, and reasoning_details duplicates
// the same payload chunk for chunk, so dropping it halves the reasoning
// bandwidth. The stream ends with a standard usage-only chunk that passes
// through byte for byte. Streams of non-reasoning models are returned
// untouched.
func normalizeChatStream(stream io.ReadCloser, model string) io.ReadCloser {
	if stream == nil {
		return nil
	}
	if !isReasoningModel(model) {
		return stream
	}
	return &reasoningStream{src: bufio.NewReader(stream), closer: stream}
}

// reasoningStream relays MiniMax's SSE stream line by line, rewriting only
// the data lines that carry reasoning_details.
type reasoningStream struct {
	src     *bufio.Reader
	closer  io.Closer
	pending bytes.Buffer
	err     error
}

func (s *reasoningStream) Read(p []byte) (int, error) {
	for s.pending.Len() == 0 {
		if s.err != nil {
			return 0, s.err
		}
		line, err := s.src.ReadBytes('\n')
		s.err = err
		if len(line) > 0 {
			s.pending.Write(s.transform(line))
		}
	}
	return s.pending.Read(p)
}

func (s *reasoningStream) Close() error { return s.closer.Close() }

// transform rewrites one SSE line, returning the input unchanged whenever
// the rewrite does not apply or the payload does not parse.
func (s *reasoningStream) transform(line []byte) []byte {
	// SSE allows the field name with or without a space before the value;
	// both spellings must reach the rewrite or a `data:{...}` line would
	// leak raw reasoning_details to the client.
	payload, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok {
		return line
	}
	payload = bytes.TrimLeft(payload, " ")
	payload = bytes.TrimRight(payload, "\r\n")
	if !bytes.Contains(line, reasoningDetailsMarker) {
		return line
	}
	// Members are decoded as raw JSON and re-emitted byte for byte: a
	// map[string]any round trip would reformat every number it touches,
	// silently truncating integers beyond 2^53 in unrelated vendor data.
	var chunk map[string]json.RawMessage
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return line
	}
	rewritten, changed := stripDeltaReasoningDetails(chunk)
	if !changed {
		return line
	}
	encoded := marshalRaw(rewritten)
	out := make([]byte, 0, len(sseDataPrefix)+len(encoded)+1)
	out = append(out, sseDataPrefix...)
	out = append(out, encoded...)
	return append(out, '\n')
}

// stripDeltaReasoningDetails deletes reasoning_details from every choice's
// delta in place and reports whether anything changed. Every member it does
// not touch keeps its original raw bytes, and an unparsable envelope leaves
// the chunk to be relayed unchanged.
func stripDeltaReasoningDetails(chunk map[string]json.RawMessage) (map[string]json.RawMessage, bool) {
	var choices []json.RawMessage
	if err := json.Unmarshal(chunk["choices"], &choices); err != nil {
		return chunk, false
	}
	changed := false
	for i, raw := range choices {
		var choice map[string]json.RawMessage
		if err := json.Unmarshal(raw, &choice); err != nil {
			continue
		}
		var delta map[string]json.RawMessage
		if err := json.Unmarshal(choice["delta"], &delta); err != nil {
			continue
		}
		if _, has := delta[reasoningDetailsKey]; !has {
			continue
		}
		delete(delta, reasoningDetailsKey)
		choice["delta"] = marshalRaw(delta)
		choices[i] = marshalRaw(choice)
		changed = true
	}
	if !changed {
		return chunk, false
	}
	chunk["choices"] = marshalRaw(choices)
	return chunk, true
}

// marshalRaw re-encodes v, a value assembled only from strings and raw JSON
// members. json.Marshal on such a value cannot fail, so the error is dropped.
func marshalRaw(v any) json.RawMessage {
	encoded, _ := json.Marshal(v)
	return encoded
}
