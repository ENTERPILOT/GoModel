package auditlog

import (
	"maps"
	"sort"
	"strings"

	"github.com/goccy/go-json"
)

// anthropicStreamMessage rebuilds the message an Anthropic Messages stream
// spells out event by event. Such a stream reaches the observer wherever the
// gateway relays Anthropic's own events instead of chat chunks: /v1/messages
// forwarded natively, the /p/anthropic passthrough, and a cached Messages
// replay.
type anthropicStreamMessage struct {
	ID           string
	Model        string
	Role         string
	StopReason   string
	StopSequence string
	Usage        map[string]any
	Blocks       map[int]*anthropicStreamBlock
}

// anthropicStreamBlock is one content block: the block as it was opened, plus
// the members its deltas spell out.
type anthropicStreamBlock struct {
	Start     map[string]any
	Text      strings.Builder
	Thinking  strings.Builder
	Signature strings.Builder
	InputJSON strings.Builder
	Citations []any
}

// parseAnthropicEvent folds one Anthropic Messages stream event into the
// builder and reports whether the event was one. Chat chunks carry no "type"
// member, so they fall through to the chat parser.
func parseAnthropicEvent(builder *streamResponseBuilder, event map[string]any) bool {
	eventType, _ := event["type"].(string)
	switch eventType {
	case "message_start":
		msg := builder.anthropicMessage()
		message, _ := event["message"].(map[string]any)
		msg.ID, _ = message["id"].(string)
		msg.Model, _ = message["model"].(string)
		msg.Role, _ = message["role"].(string)
		if usage, ok := message["usage"].(map[string]any); ok {
			msg.Usage = maps.Clone(usage)
		}
	case "content_block_start":
		block := builder.anthropicBlock(event)
		if start, ok := event["content_block"].(map[string]any); ok && block.Start == nil {
			// Most opening blocks are a few bytes, but a server-tool result
			// arrives complete in this one event; past the budget only its
			// type is kept.
			if builder.fitsCapture(start) {
				block.Start = maps.Clone(start)
			} else {
				block.Start = map[string]any{"type": start["type"]}
			}
		}
	case "content_block_delta":
		block := builder.anthropicBlock(event)
		delta, _ := event["delta"].(map[string]any)
		switch delta["type"] {
		case "text_delta":
			text, _ := delta["text"].(string)
			appendLimitedStreamText(builder, &block.Text, text)
		case "thinking_delta":
			thinking, _ := delta["thinking"].(string)
			appendLimitedStreamText(builder, &block.Thinking, thinking)
		case "signature_delta":
			signature, _ := delta["signature"].(string)
			appendLimitedStreamText(builder, &block.Signature, signature)
		case "input_json_delta":
			partial, _ := delta["partial_json"].(string)
			appendLimitedStreamText(builder, &block.InputJSON, partial)
		case "citations_delta":
			if citation, ok := delta["citation"].(map[string]any); ok && builder.fitsCapture(citation) {
				block.Citations = append(block.Citations, citation)
			}
		}
	case "message_delta":
		msg := builder.anthropicMessage()
		if delta, ok := event["delta"].(map[string]any); ok {
			if reason, ok := delta["stop_reason"].(string); ok {
				msg.StopReason = reason
			}
			if sequence, ok := delta["stop_sequence"].(string); ok {
				msg.StopSequence = sequence
			}
		}
		// message_start reports the input side and message_delta the final
		// output count, so the later event extends the earlier one.
		if usage, ok := event["usage"].(map[string]any); ok {
			if msg.Usage == nil {
				msg.Usage = make(map[string]any, len(usage))
			}
			maps.Copy(msg.Usage, usage)
		}
	case "content_block_stop", "message_stop", "ping":
		// Nothing to keep, but still Anthropic's.
	default:
		return false
	}
	return true
}

func (b *streamResponseBuilder) anthropicMessage() *anthropicStreamMessage {
	if b.Anthropic == nil {
		b.Anthropic = &anthropicStreamMessage{Blocks: make(map[int]*anthropicStreamBlock)}
	}
	return b.Anthropic
}

func (b *streamResponseBuilder) anthropicBlock(event map[string]any) *anthropicStreamBlock {
	msg := b.anthropicMessage()
	index, ok := jsonNumberToInt(event["index"])
	if !ok {
		index = defaultStreamStateIndex(msg.Blocks)
	}
	block, ok := msg.Blocks[index]
	if !ok {
		block = &anthropicStreamBlock{}
		msg.Blocks[index] = block
	}
	return block
}

// fitsCapture charges a value that arrives whole against the capture budget
// and reports whether it fit. One that does not marks the body truncated.
func (b *streamResponseBuilder) fitsCapture(value any) bool {
	raw, err := json.Marshal(value)
	if err != nil || b.truncated || len(raw) > MaxContentCapture-b.contentLen {
		b.truncated = true
		return false
	}
	b.contentLen += len(raw)
	return true
}

// build returns the message as a non-streaming Messages call would have.
func (m *anthropicStreamMessage) build() map[string]any {
	indexes := make([]int, 0, len(m.Blocks))
	for index := range m.Blocks {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	content := make([]map[string]any, 0, len(indexes))
	for _, index := range indexes {
		content = append(content, m.Blocks[index].build())
	}

	message := map[string]any{
		"id":            m.ID,
		"type":          "message",
		"role":          nonEmptyString(m.Role, "assistant"),
		"model":         m.Model,
		"content":       content,
		"stop_reason":   nil,
		"stop_sequence": nil,
	}
	// A stream cut short never reports why it stopped; null says so.
	if m.StopReason != "" {
		message["stop_reason"] = m.StopReason
	}
	if m.StopSequence != "" {
		message["stop_sequence"] = m.StopSequence
	}
	if m.Usage != nil {
		message["usage"] = m.Usage
	}
	return message
}

func (b *anthropicStreamBlock) build() map[string]any {
	block := maps.Clone(b.Start)
	if block == nil {
		block = make(map[string]any, 2)
	}
	if b.Text.Len() > 0 {
		block["text"] = b.Text.String()
	}
	if b.Thinking.Len() > 0 {
		block["thinking"] = b.Thinking.String()
	}
	if b.Signature.Len() > 0 {
		block["signature"] = b.Signature.String()
	}
	if len(b.Citations) > 0 {
		block["citations"] = b.Citations
	}
	if b.InputJSON.Len() > 0 {
		block["input"] = decodeToolInput(b.InputJSON.String())
	}
	if _, ok := block["type"]; !ok {
		block["type"] = "text"
	}
	return block
}

// decodeToolInput parses the JSON text a tool input streamed as. Numbers stay
// exact, so a large ID is logged as the client received it. A cut or
// truncated stream leaves the text unparsable, and then the text is all there
// is to show.
func decodeToolInput(raw string) any {
	if !json.Valid([]byte(raw)) {
		return raw
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var input any
	if err := decoder.Decode(&input); err != nil {
		return raw
	}
	return input
}
