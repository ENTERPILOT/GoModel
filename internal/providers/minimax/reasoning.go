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
const (
	thinkOpenTag  = "<think>"
	thinkCloseTag = "</think>"
	reasoningKey  = "reasoning_content"
)

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
	if existing := msg.ExtraFields.Lookup(reasoningKey); len(existing) > 0 {
		// Already canonical: keep the upstream value, just strip the tags.
		return
	}
	encoded, err := json.Marshal(reasoning)
	if err != nil {
		return
	}
	merged, err := core.MergeUnknownJSONFields(msg.ExtraFields, map[string]json.RawMessage{
		reasoningKey: encoded,
	})
	if err != nil {
		return
	}
	msg.ExtraFields = merged
}

// splitThink returns content with every <think>...</think> block removed and
// the inner text concatenated as the reasoning string. An unterminated block
// (no closing tag, typically finish_reason=length) drops its inner text so
// the parser never emits a half-open tag.
func splitThink(s string) (content, reasoning string) {
	var cb, rb strings.Builder
	rest := s
	for {
		open := strings.Index(rest, thinkOpenTag)
		if open < 0 {
			cb.WriteString(rest)
			return strings.TrimSpace(cb.String()), rb.String()
		}
		cb.WriteString(rest[:open])
		rest = rest[open+len(thinkOpenTag):]
		close := strings.Index(rest, thinkCloseTag)
		if close < 0 {
			// Unterminated: drop the partial block rather than emit a half tag.
			return strings.TrimSpace(cb.String()), rb.String()
		}
		rb.WriteString(rest[:close])
		rest = rest[close+len(thinkCloseTag):]
	}
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
			return 0, s.err
		}
		line, err := s.src.ReadBytes('\n')
		s.err = err
		if len(line) > 0 {
			s.pending.Write(s.rewrite(line))
		}
	}
	return s.pending.Read(p)
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
	encoded, err := json.Marshal(choices)
	if err != nil {
		return line
	}
	chunk["choices"] = encoded
	out, err := json.Marshal(chunk)
	if err != nil {
		return line
	}
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
		encoded, err := json.Marshal(text)
		if err != nil {
			return raw, false
		}
		d["content"] = encoded
	}
	if reasoning != "" {
		encoded, err := json.Marshal(reasoning)
		if err != nil {
			return raw, false
		}
		d["reasoning_content"] = encoded
	}
	encDelta, err := json.Marshal(d)
	if err != nil {
		return raw, false
	}
	choice["delta"] = encDelta
	out, err := json.Marshal(choice)
	if err != nil {
		return raw, false
	}
	return out, true
}

// thinkParser runs the <think> state machine across successive content
// deltas. Tags may be split across SSE lines, so the parser carries the
// last few bytes of the previous feed in carry and only emits content once
// they are confirmed as not the start of a tag.
type thinkParser struct {
	inThink bool
	carry   string // bytes held back at the end of the previous feed
}

// feed consumes one content delta and returns the text to emit on the
// content and reasoning_content members of the next outgoing delta. Carry
// across calls lets a <think> or </think> tag that lands across an SSE line
// boundary still be recognised.
func (p *thinkParser) feed(text string) (content, reasoning string) {
	combined := p.carry + text
	p.carry = ""

	var cb, rb strings.Builder
	i := 0
	for i < len(combined) {
		tag := thinkOpenTag
		if p.inThink {
			tag = thinkCloseTag
		}
		// Skip the search when the remainder is shorter than the tag:
		// strings.Index reports a zero-length match in that case and the
		// parser would otherwise consume those bytes as if a tag had landed.
		j := -1
		if len(combined)-i >= len(tag) {
			j = strings.Index(combined[i:], tag)
		}
		if j < 0 {
			// No complete tag in the remainder. Hold from the last '<' so
			// a tag split across this feed and the next is still recognised:
			// emitting those bytes now would leak a partial tag to the client
			// when the next feed happens to complete it.
			lastAngle := strings.LastIndex(combined[i:], "<")
			if lastAngle < 0 {
				emit(p.inThink, &cb, &rb, combined[i:])
				return cb.String(), rb.String()
			}
			boundary := i + lastAngle
			if boundary > i {
				emit(p.inThink, &cb, &rb, combined[i:boundary])
			}
			p.carry = combined[boundary:]
			return cb.String(), rb.String()
		}
		emit(p.inThink, &cb, &rb, combined[i:i+j])
		i += j + len(tag)
		p.inThink = !p.inThink
	}
	return cb.String(), rb.String()
}

func emit(inThink bool, content, reasoning *strings.Builder, segment string) {
	if inThink {
		reasoning.WriteString(segment)
		return
	}
	content.WriteString(segment)
}