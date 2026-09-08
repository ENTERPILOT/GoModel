package anthropic

import (
	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// Anthropic signs every thinking block it returns and refuses a later turn
// that replays one without its signature. The signature has no OpenAI-
// compatible field, and reasoning_content carries only the text, so the blocks
// travel back to the client as provider replay state under
// extra_content.anthropic.thinking_blocks — the same member the Messages
// ingress fills from a client's own thinking blocks and prependThinkingBlocks
// restores upstream. Every dialect therefore round-trips them without knowing
// what they mean.

// thinkingReplayBlock is a thinking or redacted_thinking block in the exact
// shape Anthropic requires back. A thinking block always carries its text,
// which is empty when the model omitted it, so the field is never dropped.
type thinkingReplayBlock struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature,omitempty"`
}

type redactedThinkingReplayBlock struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

// thinkingReplayJSON encodes one response content block for replay. ok is
// false for blocks that are not part of the thinking protocol.
func thinkingReplayJSON(block anthropicContent) (json.RawMessage, bool) {
	switch block.Type {
	case "thinking":
		raw, err := json.Marshal(thinkingReplayBlock{Type: block.Type, Thinking: block.Thinking, Signature: block.Signature})
		return raw, err == nil
	case "redacted_thinking":
		raw, err := json.Marshal(redactedThinkingReplayBlock{Type: block.Type, Data: block.Data})
		return raw, err == nil
	default:
		return nil, false
	}
}

// withThinkingReplay attaches thinking blocks to a message's extra fields as
// extra_content.anthropic.thinking_blocks. Fields are returned unchanged when
// there is nothing to replay, so a response without thinking keeps the shape
// it has always had.
func withThinkingReplay(fields core.UnknownJSONFields, blocks []json.RawMessage) core.UnknownJSONFields {
	if len(blocks) == 0 {
		return fields
	}
	raw, err := json.Marshal(map[string][]json.RawMessage{core.ThinkingBlocksField: blocks})
	if err != nil {
		return fields
	}
	updated, err := fields.WithExtraContent(core.ExtraContentVendorAnthropic, raw)
	if err != nil {
		return fields
	}
	return updated
}

// extractThinkingReplay collects the thinking blocks of a non-streamed
// response in order.
func extractThinkingReplay(blocks []anthropicContent) []json.RawMessage {
	var out []json.RawMessage
	for _, block := range blocks {
		if raw, ok := thinkingReplayJSON(block); ok {
			out = append(out, raw)
		}
	}
	return out
}
