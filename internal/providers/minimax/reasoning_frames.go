package minimax

import (
	"bytes"

	"github.com/goccy/go-json"
)

// Synthetic SSE frame builders for the streaming <think> handling: the
// frames a thinkStream synthesizes around rewritten deltas — held parser
// carry flushed before a finish or [DONE], and the finish-only frame that
// carries a terminal finish_reason stripped from its content event.

// carryFrame flushes the carry of the parser for one choice into a complete
// synthetic SSE delta frame, or returns nil when that choice holds nothing.
// Each synthetic frame ends with the SSE delimiter (\n\n) so downstream
// frame decoders see a complete event.
func (s *thinkStream) carryFrame(index int) []byte {
	p, ok := s.parsers[index]
	if !ok {
		return nil
	}
	c, r := p.flush()
	if c == "" && r == "" {
		return nil
	}
	delta := map[string]json.RawMessage{}
	if c != "" {
		delta["content"] = json.RawMessage(mustMarshalString(c))
	}
	if r != "" {
		delta["reasoning_content"] = json.RawMessage(mustMarshalString(r))
	}
	// json.Marshal on an int cannot fail; the result is used directly.
	encodedIndex, _ := json.Marshal(index)
	return choiceFrame(map[string]json.RawMessage{
		"index": encodedIndex,
		"delta": mustMarshalRaw(delta),
	})
}

// choiceFrame wraps one choice object in a complete synthetic SSE delta
// frame ending with the SSE delimiter (\n\n) so downstream frame decoders
// see a complete event.
func choiceFrame(choice map[string]json.RawMessage) []byte {
	choices := []map[string]json.RawMessage{choice}
	encoded, _ := json.Marshal(map[string]json.RawMessage{"choices": mustMarshalJSON(choices)})
	out := make([]byte, 0, len(sseDataPrefix)+len(encoded)+2)
	out = append(out, sseDataPrefix...)
	out = append(out, encoded...)
	return append(out, '\n', '\n')
}

// finishSplit inspects a rewritten choice for a terminal finish_reason.
// When the rewrite parked new carry bytes, emitting the finish on the same
// event would strand the carry behind it, so the finish_reason is stripped
// from the content event and the returned frames carry, in order, the
// freshly parked carry and a finish-only frame — both emitted after the
// content event. A choice that is not finishing, or whose rewrite parked
// no carry, is returned unchanged with no frames.
func (s *thinkStream) finishSplit(raw json.RawMessage) (json.RawMessage, []byte) {
	var choice map[string]json.RawMessage
	if err := json.Unmarshal(raw, &choice); err != nil {
		return raw, nil
	}
	finish, ok := choice["finish_reason"]
	if !ok || bytes.Equal(bytes.TrimSpace(finish), []byte("null")) {
		return raw, nil
	}
	index := choiceIndex(choice)
	p := s.parsers[index]
	if p == nil || p.carry == "" {
		return raw, nil
	}
	delete(choice, "finish_reason")
	// Marshaling a decoded map of json.RawMessage cannot fail; the result
	// is used directly.
	out, _ := json.Marshal(choice)
	frames := append(s.carryFrame(index), finishFrame(index, finish)...)
	return out, frames
}

// finishFrame builds a synthetic finish-only SSE delta frame: an empty
// delta carrying the original finish_reason for the given choice index.
// Like carryFrame it ends with the SSE delimiter (\n\n) so downstream
// frame decoders see a complete event.
func finishFrame(index int, finish json.RawMessage) []byte {
	// json.Marshal on an int cannot fail; the result is used directly.
	encodedIndex, _ := json.Marshal(index)
	return choiceFrame(map[string]json.RawMessage{
		"index":         encodedIndex,
		"delta":         mustMarshalRaw(map[string]json.RawMessage{}),
		"finish_reason": finish,
	})
}

// finishCarryFrame returns a synthetic carry frame for a choice whose delta
// carries a terminal finish_reason, or nil when the choice is not finishing
// (finish_reason absent or null) or its parser holds no carry. Only the
// finishing choice's own parser is flushed — other choices keep their carry
// until their own finish or the end of the stream.
func (s *thinkStream) finishCarryFrame(raw json.RawMessage) []byte {
	var choice map[string]json.RawMessage
	if err := json.Unmarshal(raw, &choice); err != nil {
		return nil
	}
	finish, ok := choice["finish_reason"]
	if !ok || bytes.Equal(bytes.TrimSpace(finish), []byte("null")) {
		return nil
	}
	return s.carryFrame(choiceIndex(choice))
}

func mustMarshalString(s string) string {
	encoded, _ := json.Marshal(s)
	return string(encoded)
}

func mustMarshalRaw(v map[string]json.RawMessage) json.RawMessage {
	encoded, _ := json.Marshal(v)
	return encoded
}

func mustMarshalJSON(v any) json.RawMessage {
	encoded, _ := json.Marshal(v)
	return encoded
}
