package exchange

import (
	"fmt"

	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
)

// chatReasoningMembers are the chat message members that carry reasoning
// text, in order of precedence: the reasoning_content extension most
// providers use, then the vendor "reasoning" member (vLLM, Groq,
// OpenRouter).
var chatReasoningMembers = []string{"reasoning_content", "reasoning"}

// chatReasoning returns the reasoning text of a chat message and the member
// carrying it, or two empty strings when it has none.
func chatReasoning(fields core.UnknownJSONFields) (text, member string) {
	for _, member := range chatReasoningMembers {
		if text := lookupString(fields, member); text != "" {
			return text, member
		}
	}
	return "", ""
}

// reasoningParts prepends a reasoning part to parts when the chat message
// carries reasoning: the unified message lists reasoning first.
func reasoningParts(fields core.UnknownJSONFields, parts []pluginapi.Part) []pluginapi.Part {
	text, _ := chatReasoning(fields)
	if text == "" {
		return parts
	}
	return append([]pluginapi.Part{{Kind: pluginapi.PartReasoning, Text: text}}, parts...)
}

// patchChatReasoning writes the reasoning part of m back to the member it
// was read from when a plugin changed it. Other fields stay as they are.
func patchChatReasoning(fields core.UnknownJSONFields, m pluginapi.Message) (core.UnknownJSONFields, error) {
	original, member := chatReasoning(fields)
	if member == "" {
		return fields, nil
	}
	for _, part := range m.Parts {
		if part.Kind != pluginapi.PartReasoning {
			continue
		}
		if part.Text == original {
			return fields, nil
		}
		encoded, err := json.Marshal(part.Text)
		if err != nil {
			return core.UnknownJSONFields{}, err
		}
		return core.MergeUnknownJSONFields(fields, map[string]json.RawMessage{member: encoded})
	}
	return fields, nil
}

// reasoningSegment is one piece of a Responses reasoning item's text: a
// reasoning_text content entry, or a summary entry when summary is set.
// index is its position in the item's content or summary.
type reasoningSegment struct {
	index   int
	summary bool
	text    string
}

// reasoningSegments lists the text of a reasoning item: its reasoning_text
// content, then its summary. An item with neither yields one empty segment
// with index -1, so every reasoning item maps to at least one part.
func reasoningSegments(item core.ResponsesOutputItem) []reasoningSegment {
	var out []reasoningSegment
	for j, content := range item.Content {
		if content.Type == "reasoning_text" {
			out = append(out, reasoningSegment{index: j, text: content.Text})
		}
	}
	for k, entry := range reasoningSummaryEntries(item) {
		var text string
		_ = json.Unmarshal(entry["text"], &text)
		out = append(out, reasoningSegment{index: k, summary: true, text: text})
	}
	if len(out) == 0 {
		out = append(out, reasoningSegment{index: -1})
	}
	return out
}

func reasoningSummaryEntries(item core.ResponsesOutputItem) []map[string]json.RawMessage {
	raw := item.ExtraFields.Lookup("summary")
	if raw == nil {
		return nil
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil
	}
	return entries
}

// setReasoningSegment writes text to the reasoning segment at loc of item.
// A summary entry gets its text spliced in place, so the summary keeps its
// encoding apart from that one value.
func setReasoningSegment(item *core.ResponsesOutputItem, loc partLocation, text string) error {
	if !loc.summary {
		item.Content[loc.content].Text = text
		return nil
	}
	raw := item.ExtraFields.Lookup("summary")
	current := gjson.GetBytes(raw, fmt.Sprintf("%d.text", loc.content))
	if current.Type != gjson.String || current.String() == text {
		return nil
	}
	if current.Index <= 0 {
		return fmt.Errorf("exchange: cannot locate text of reasoning summary %d", loc.content)
	}
	encoded, err := json.Marshal(text)
	if err != nil {
		return err
	}
	summary := make([]byte, 0, len(raw)+len(encoded))
	summary = append(summary, raw[:current.Index]...)
	summary = append(summary, encoded...)
	summary = append(summary, raw[current.Index+len(current.Raw):]...)
	fields, err := core.MergeUnknownJSONFields(item.ExtraFields, map[string]json.RawMessage{"summary": summary})
	if err != nil {
		return err
	}
	item.ExtraFields = fields
	return nil
}
