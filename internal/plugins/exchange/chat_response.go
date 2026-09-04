package exchange

import (
	"fmt"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/pluginapi"
)

// FromChatResponse builds the unified completion for a chat response. Each
// choice's parts are, in order: reasoning_content, content text, refusal,
// tool calls. Choice messages get the ID "choice:<index>".
func FromChatResponse(resp *core.ChatResponse) (*pluginapi.Completion, error) {
	if resp == nil {
		return nil, fmt.Errorf("exchange: nil chat response")
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("exchange: encode chat response: %w", err)
	}
	c := &pluginapi.Completion{ID: resp.ID, Model: resp.Model, Raw: raw, Usage: usageFromChat(resp.Usage)}
	c.Choices = make([]pluginapi.Choice, 0, len(resp.Choices))
	for i, ch := range resp.Choices {
		parts, err := partsFromChatContent(ch.Message.Content)
		if err != nil {
			return nil, fmt.Errorf("exchange: choice %d: %w", i, err)
		}
		msg := pluginapi.Message{ID: choiceKey(i), Role: pluginapi.RoleAssistant}
		if reasoning := lookupString(ch.Message.ExtraFields, "reasoning_content"); reasoning != "" {
			msg.Parts = append(msg.Parts, pluginapi.Part{Kind: pluginapi.PartReasoning, Text: reasoning})
		}
		msg.Parts = append(msg.Parts, parts...)
		if refusal := lookupString(ch.Message.ExtraFields, "refusal"); refusal != "" {
			msg.Parts = append(msg.Parts, pluginapi.Part{Kind: pluginapi.PartRefusal, Text: refusal})
		}
		for _, tc := range ch.Message.ToolCalls {
			msg.Parts = append(msg.Parts, pluginapi.Part{Kind: pluginapi.PartToolCall, ToolCall: &pluginapi.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: argumentsFromString(tc.Function.Arguments),
			}})
		}
		c.Choices = append(c.Choices, pluginapi.Choice{Index: ch.Index, Message: msg, FinishReason: ch.FinishReason})
	}
	c.Reset()
	return c, nil
}

func usageFromChat(u core.Usage) pluginapi.Usage {
	out := pluginapi.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens, TotalTokens: u.TotalTokens}
	if u.PromptTokensDetails != nil {
		out.CachedInputTokens = u.PromptTokensDetails.CachedTokens
	}
	return out
}

// ApplyToChatResponse returns a copy of original with the completion's edits
// applied. Edited choices have their text rewritten in place and their
// finish reason updated; replaced choices get a plain string content. Extra
// fields (reasoning_content, refusal) are kept.
func ApplyToChatResponse(original *core.ChatResponse, c *pluginapi.Completion) (*core.ChatResponse, error) {
	if original == nil || c == nil {
		return nil, fmt.Errorf("exchange: nil chat response or completion")
	}
	result := *original
	result.Choices = make([]core.Choice, len(original.Choices))
	for i, ch := range original.Choices {
		ch.Message.Content = cloneContent(ch.Message.Content)
		ch.Message.ToolCalls = cloneToolCalls(ch.Message.ToolCalls)
		result.Choices[i] = ch
	}
	for key, kind := range c.Changes().Messages {
		idx, ok := choiceIndex(key)
		if !ok || idx >= len(c.Choices) || idx >= len(result.Choices) {
			return nil, fmt.Errorf("exchange: change %q does not match a response choice", key)
		}
		unified := c.Choices[idx]
		target := &result.Choices[idx]
		target.FinishReason = unified.FinishReason
		content, _ := splitParts(unified.Message)
		switch kind {
		case pluginapi.ChangeReplaced:
			target.Message.Content = joinText(content)
		case pluginapi.ChangeEdited:
			rewritten, _, err := rewriteChatContent(original.Choices[idx].Message.Content, false, textOnly(content))
			if err != nil {
				return nil, fmt.Errorf("exchange: choice %d: %w", idx, err)
			}
			target.Message.Content = rewritten
		}
	}
	return &result, nil
}

// textOnly keeps the parts that live in a chat response's content field:
// reasoning and refusal come from extra fields and must not be re-encoded.
func textOnly(parts []pluginapi.Part) []pluginapi.Part {
	out := make([]pluginapi.Part, 0, len(parts))
	for _, part := range parts {
		switch part.Kind {
		case pluginapi.PartReasoning, pluginapi.PartRefusal:
			continue
		}
		out = append(out, part)
	}
	return out
}

// CompletionToChatResponse synthesizes a chat response from a completion,
// for "respond" decisions on chat endpoints.
func CompletionToChatResponse(c *pluginapi.Completion, model string) *core.ChatResponse {
	if c == nil {
		c = &pluginapi.Completion{}
	}
	if model == "" {
		model = c.Model
	}
	resp := &core.ChatResponse{
		ID:      randomID("gomodel-plugin-"),
		Object:  "chat.completion",
		Model:   model,
		Created: time.Now().Unix(),
		Usage:   usageToChat(c.Usage),
	}
	choices := c.Choices
	if len(choices) == 0 {
		choices = []pluginapi.Choice{{Message: pluginapi.TextMessage(pluginapi.RoleAssistant, "")}}
	}
	for i, ch := range choices {
		content, calls := splitParts(ch.Message)
		msg := core.ResponseMessage{Role: "assistant", Content: joinText(content)}
		for _, call := range calls {
			msg.ToolCalls = append(msg.ToolCalls, newToolCall(call))
		}
		if len(textParts(content)) == 0 && len(calls) > 0 {
			msg.Content = nil
		}
		finish := ch.FinishReason
		if finish == "" {
			finish = "stop"
		}
		resp.Choices = append(resp.Choices, core.Choice{Index: i, Message: msg, FinishReason: finish})
	}
	return resp
}

func usageToChat(u pluginapi.Usage) core.Usage {
	out := core.Usage{PromptTokens: u.InputTokens, CompletionTokens: u.OutputTokens, TotalTokens: u.TotalTokens}
	if u.CachedInputTokens > 0 {
		out.PromptTokensDetails = &core.PromptTokensDetails{CachedTokens: u.CachedInputTokens}
	}
	return out
}
