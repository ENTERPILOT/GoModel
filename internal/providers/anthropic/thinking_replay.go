package anthropic

import (
	"maps"
	"slices"

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

// thinkingReplayBlock encodes one response content block in the exact shape
// Anthropic requires back, the shape prependThinkingBlocks decodes. A thinking
// block always carries its text, which is empty when the model omitted it, so
// the field is never dropped. ok is false for blocks outside the thinking
// protocol.
func thinkingReplayBlock(block anthropicContent) (anthropicContentBlock, bool) {
	switch block.Type {
	case "thinking":
		thinking := block.Thinking
		return anthropicContentBlock{Type: block.Type, Thinking: &thinking, Signature: block.Signature}, true
	case "redacted_thinking":
		return anthropicContentBlock{Type: block.Type, Data: block.Data}, true
	default:
		return anthropicContentBlock{}, false
	}
}

// withThinkingReplay attaches the thinking blocks of content to a message's
// extra fields as extra_content.anthropic.thinking_blocks. Fields are returned
// unchanged when there is nothing to replay, so a response without thinking
// keeps the shape it has always had.
func withThinkingReplay(fields core.UnknownJSONFields, content []anthropicContent) core.UnknownJSONFields {
	var blocks []anthropicContentBlock
	for _, c := range content {
		if block, ok := thinkingReplayBlock(c); ok {
			blocks = append(blocks, block)
		}
	}
	if len(blocks) == 0 {
		return fields
	}
	vendor, err := json.Marshal(map[string][]anthropicContentBlock{core.ThinkingBlocksField: blocks})
	if err != nil {
		return fields
	}
	updated, err := fields.WithExtraContent(core.ExtraContentVendorAnthropic, vendor)
	if err != nil {
		return fields
	}
	return updated
}

// thinkingReplayState accumulates the thinking blocks of one streamed turn,
// keyed by upstream content-block index, so a stream hands the client the
// same replay state a buffered response carries. Both stream converters own
// one.
type thinkingReplayState struct {
	blocks map[int]*anthropicContent
}

func newThinkingReplayState() thinkingReplayState {
	return thinkingReplayState{blocks: make(map[int]*anthropicContent)}
}

// track records a thinking or redacted_thinking block as it starts and
// reports whether the block belongs to the thinking protocol at all. A
// redacted block arrives whole, so tracking it is all there is to do.
func (s *thinkingReplayState) track(index int, block *anthropicContent) bool {
	if block == nil || (block.Type != "thinking" && block.Type != "redacted_thinking") {
		return false
	}
	if _, seen := s.blocks[index]; !seen {
		tracked := *block
		s.blocks[index] = &tracked
	}
	return true
}

// tracked reports whether index is a block of the thinking protocol.
func (s *thinkingReplayState) tracked(index int) bool {
	_, ok := s.blocks[index]
	return ok
}

// isThinking reports whether index is a readable thinking block; a redacted
// one has no text to stream.
func (s *thinkingReplayState) isThinking(index int) bool {
	block := s.blocks[index]
	return block != nil && block.Type == "thinking"
}

func (s *thinkingReplayState) appendThinking(index int, text string) {
	if block := s.blocks[index]; block != nil {
		block.Thinking += text
	}
}

func (s *thinkingReplayState) appendSignature(index int, signature string) {
	if block := s.blocks[index]; block != nil {
		block.Signature += signature
	}
}

// extraContent renders every block tracked so far as an extra_content value,
// in upstream order. It is cumulative rather than incremental so a client
// that keeps only the most recent value still ends up with the whole turn.
// nil when nothing has been tracked.
func (s *thinkingReplayState) extraContent() json.RawMessage {
	content := make([]anthropicContent, 0, len(s.blocks))
	for _, index := range slices.Sorted(maps.Keys(s.blocks)) {
		content = append(content, *s.blocks[index])
	}
	return withThinkingReplay(core.UnknownJSONFields{}, content).Lookup(core.ExtraContentField)
}
