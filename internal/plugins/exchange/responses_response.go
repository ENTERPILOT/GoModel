package exchange

import (
	"fmt"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
)

// FromResponsesResponse builds the unified completion for a Responses
// response: one choice whose parts follow the output items in order.
// output_text content becomes text, refusal content a refusal part,
// function_call items tool calls, reasoning items reasoning parts (summary
// text), and everything else opaque parts. The finish reason is derived:
// "tool_calls" when a function call is present, else "stop" for a completed
// response, "length" for an incomplete one, otherwise the status.
func FromResponsesResponse(resp *core.ResponsesResponse) (*pluginapi.Completion, error) {
	if resp == nil {
		return nil, fmt.Errorf("exchange: nil responses response")
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("exchange: encode responses response: %w", err)
	}
	c := &pluginapi.Completion{ID: resp.ID, Model: resp.Model, Raw: raw, Usage: usageFromResponses(resp.Usage)}
	msg := pluginapi.Message{ID: choiceKey(0), Role: pluginapi.RoleAssistant}
	hasToolCall := false
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				switch content.Type {
				case "output_text":
					msg.Parts = append(msg.Parts, pluginapi.Part{Kind: pluginapi.PartText, Text: content.Text})
				case "refusal":
					msg.Parts = append(msg.Parts, pluginapi.Part{Kind: pluginapi.PartRefusal, Text: content.Text})
				default:
					msg.Parts = append(msg.Parts, opaquePart(content))
				}
			}
		case "function_call":
			hasToolCall = true
			msg.Parts = append(msg.Parts, pluginapi.Part{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{
				ID:        item.CallID,
				Name:      item.Name,
				Arguments: argumentsFromString(item.Arguments),
			}})
		case "reasoning":
			msg.Parts = append(msg.Parts, pluginapi.Part{Kind: pluginapi.PartReasoning, Text: reasoningSummary(item)})
		default:
			msg.Parts = append(msg.Parts, opaquePart(item))
		}
	}
	c.Choices = []pluginapi.Choice{{Index: 0, Message: msg, FinishReason: responsesFinishReason(resp.Status, hasToolCall)}}
	c.Reset()
	return c, nil
}

func reasoningSummary(item core.ResponsesOutputItem) string {
	raw := item.ExtraFields.Lookup("summary")
	if raw == nil {
		return ""
	}
	var summary []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		return ""
	}
	texts := make([]string, 0, len(summary))
	for _, s := range summary {
		if s.Text != "" {
			texts = append(texts, s.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func responsesFinishReason(status string, hasToolCall bool) string {
	switch {
	case hasToolCall:
		return "tool_calls"
	case status == "completed":
		return "stop"
	case status == "incomplete":
		return "length"
	default:
		return status
	}
}

func usageFromResponses(u *core.ResponsesUsage) pluginapi.Usage {
	if u == nil {
		return pluginapi.Usage{}
	}
	out := pluginapi.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, TotalTokens: u.TotalTokens}
	if u.PromptTokensDetails != nil {
		out.CachedInputTokens = u.PromptTokensDetails.CachedTokens
	}
	return out
}

// partLocation says which output item and content index a unified part came
// from; content is -1 for item-level parts.
type partLocation struct {
	item, content int
}

func responsesPartLocations(resp *core.ResponsesResponse) []partLocation {
	var locs []partLocation
	for i, item := range resp.Output {
		if item.Type == "message" {
			for j := range item.Content {
				locs = append(locs, partLocation{item: i, content: j})
			}
			continue
		}
		locs = append(locs, partLocation{item: i, content: -1})
	}
	return locs
}

// ApplyToResponsesResponse returns a copy of original with the completion's
// edits applied. Edited text parts are rewritten in their output_text
// items; a replaced choice keeps a single output_text in the first message
// item and drops the other text items. The response status and the finish
// reason are left unchanged: Responses has no finish_reason field, and the
// host renders a "content_filter" decision through headers instead.
func ApplyToResponsesResponse(original *core.ResponsesResponse, c *pluginapi.Completion) (*core.ResponsesResponse, error) {
	if original == nil || c == nil {
		return nil, fmt.Errorf("exchange: nil responses response or completion")
	}
	result := *original
	result.Output = cloneOutputItems(original.Output)
	kind, ok := c.Changes().Messages[choiceKey(0)]
	if !ok {
		return &result, nil
	}
	if len(c.Choices) == 0 {
		return nil, fmt.Errorf("exchange: completion has no choice to apply")
	}
	parts := c.Choices[0].Message.Parts
	switch kind {
	case pluginapi.ChangeReplaced:
		replaceResponsesText(&result, joinText(parts))
	default:
		locs := responsesPartLocations(original)
		if len(locs) != len(parts) {
			return nil, fmt.Errorf("exchange: choice parts changed structurally (%d parts, %d output entries); use ReplaceText", len(parts), len(locs))
		}
		for i, part := range parts {
			if part.Kind != pluginapi.PartText || locs[i].content < 0 {
				continue
			}
			result.Output[locs[i].item].Content[locs[i].content].Text = part.Text
		}
	}
	return &result, nil
}

func replaceResponsesText(resp *core.ResponsesResponse, text string) {
	replacement := core.ResponsesContentItem{Type: "output_text", Text: text}
	first := -1
	out := make([]core.ResponsesOutputItem, 0, len(resp.Output)+1)
	for _, item := range resp.Output {
		if item.Type != "message" {
			out = append(out, item)
			continue
		}
		if first < 0 {
			first = len(out)
			item.Content = []core.ResponsesContentItem{replacement}
			out = append(out, item)
			continue
		}
		kept := item.Content[:0:0]
		for _, content := range item.Content {
			if content.Type != "output_text" {
				kept = append(kept, content)
			}
		}
		if len(kept) == 0 {
			continue
		}
		item.Content = kept
		out = append(out, item)
	}
	if first < 0 {
		out = append(out, newResponsesMessageItem(replacement))
	}
	resp.Output = out
}

func newResponsesMessageItem(content ...core.ResponsesContentItem) core.ResponsesOutputItem {
	return core.ResponsesOutputItem{
		ID:      randomID("msg_"),
		Type:    "message",
		Role:    "assistant",
		Status:  "completed",
		Content: content,
	}
}

func cloneOutputItems(items []core.ResponsesOutputItem) []core.ResponsesOutputItem {
	if items == nil {
		return nil
	}
	out := make([]core.ResponsesOutputItem, len(items))
	for i, item := range items {
		item.Content = append([]core.ResponsesContentItem(nil), item.Content...)
		item.ExtraFields = core.CloneUnknownJSONFields(item.ExtraFields)
		out[i] = item
	}
	return out
}

// CompletionToResponsesResponse synthesizes a Responses response from a
// completion, for "respond" decisions on the Responses endpoint. Only the
// first choice is rendered.
func CompletionToResponsesResponse(c *pluginapi.Completion, model string) *core.ResponsesResponse {
	if c == nil {
		c = &pluginapi.Completion{}
	}
	if model == "" {
		model = c.Model
	}
	resp := &core.ResponsesResponse{
		ID:        randomID("gomodel-plugin-"),
		Object:    "response",
		CreatedAt: time.Now().Unix(),
		Model:     model,
		Status:    "completed",
		Output:    []core.ResponsesOutputItem{},
		Usage:     &core.ResponsesUsage{InputTokens: c.Usage.InputTokens, OutputTokens: c.Usage.OutputTokens, TotalTokens: c.Usage.TotalTokens},
	}
	var parts []pluginapi.Part
	if len(c.Choices) > 0 {
		parts = c.Choices[0].Message.Parts
	}
	var content []core.ResponsesContentItem
	for _, part := range parts {
		switch part.Kind {
		case pluginapi.PartText:
			content = append(content, core.ResponsesContentItem{Type: "output_text", Text: part.Text})
		case pluginapi.PartRefusal:
			content = append(content, core.ResponsesContentItem{Type: "refusal", Text: part.Text})
		case pluginapi.PartToolCall:
			if part.ToolCall == nil {
				continue
			}
			resp.Output = append(resp.Output, core.ResponsesOutputItem{
				ID:        randomID("fc_"),
				Type:      "function_call",
				Status:    "completed",
				CallID:    part.ToolCall.ID,
				Name:      part.ToolCall.Name,
				Arguments: argumentsToString(part.ToolCall.Arguments),
			})
		}
	}
	if len(content) == 0 && len(resp.Output) == 0 {
		content = []core.ResponsesContentItem{{Type: "output_text", Text: ""}}
	}
	if len(content) > 0 {
		resp.Output = append([]core.ResponsesOutputItem{newResponsesMessageItem(content...)}, resp.Output...)
	}
	return resp
}
