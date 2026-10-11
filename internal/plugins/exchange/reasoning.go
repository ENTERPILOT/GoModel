package exchange

import (
	"fmt"

	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
)

// chatReasoningMembers are the chat message members that carry reasoning
// text: the reasoning_content extension most providers use, then the
// vendor "reasoning" member (vLLM, Groq, OpenRouter). Some providers send
// both.
var chatReasoningMembers = []string{"reasoning_content", "reasoning"}

// chatReasoningText is one reasoning text of a chat message and the members
// carrying it: both when a provider sends the same text in each.
type chatReasoningText struct {
	text    string
	members []string
}

// chatReasoning lists the reasoning texts of a chat message in member
// order, one per distinct non-empty text.
func chatReasoning(fields core.UnknownJSONFields) []chatReasoningText {
	var out []chatReasoningText
	for _, member := range chatReasoningMembers {
		text := lookupString(fields, member)
		if text == "" {
			continue
		}
		if len(out) > 0 && out[0].text == text {
			out[0].members = append(out[0].members, member)
			continue
		}
		out = append(out, chatReasoningText{text: text, members: []string{member}})
	}
	return out
}

// reasoningParts prepends a reasoning part per reasoning text of the chat
// message to parts: the unified message lists reasoning first.
func reasoningParts(fields core.UnknownJSONFields, parts []pluginapi.Part) []pluginapi.Part {
	texts := chatReasoning(fields)
	if len(texts) == 0 {
		return parts
	}
	out := make([]pluginapi.Part, 0, len(texts)+len(parts))
	for _, r := range texts {
		out = append(out, pluginapi.Part{Kind: pluginapi.PartReasoning, Text: r.text})
	}
	return append(out, parts...)
}

// patchChatReasoning writes the reasoning parts of m back, in order, to the
// members they were read from when a plugin changed them. Other fields stay
// as they are.
func patchChatReasoning(fields core.UnknownJSONFields, m pluginapi.Message) (core.UnknownJSONFields, error) {
	texts := chatReasoning(fields)
	patch := map[string]json.RawMessage{}
	i := 0
	for _, part := range m.Parts {
		if part.Kind != pluginapi.PartReasoning || i >= len(texts) {
			continue
		}
		r := texts[i]
		i++
		if part.Text == r.text {
			continue
		}
		encoded, err := json.Marshal(part.Text)
		if err != nil {
			return core.UnknownJSONFields{}, err
		}
		for _, member := range r.members {
			patch[member] = encoded
		}
	}
	if len(patch) == 0 {
		return fields, nil
	}
	return core.MergeUnknownJSONFields(fields, patch)
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
