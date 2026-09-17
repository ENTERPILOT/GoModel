package minimax

import (
	"bufio"
	"bytes"
	"io"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// MiniMax reasoning models (M3, M2.7) ship their chain of thought as inline
// <think>...</think> XML inside the answer content. Other MiniMax models do
// not emit that tag. GoModel's canonical shape on /v1/chat/completions is
// reasoning_content — the same field Anthropic, DeepSeek, Cohere, and vLLM
// emit and the Responses and Messages translation layers read — so the
// blocks are stripped from content and moved there. Relaying the inline XML
// makes a reasoning model look broken to any OpenAI-compatible client that
// reads reasoning_content (Kimi Code, Open WebUI, anything Anthropic-shaped).
//
// MiniMax occasionally closes the block with the namespaced </mm:think>
// variant instead of the plain </think>, so the parser accepts both spellings
// of the close marker and treats them identically.
const (
	thinkOpenTag = "<think>"
	reasoningKey = "reasoning_content"
)

// thinkCloseTags lists every closing marker the parser accepts. Order does
// not matter: both the buffered and the streaming parser take the earliest
// match in the text they are scanning.
var thinkCloseTags = []string{"</think>", "</mm:think>"}

// isReasoningModel reports whether model is a MiniMax family member that
// emits inline <think> blocks. The M3 line ships adaptive thinking and the
// M2 line (M2.7 and later) thinks by default; anything else does not, so the
// parse step stays inert.
func isReasoningModel(model string) bool {
	m := strings.ToLower(model)
	return strings.HasPrefix(m, "minimax-m3") || strings.HasPrefix(m, "minimax-m2")
}

// normalizeChatResponse strips <think>...</think> blocks from every choice's
// content and moves the inner text into the canonical reasoning_content
// member. A response with no <think> blocks is left alone, including any
// existing reasoning_content the upstream carried.
func normalizeChatResponse(resp *core.ChatResponse) {
	if resp == nil {
		return
	}
	for i := range resp.Choices {
		normalizeChoice(&resp.Choices[i])
	}
}

func normalizeChoice(choice *core.Choice) {
	msg := &choice.Message
	raw, ok := msg.Content.(string)
	if !ok || raw == "" {
		return
	}
	stripped, reasoning := splitThink(raw)
	if reasoning == "" && stripped == raw {
		return
	}
	msg.Content = stripped
	if reasoning == "" {
		return
	}
	// Upstream may already carry a reasoning_content (e.g. another layer
	// pre-parsed the think block). When it does, keep the upstream text
	// verbatim — the parser only strips the tags from content, never
	// overwrites an already-canonical reasoning member with what it
	// extracted from content.
	if existing := msg.ExtraFields.Lookup(reasoningKey); len(existing) > 0 {
		return
	}
	// json.Marshal on a string and MergeUnknownJSONFields on a pre-validated
	// base cannot fail; both results are therefore used directly.
	encoded, _ := json.Marshal(reasoning)
	merged, _ := core.MergeUnknownJSONFields(msg.ExtraFields, map[string]json.RawMessage{
		reasoningKey: encoded,
	})
	msg.ExtraFields = merged
}

// splitThink extracts the reasoning body of s.
//
// Confirmed-close matching: a closing marker inside a think block is a real
// close in two cases only — no closing marker comes after it (final close),
// or a <think> open comes after it before the next closing marker (a chained
// block follows). Every other closing marker is literal text the model wrote
// while reasoning about the tags themselves and stays in the reasoning
// output verbatim. This keeps chained think → answer → think → answer
// sequences intact without letting a stray marker close the block early.
//
// Text outside think blocks is content; orphan closing markers in content
// are dropped. An unterminated block (no closing marker at all, typically
// finish_reason=length) emits the partial inner text as reasoning so the
// client still sees the chain of thought the model produced before it was
// cut off.
func splitThink(s string) (content, reasoning string) {
	var cb, rb strings.Builder
	rest := s
	inThink := false
	for len(rest) > 0 {
		if inThink {
			ci, tag := earliestClose(rest)
			if ci < 0 {
				rb.WriteString(rest)
				break
			}
			if isRealClose(rest[ci+len(tag):]) {
				rb.WriteString(rest[:ci])
				rest = rest[ci+len(tag):]
				inThink = false
				continue
			}
			// Literal close inside reasoning: keep it verbatim.
			rb.WriteString(rest[:ci+len(tag)])
			rest = rest[ci+len(tag):]
			continue
		}
		open := strings.Index(rest, thinkOpenTag)
		ci, tag := earliestClose(rest)
		switch {
		case open < 0 && ci < 0:
			cb.WriteString(rest)
			rest = ""
		case ci >= 0 && (open < 0 || ci < open):
			// Orphan close in content: drop the marker.
			cb.WriteString(rest[:ci])
			rest = rest[ci+len(tag):]
		default:
			cb.WriteString(rest[:open])
			rest = rest[open+len(thinkOpenTag):]
			inThink = true
		}
	}
	return strings.TrimSpace(cb.String()), rb.String()
}

// isRealClose reports whether a closing marker ends the current think
// block. s is the text right after the marker. The marker is real when no
// closing marker follows it, or when a <think> open follows it before the
// next closing marker.
func isRealClose(s string) bool {
	ci, _ := earliestClose(s)
	if ci < 0 {
		return true
	}
	open := strings.Index(s, thinkOpenTag)
	return open >= 0 && open < ci
}

// earliestClose returns the index and text of the first accepted closing
// marker in s. index is -1 when s contains none.
func earliestClose(s string) (index int, tag string) {
	index, tag = -1, ""
	for _, t := range thinkCloseTags {
		if i := strings.Index(s, t); i >= 0 && (index < 0 || i < index) {
			index, tag = i, t
		}
	}
	return index, tag
}

// earliestMarker returns the index and text of the first opening or closing
// marker in s, whichever comes first. index is -1 when s contains none.
func earliestMarker(s string) (index int, tag string) {
	index, tag = earliestClose(s)
	if o := strings.Index(s, thinkOpenTag); o >= 0 && (index < 0 || o < index) {
		return o, thinkOpenTag
	}
	return index, tag
}

// stripAllCloses removes every accepted closing marker from s.
func stripAllCloses(s string) string {
	for _, tag := range thinkCloseTags {
		s = strings.ReplaceAll(s, tag, "")
	}
	return s
}

// stripLeadingCloses removes every accepted closing marker at the head of s.
func stripLeadingCloses(s string) string {
	stripped := true
	for stripped {
		stripped = false
		for _, tag := range thinkCloseTags {
			if strings.HasPrefix(s, tag) {
				s = s[len(tag):]
				stripped = true
			}
		}
	}
	return s
}

// sseDataPrefix introduces the JSON payload of an SSE event.
var sseDataPrefix = []byte("data: ")

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
	parser  thinkParser
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
			s.pending.Write(s.rewrite(line))
		}
	}
	return s.pending.Read(p)
}

// flushCarry emits any carry bytes the parser was still holding at end of
// stream. An unfinished think block is closed here so the partial chain
// of thought reaches the client.
func (s *thinkStream) flushCarry() {
	c, r := s.parser.flush()
	if c == "" && r == "" {
		return
	}
	delta := map[string]json.RawMessage{}
	if c != "" {
		delta["content"] = json.RawMessage(mustMarshalString(c))
	}
	if r != "" {
		delta["reasoning_content"] = json.RawMessage(mustMarshalString(r))
	}
	choices := []map[string]json.RawMessage{{
		"index": json.RawMessage(`0`),
		"delta": mustMarshalRaw(delta),
	}}
	encoded, _ := json.Marshal(map[string]json.RawMessage{"choices": mustMarshalJSON(choices)})
	out := make([]byte, 0, len(sseDataPrefix)+len(encoded)+1)
	out = append(out, sseDataPrefix...)
	out = append(out, encoded...)
	out = append(out, '\n')
	s.pending.Write(out)
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

func (s *thinkStream) Close() error { return s.closer.Close() }

// rewrite processes one SSE line, returning either the original bytes when
// no rewrite applies or a rewritten line whose delta.content has had any
// <think> text split off into a delta.reasoning_content member.
func (s *thinkStream) rewrite(line []byte) []byte {
	if !bytes.HasPrefix(line, sseDataPrefix) || !bytes.Contains(line, []byte(`"content"`)) {
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
	changed := false
	for i, raw := range choices {
		rewritten, ok := s.rewriteChoice(raw)
		if !ok {
			continue
		}
		choices[i] = rewritten
		changed = true
	}
	if !changed {
		return line
	}
	// Marshaling a decoded []json.RawMessage slice or map cannot fail; both
	// results are used directly.
	encoded, _ := json.Marshal(choices)
	chunk["choices"] = encoded
	out, _ := json.Marshal(chunk)
	result := make([]byte, 0, len(sseDataPrefix)+len(out)+1)
	result = append(result, sseDataPrefix...)
	result = append(result, out...)
	return append(result, '\n')
}

func (s *thinkStream) rewriteChoice(raw json.RawMessage) (json.RawMessage, bool) {
	var choice map[string]json.RawMessage
	if err := json.Unmarshal(raw, &choice); err != nil {
		return raw, false
	}
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
	text, reasoning := s.parser.feed(content)
	if text == content && reasoning == "" {
		return raw, false
	}
	delete(d, "content")
	if text != "" {
		encoded, _ := json.Marshal(text)
		d["content"] = encoded
	}
	if reasoning != "" {
		encoded, _ := json.Marshal(reasoning)
		d["reasoning_content"] = encoded
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

	// Hold mode. A <think> open seen inside an open think block is literal
	// text, and it proves the block contains marker text — the next closing
	// marker is then suspect and gets held back until one of three events
	// resolves it: a new open (the held close was real, a chained block
	// follows), another close (the held close was literal), or EOF (the
	// held close was the real final close).
	suspect bool            // nested open seen; the next close is suspect
	heldTag string          // the held closing marker, "" when none is held
	pending strings.Builder // text received after the held closing marker
}

// feed consumes one content delta and returns the text to emit on the
// content and reasoning_content members of the next outgoing delta. Carry
// across calls lets a <think> or closing tag that lands across an SSE line
// boundary still be recognised.
//
// Fast mode is the default: every marker toggles the state at once, so
// chained think blocks stream without delay and reasoning emits live. The
// accepted trade-off is that a literal </think> written inside reasoning
// closes the block early — zero hold beats rare leakage.
//
// Hold mode engages only after a nested <think> open. From then on the
// parser holds each closing marker (and the text after it) until the
// confirmed-close rule can decide it: a following <think> confirms the held
// close as real, a following close proves it literal (it is restored into
// reasoning verbatim), and end of stream confirms it as the final close.
func (p *thinkParser) feed(text string) (content, reasoning string) {
	combined := p.carry + text
	p.carry = ""
	if !p.inThink && p.heldTag == "" {
		// Orphan close markers at the head of the combined buffer mean the
		// parser already exited an inner think while the outer one stayed
		// open. Strip them: otherwise the `<` at position 0 keeps the parser
		// waiting for a `<think>` open that never comes and the carry grows
		// forever without ever emitting.
		combined = stripLeadingCloses(combined)
	}

	var cb, rb strings.Builder
	i := 0
	for i < len(combined) {
		// The marker the parser looks for depends on state: holding a
		// suspect close it watches for any marker, inside a think block it
		// watches for any marker (a nested open arms hold mode), and
		// outside a block it watches for the open.
		idx, tag := -1, ""
		switch {
		case p.heldTag != "" || p.inThink:
			idx, tag = earliestMarker(combined[i:])
		default:
			if j := strings.Index(combined[i:], thinkOpenTag); j >= 0 {
				idx, tag = j, thinkOpenTag
			}
		}
		if idx < 0 {
			// No complete marker in the remainder. Hold from the last '<'
			// so a tag split across this feed and the next is still
			// recognised: emitting those bytes now would leak a partial tag
			// to the client when the next feed happens to complete it.
			rest := combined[i:]
			lastAngle := strings.LastIndex(rest, "<")
			emitUntil := len(rest)
			if lastAngle >= 0 {
				emitUntil = lastAngle
				p.carry = rest[lastAngle:]
			}
			if emitUntil > 0 {
				if p.heldTag != "" {
					p.pending.WriteString(rest[:emitUntil])
				} else {
					emit(p.inThink, &cb, &rb, rest[:emitUntil])
				}
			}
			return stripOrphanCloses(cb.String()), rb.String()
		}
		segment := combined[i : i+idx]
		i += idx + len(tag)
		switch {
		case p.heldTag != "" && tag == thinkOpenTag:
			// A new block opened: the held close was real and the pending
			// text is content between the two blocks.
			p.pending.WriteString(segment)
			cb.WriteString(p.pending.String())
			p.pending.Reset()
			p.heldTag = ""
			p.suspect = false
		case p.heldTag != "":
			// Another close arrived first: the held close was literal text.
			// Restore it into reasoning verbatim and hold the new close.
			p.pending.WriteString(segment)
			rb.WriteString(p.heldTag)
			rb.WriteString(p.pending.String())
			p.pending.Reset()
			p.heldTag = tag
		case !p.inThink:
			cb.WriteString(segment)
			p.inThink = true
		case tag == thinkOpenTag:
			// Nested open inside a block: literal text, arms hold mode.
			rb.WriteString(segment)
			rb.WriteString(tag)
			p.suspect = true
		case p.suspect:
			// First close after a nested open: hold it until resolved.
			rb.WriteString(segment)
			p.heldTag = tag
		default:
			// Fast close: exit the block immediately.
			rb.WriteString(segment)
			p.inThink = false
		}
	}
	return stripOrphanCloses(cb.String()), rb.String()
}

// stripOrphanCloses removes close markers that leaked into the content
// stream because the parser exited on an inner think's close while the
// outer one was still open. They are formatting noise, never content.
func stripOrphanCloses(s string) string {
	if !containsAnyClose(s) {
		return s
	}
	return stripAllCloses(s)
}

// containsAnyClose reports whether s contains any accepted closing marker.
func containsAnyClose(s string) bool {
	for _, tag := range thinkCloseTags {
		if strings.Contains(s, tag) {
			return true
		}
	}
	return false
}

// flush emits everything the parser was still holding when the stream
// ended. A held suspect close is confirmed as the real final close: the
// pending text after it is content. An unfinished think block is treated
// as closed — the carry becomes reasoning so the client sees the partial
// chain of thought — matching the posture of any other interrupted turn.
// Outside think mode the carry becomes content with orphan closes stripped.
func (p *thinkParser) flush() (content, reasoning string) {
	carry := p.carry
	p.carry = ""
	if p.heldTag != "" {
		c := stripOrphanCloses(p.pending.String() + carry)
		p.pending.Reset()
		p.heldTag = ""
		return c, ""
	}
	if carry == "" {
		return "", ""
	}
	if p.inThink {
		return "", carry
	}
	return stripOrphanCloses(carry), ""
}

func emit(inThink bool, content, reasoning *strings.Builder, segment string) {
	if inThink {
		reasoning.WriteString(segment)
		return
	}
	content.WriteString(segment)
}