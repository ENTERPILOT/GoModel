package anthropicapi

import (
	"bytes"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// FromChatResponse renders a canonical chat response in the Anthropic Messages
// response shape.
func FromChatResponse(resp *core.ChatResponse) *MessagesResponse {
	out := &MessagesResponse{
		Type:    "message",
		Role:    "assistant",
		Content: []ResponseContentBlock{},
		Usage:   Usage{},
	}
	if resp == nil {
		return out
	}

	out.ID = normalizeMessageID(resp.ID)
	out.Model = resp.Model
	out.Usage = usageFromCore(resp.Usage)

	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		if thinking := reasoningContent(choice.Message.ExtraFields); thinking != "" {
			out.Content = append(out.Content, ResponseContentBlock{Type: "thinking", Thinking: thinking})
		}
		if text := core.ExtractTextContent(choice.Message.Content); text != "" {
			out.Content = append(out.Content, ResponseContentBlock{Type: "text", Text: text})
		}
		for _, call := range choice.Message.ToolCalls {
			out.Content = append(out.Content, ResponseContentBlock{
				Type:  "tool_use",
				ID:    call.ID,
				Name:  call.Function.Name,
				Input: argumentsToRaw(call.Function.Arguments),
			})
		}
		out.StopReason = stopReasonFromFinish(choice.FinishReason, len(choice.Message.ToolCalls) > 0)
		// Providers that report the matched stop sequence natively carry it as
		// a choice extension; surface it per the Anthropic contract. OpenAI's
		// finish_reason "stop" conflates natural and stop-parameter stops, so
		// OpenAI-family providers keep reporting "end_turn".
		if choice.StopSequence != "" && out.StopReason == "end_turn" {
			out.StopReason = "stop_sequence"
			sequence := choice.StopSequence
			out.StopSequence = &sequence
		}
	}
	if out.StopReason == "" {
		out.StopReason = "end_turn"
	}
	return out
}

func usageFromCore(usage core.Usage) Usage {
	out := Usage{
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
	}
	out.CacheCreationInputTokens = intFromRaw(usage.RawUsage["cache_creation_input_tokens"])
	out.CacheReadInputTokens = intFromRaw(usage.RawUsage["cache_read_input_tokens"])
	return out
}

// normalizeMessageID ensures the response carries an Anthropic-style msg_ id.
func normalizeMessageID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if strings.HasPrefix(id, "msg_") {
		return id
	}
	return "msg_" + id
}

// reasoningContent extracts the reasoning_content surfaced by providers (e.g.
// the anthropic provider) in a response message's extra fields.
func reasoningContent(fields core.UnknownJSONFields) string {
	raw := fields.Lookup("reasoning_content")
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return ""
	}
	return text
}

// argumentsToRaw renders a tool-call arguments string as a JSON object value.
func argumentsToRaw(arguments string) json.RawMessage {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return json.RawMessage("{}")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(trimmed)); err != nil {
		return json.RawMessage("{}")
	}
	return json.RawMessage(compact.Bytes())
}

// stopReasonFromFinish maps an OpenAI finish_reason to an Anthropic stop_reason.
//
// A response carrying tool calls always reports "tool_use" regardless of the
// upstream finish_reason: OpenAI-family providers report finish_reason "stop"
// alongside tool calls when a tool is forced via tool_choice, and the Anthropic
// Messages API contract guarantees "tool_use" whenever the content holds
// tool_use blocks.
func stopReasonFromFinish(finish string, hasToolCalls bool) string {
	if hasToolCalls {
		return "tool_use"
	}
	switch finish {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "end_turn"
	case "":
		return ""
	default:
		return finish
	}
}

// intFromRaw coerces a value decoded from a raw usage map into an int.
func intFromRaw(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0
		}
		return int(n)
	default:
		return 0
	}
}
