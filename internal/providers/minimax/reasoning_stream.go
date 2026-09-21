package minimax

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/goccy/go-json"
)

// sseDataPrefix introduces the JSON payload of an SSE event.
var sseDataPrefix = []byte("data: ")

// Markers gate the per-chunk decode: content deltas, role chunks and the
// terminal [DONE] line never carry them and are relayed with their original
// bytes, so only reasoning turns pay for a re-encode. The summary marker
// catches the trailing chat.completion event, whose full message announces
// the end of an M3 reasoning stream.
var (
	reasoningDetailsMarker = []byte(`"reasoning_details"`)
	summaryMarker          = []byte(`"message"`)
)

// normalizeChatStream rewrites a chat completions SSE stream from MiniMax's
// cumulative reasoning_details deltas onto canonical reasoning_content
// deltas, and collapses the trailing summary event to its usage. Streams of
// non-reasoning models are returned untouched.
func normalizeChatStream(stream io.ReadCloser, model string) io.ReadCloser {
	if stream == nil {
		return nil
	}
	if !isReasoningModel(model) {
		return stream
	}
	return &reasoningStream{
		src:        bufio.NewReader(stream),
		closer:     stream,
		cumulative: map[int]string{},
	}
}

// reasoningStream relays MiniMax's SSE stream line by line, tracking the
// reasoning text already emitted per choice so each delta forwards only the
// new suffix.
type reasoningStream struct {
	src        *bufio.Reader
	closer     io.Closer
	pending    bytes.Buffer
	cumulative map[int]string
	err        error
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
	// leak raw cumulative reasoning_details to the client.
	payload, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok {
		return line
	}
	payload = bytes.TrimLeft(payload, " ")
	payload = bytes.TrimRight(payload, "\r\n")
	if !bytes.Contains(line, reasoningDetailsMarker) && !bytes.Contains(line, summaryMarker) {
		return line
	}
	// Members are decoded as raw JSON and re-emitted byte for byte: a
	// map[string]any round trip would reformat every number it touches,
	// silently truncating integers beyond 2^53 in unrelated vendor data.
	var chunk map[string]json.RawMessage
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return line
	}
	rewritten, changed, err := s.rewriteChunk(chunk)
	if err != nil || !changed {
		return line
	}
	encoded := marshalRaw(rewritten)
	out := make([]byte, 0, len(sseDataPrefix)+len(encoded)+1)
	out = append(out, sseDataPrefix...)
	out = append(out, encoded...)
	return append(out, '\n')
}

// rewriteChunk rewrites chunk in place and reports whether anything changed,
// returning the chunk to encode. Every member it does not rewrite keeps its
// original raw bytes.
func (s *reasoningStream) rewriteChunk(chunk map[string]json.RawMessage) (map[string]json.RawMessage, bool, error) {
	var choices []json.RawMessage
	if err := json.Unmarshal(chunk["choices"], &choices); err != nil {
		return nil, false, err
	}
	changed := false
	for i, raw := range choices {
		var choice map[string]json.RawMessage
		if err := json.Unmarshal(raw, &choice); err != nil {
			return nil, false, err
		}
		if _, isMessage := choice["message"]; isMessage {
			// MiniMax M3 ends a reasoning stream with a full chat.completion
			// object — the complete message plus the real usage. Clients have
			// already received every delta, so the event collapses to an empty
			// choices array plus the usage and the duplicate message goes. The
			// rest of the envelope (id, object, created, model) survives, and
			// a finish_reason on the event is hoisted onto the emptied choice
			// so downstream consumers still see the terminal status.
			if _, hasUsage := chunk["usage"]; !hasUsage {
				return chunk, false, nil
			}
			// The emptied choice keeps its index and terminal status so
			// finish-bearing streams still record completion state. The
			// members are raw JSON, so the re-encode cannot fail.
			emptied := map[string]json.RawMessage{}
			if idx, has := choice["index"]; has {
				emptied["index"] = idx
			}
			if finish, has := choice["finish_reason"]; has {
				emptied["finish_reason"] = finish
			}
			chunk["choices"] = marshalRaw([]map[string]json.RawMessage{emptied})
			return chunk, true, nil
		}
		delta, hasDelta := choice["delta"]
		if !hasDelta || len(delta) == 0 {
			continue
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(delta, &fields); err != nil {
			return nil, false, err
		}
		if _, hasCanonical := fields[canonicalReasoningKey]; hasCanonical {
			continue
		}
		details, hasDetails := fields[reasoningDetailsKey]
		if !hasDetails {
			continue
		}
		suffix, err := s.reasoningSuffix(choiceIndex(choice, i), details)
		if err != nil {
			return nil, false, err
		}
		delete(fields, reasoningDetailsKey)
		if suffix != "" {
			fields[canonicalReasoningKey] = marshalRaw(suffix)
		}
		choice["delta"] = marshalRaw(fields)
		choices[i] = marshalRaw(choice)
		changed = true
	}
	if !changed {
		return nil, false, nil
	}
	chunk["choices"] = marshalRaw(choices)
	return chunk, true, nil
}

// reasoningSuffix returns the part of a choice's cumulative reasoning_details
// text that has not been emitted yet, and records the full text as emitted.
func (s *reasoningStream) reasoningSuffix(index int, raw json.RawMessage) (string, error) {
	full, ok := concatenateReasoningDetails(raw)
	if !ok {
		return "", fmt.Errorf("reasoning_details is not an array of objects")
	}
	previous := s.cumulative[index]
	suffix := ""
	switch {
	case full == previous:
	case strings.HasPrefix(full, previous):
		suffix = full[len(previous):]
	default:
		// The upstream buffer was replaced rather than extended: forward the
		// whole text so the client's reasoning stays consistent with it.
		suffix = full
	}
	s.cumulative[index] = full
	return suffix, nil
}

// choiceIndex returns the choice's index member, falling back to its position
// in the choices array when the member is absent or not a number.
func choiceIndex(choice map[string]json.RawMessage, position int) int {
	var index int
	if err := json.Unmarshal(choice["index"], &index); err != nil {
		return position
	}
	return index
}
