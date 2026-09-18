package minimax

import (
	"bufio"
	"bytes"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/goccy/go-json"
)

// Streaming half of the MiniMax <think> handling: normalizeChatStream wraps
// the SSE stream and thinkParser runs the state machine across successive
// content deltas. The buffered half (response normalization and the
// close-marker helpers) lives in reasoning.go; the synthetic SSE frame
// builders (carry and finish frames) live in reasoning_frames.go.

// sseDataPrefix introduces the JSON payload of an SSE event.
var sseDataPrefix = []byte("data: ")

// doneEvent is the terminal SSE event of a chat completion stream.
var doneEvent = []byte("data: [DONE]")

// thinkMarkers lists every marker a split-across-feeds suffix could
// complete into: every accepted opening tag plus every accepted closing
// marker.
var thinkMarkers = slices.Concat(thinkOpenTags, thinkCloseTags)

// normalizeChatStream wraps a chat completion SSE stream, splitting every
// <think>...</think> block out of the delta.content member into a new
// delta.reasoning_content member. The <think> and </think> tag bytes never
// reach the client. Lines that are not data events, and data events whose
// delta has no content, pass through byte for byte, so non-thinking models
// and tool-only deltas pay nothing.
func normalizeChatStream(stream io.ReadCloser) io.ReadCloser {
	if stream == nil {
		return nil
	}
	return &thinkStream{src: bufio.NewReader(stream), closer: stream}
}

type thinkStream struct {
	src     *bufio.Reader
	closer  io.ReadCloser
	pending bytes.Buffer
	err     error
	parsers map[int]*thinkParser // one parser per choice index
}

// parserFor returns the thinkParser for the given choice index, creating it
// on first use. Each choice carries its own think state: with n>1 a think
// block in one choice must not reclassify another choice's content.
func (s *thinkStream) parserFor(index int) *thinkParser {
	if s.parsers == nil {
		s.parsers = map[int]*thinkParser{}
	}
	p, ok := s.parsers[index]
	if !ok {
		p = &thinkParser{}
		s.parsers[index] = p
	}
	return p
}

func (s *thinkStream) Read(p []byte) (int, error) {
	for s.pending.Len() == 0 {
		if s.err != nil {
			s.flushCarry()
			if s.pending.Len() == 0 {
				return 0, s.err
			}
			// flushCarry produced a final delta; serve it on this Read and
			// surface the EOF on the next one so the bytes are not dropped.
			break
		}
		line, err := s.src.ReadBytes('\n')
		s.err = err
		if len(line) > 0 {
			if isDoneEvent(line) {
				// Clients stop reading at [DONE]; flush any held carry
				// first so the tail of the stream is not lost.
				s.flushCarry()
			}
			s.pending.Write(s.rewrite(line))
		}
	}
	return s.pending.Read(p)
}

// isDoneEvent reports whether line is the terminal `data: [DONE]` SSE event.
func isDoneEvent(line []byte) bool {
	return bytes.Equal(bytes.TrimRight(line, "\r\n"), doneEvent)
}

// flushCarry emits one synthetic delta per choice whose parser was still
// holding carry bytes, in choice-index order. An unfinished think block is
// closed here so the partial chain of thought reaches the client.
func (s *thinkStream) flushCarry() {
	if len(s.parsers) == 0 {
		return
	}
	indices := make([]int, 0, len(s.parsers))
	for index := range s.parsers {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		s.pending.Write(s.carryFrame(index))
	}
}

func (s *thinkStream) Close() error { return s.closer.Close() }

// rewrite processes one SSE line, returning either the original bytes when
// no rewrite applies or a rewritten line whose delta.content has had any
// <think> text split off into a delta.reasoning_content member. When a
// choice in the line carries a terminal finish_reason, that choice's held
// parser carry must reach the client before the finish — clients treat the
// finish chunk as the end of the stream. A finish-only chunk keeps its held
// carry flushed as a synthetic delta frame prepended to the returned bytes.
// A chunk whose content AND finish_reason arrive together is split instead:
// the content event goes first with finish_reason stripped, then any carry
// the content just parked flushes as its own frame, and the finish_reason
// travels last on a finish-only frame — prepending the freshly parked carry
// would hand the client its deltas out of order.
func (s *thinkStream) rewrite(line []byte) []byte {
	if !bytes.HasPrefix(line, sseDataPrefix) ||
		(!bytes.Contains(line, []byte(`"content"`)) && !bytes.Contains(line, []byte(`"finish_reason"`))) {
		return line
	}
	payload := bytes.TrimRight(line[len(sseDataPrefix):], "\r\n")
	var chunk map[string]json.RawMessage
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return line
	}
	var choices []json.RawMessage
	if err := json.Unmarshal(chunk["choices"], &choices); err != nil {
		return line
	}
	var prefix, suffix []byte
	changed := false
	for i, raw := range choices {
		rewritten, ok := s.rewriteChoice(raw)
		if !ok {
			// No content was rewritten: a terminal choice can only be a
			// finish-only chunk here, so its held carry flushes ahead of
			// the finish as before.
			prefix = append(prefix, s.finishCarryFrame(raw)...)
			continue
		}
		rewritten, frames := s.finishSplit(rewritten)
		choices[i] = rewritten
		changed = true
		suffix = append(suffix, frames...)
	}
	if !changed {
		return append(prefix, line...)
	}
	// Marshaling a decoded []json.RawMessage slice or map cannot fail; both
	// results are used directly.
	encoded, _ := json.Marshal(choices)
	chunk["choices"] = encoded
	out, _ := json.Marshal(chunk)
	result := make([]byte, 0, len(prefix)+len(sseDataPrefix)+len(out)+1+len(suffix))
	result = append(result, prefix...)
	result = append(result, sseDataPrefix...)
	result = append(result, out...)
	result = append(result, '\n')
	return append(result, suffix...)
}

// choiceIndex reads the index of a decoded choice, falling back to 0 when
// the member is missing or non-numeric.
func choiceIndex(choice map[string]json.RawMessage) int {
	index := 0
	if rawIndex, ok := choice["index"]; ok {
		_ = json.Unmarshal(rawIndex, &index)
	}
	return index
}

func (s *thinkStream) rewriteChoice(raw json.RawMessage) (json.RawMessage, bool) {
	var choice map[string]json.RawMessage
	if err := json.Unmarshal(raw, &choice); err != nil {
		return raw, false
	}
	// Each choice feeds the parser keyed by its own index; a missing or
	// non-numeric index falls back to choice 0.
	index := choiceIndex(choice)
	delta, ok := choice["delta"]
	if !ok {
		return raw, false
	}
	var d map[string]json.RawMessage
	if err := json.Unmarshal(delta, &d); err != nil {
		return raw, false
	}
	contentRaw, has := d["content"]
	if !has || len(bytes.TrimSpace(contentRaw)) == 0 {
		return raw, false
	}
	var content string
	if err := json.Unmarshal(contentRaw, &content); err != nil {
		return raw, false
	}
	text, reasoning := s.parserFor(index).feed(content)
	if text == content && reasoning == "" {
		return raw, false
	}
	delete(d, "content")
	if text != "" {
		encoded, _ := json.Marshal(text)
		d["content"] = encoded
	}
	if reasoning != "" {
		// An upstream delta that already carries reasoning_content keeps it
		// verbatim — the parser strips the tags from content but never
		// overwrites an already-canonical reasoning member.
		if _, exists := d["reasoning_content"]; !exists {
			encoded, _ := json.Marshal(reasoning)
			d["reasoning_content"] = encoded
		}
	}
	// Marshaling decoded maps of json.RawMessage cannot fail; all three
	// results are used directly.
	encDelta, _ := json.Marshal(d)
	choice["delta"] = encDelta
	out, _ := json.Marshal(choice)
	return out, true
}

// thinkParser runs the <think> state machine across successive content
// deltas. Tags may be split across SSE lines, so the parser carries the
// last few bytes of the previous feed in carry and only emits content once
// they are confirmed as not the start of a tag.
type thinkParser struct {
	inThink bool
	carry   string // bytes held back from the end of the previous feed
}

// feed consumes one content delta and returns the text to emit on the
// content and reasoning_content members of the next outgoing delta. Carry
// across calls lets a <think> or closing tag that lands across an SSE line
// boundary still be recognised.
//
// Every marker toggles the state at once, so chained think blocks stream
// without delay and reasoning emits live. The accepted trade-off is that a
// literal </think> the model wrote as text inside its reasoning is treated
// as a real close: the rest of the trace lands in content. Waiting for
// confirmation would stall the stream, and a close can only be confirmed
// by input that may never come — the buffered splitThink handles that
// exact shape correctly via confirmed-close matching.
func (p *thinkParser) feed(text string) (content, reasoning string) {
	combined := p.carry + text
	p.carry = ""
	if !p.inThink {
		// Orphan close markers at the head of the combined buffer mean the
		// parser already exited an inner think while the outer one stayed
		// open. Escape them: otherwise the `<` at position 0 keeps the
		// parser waiting for a `<think>` open that never comes and the
		// carry grows forever without ever emitting.
		combined = escapeLeadingCloses(combined)
	}

	var cb, rb strings.Builder
	i := 0
	for i < len(combined) {
		// The marker the parser looks for depends on state: inside think it
		// wants a closing marker, outside it wants the opening marker.
		idx, tag := earliestOpen(combined[i:])
		if p.inThink {
			idx, tag = earliestClose(combined[i:])
		}
		if idx < 0 {
			// No complete marker in the remainder. Hold only a suffix that
			// could still complete into a marker: emitting a partial tag
			// would leak it to the client when the next feed completes it,
			// but a suffix that can never be a marker must emit at once
			// instead of sticking in carry until EOF.
			rest := combined[i:]
			hold := holdFrom(rest)
			if hold < len(rest) {
				p.carry = rest[hold:]
			}
			if hold > 0 {
				emit(p.inThink, &cb, &rb, rest[:hold])
			}
			return escapeOrphanCloses(cb.String()), rb.String()
		}
		emit(p.inThink, &cb, &rb, combined[i:i+idx])
		i += idx + len(tag)
		p.inThink = !p.inThink
	}
	return escapeOrphanCloses(cb.String()), rb.String()
}

// holdFrom returns the index in s at which a suffix begins that is a proper
// prefix of any recognized opening or closing marker — the only bytes worth
// holding for the next feed. It returns len(s) when no suffix qualifies, in
// which case the whole remainder emits immediately.
func holdFrom(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] != '<' {
			continue
		}
		suffix := s[i:]
		for _, m := range thinkMarkers {
			if len(suffix) < len(m) && strings.HasPrefix(m, suffix) {
				return i
			}
		}
	}
	return len(s)
}

// flush emits any carry bytes the parser was still holding when the stream
// ended. An unfinished think block is treated as closed — the carry
// becomes reasoning so the client sees the partial chain of thought —
// matching the posture of any other interrupted turn. Outside think mode
// the carry becomes content with orphan closes escaped.
func (p *thinkParser) flush() (content, reasoning string) {
	carry := p.carry
	p.carry = ""
	if carry == "" {
		return "", ""
	}
	if p.inThink {
		return "", carry
	}
	return escapeOrphanCloses(carry), ""
}

func emit(inThink bool, content, reasoning *strings.Builder, segment string) {
	if inThink {
		reasoning.WriteString(segment)
		return
	}
	content.WriteString(segment)
}
