package telemetry

import (
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"

	"github.com/enterpilot/gomodel/internal/core"
)

const (
	// maxContentBytes caps each captured text part so one huge prompt or
	// completion cannot blow up a span. Longer text is cut at a rune
	// boundary and marked.
	maxContentBytes = core.GenerationContentLimit
	// maxAttributeContentBytes caps the text of one content attribute, so
	// many parts within their own limit cannot add up to an oversized span.
	maxAttributeContentBytes = 512 << 10
	truncatedMarker          = "… [truncated]"
)

// genAIMessage and genAIPart follow the OpenTelemetry GenAI message schema
// used by gen_ai.input.messages and gen_ai.output.messages. Media and file
// parts keep only their type: captured content never carries binary payloads
// or data URLs.
type genAIMessage struct {
	Role         string      `json:"role"`
	Parts        []genAIPart `json:"parts"`
	FinishReason string      `json:"finish_reason,omitempty"`
}

type genAIPart struct {
	Type      string `json:"type"`
	Content   string `json:"content,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments any    `json:"arguments,omitempty"`
	Response  string `json:"response,omitempty"`
}

// contentAttributes renders the outcome's request and response messages.
func contentAttributes(outcome core.GenerationOutcome) []attribute.KeyValue {
	var system []genAIPart
	var input, output []genAIMessage
	switch req := outcome.Request.(type) {
	case *core.ChatRequest:
		if req != nil {
			input = chatInputMessages(req.Messages)
		}
	case *core.ResponsesRequest:
		if req != nil {
			system = appendText(nil, req.Instructions)
			input = responsesInputMessages(req.Input)
		}
	}
	switch resp := outcome.Response.(type) {
	case *core.ChatResponse:
		if resp != nil {
			output = chatOutputMessages(resp.Choices)
		}
	case *core.ResponsesResponse:
		if resp != nil {
			output = responsesOutputMessages(resp)
		}
	}

	limitParts(system, maxAttributeContentBytes)
	limitMessages(input)
	limitMessages(output)

	var attrs []attribute.KeyValue
	attrs = appendJSONAttribute(attrs, "gen_ai.system_instructions", system)
	attrs = appendJSONAttribute(attrs, "gen_ai.input.messages", input)
	return appendJSONAttribute(attrs, "gen_ai.output.messages", output)
}

// limitMessages spends the attribute's text budget from the newest message
// backwards, so a long conversation keeps its latest turns intact and loses
// the oldest text first.
func limitMessages(messages []genAIMessage) {
	remaining := maxAttributeContentBytes
	for _, message := range slices.Backward(messages) {
		remaining = limitParts(message.Parts, remaining)
	}
}

// limitParts cuts the parts' text to the remaining budget and returns what
// is left of it.
func limitParts(parts []genAIPart, remaining int) int {
	for i := range parts {
		part := &parts[i]
		part.Content, remaining = spendText(part.Content, remaining)
		part.Response, remaining = spendText(part.Response, remaining)
		switch arguments := part.Arguments.(type) {
		case string:
			part.Arguments, remaining = spendText(arguments, remaining)
		case json.RawMessage:
			if len(arguments) > remaining {
				part.Arguments, remaining = truncatedMarker, 0
			} else {
				remaining -= len(arguments)
			}
		}
	}
	return remaining
}

func spendText(text string, remaining int) (string, int) {
	if len(text) <= remaining {
		return text, remaining - len(text)
	}
	return cutText(text, remaining), 0
}

func appendJSONAttribute[T any](attrs []attribute.KeyValue, key string, value []T) []attribute.KeyValue {
	if len(value) == 0 {
		return attrs
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return attrs
	}
	return append(attrs, attribute.String(key, string(encoded)))
}

func chatInputMessages(messages []core.Message) []genAIMessage {
	out := make([]genAIMessage, 0, len(messages))
	for _, message := range messages {
		if message.Role == "tool" {
			out = append(out, genAIMessage{Role: "tool", Parts: []genAIPart{{
				Type:     "tool_call_response",
				ID:       message.ToolCallID,
				Response: truncateContent(core.ExtractTextContent(message.Content)),
			}}})
			continue
		}
		parts := appendContentParts(nil, message.Content)
		parts = appendToolCalls(parts, message.ToolCalls)
		out = append(out, genAIMessage{Role: message.Role, Parts: parts})
	}
	return out
}

func chatOutputMessages(choices []core.Choice) []genAIMessage {
	out := make([]genAIMessage, 0, len(choices))
	for _, choice := range choices {
		role := choice.Message.Role
		if role == "" {
			role = "assistant"
		}
		parts := appendContentParts(nil, choice.Message.Content)
		parts = appendToolCalls(parts, choice.Message.ToolCalls)
		out = append(out, genAIMessage{Role: role, Parts: parts, FinishReason: choice.FinishReason})
	}
	return out
}

// responsesInputMessages accepts every input form a Responses request
// carries: a plain string, decoded input items, or generic JSON items built
// in code.
func responsesInputMessages(input any) []genAIMessage {
	switch typed := input.(type) {
	case string:
		if parts := appendText(nil, typed); len(parts) > 0 {
			return []genAIMessage{{Role: "user", Parts: parts}}
		}
		return nil
	case []core.ResponsesInputElement:
		return responsesItemMessages(typed)
	case []any:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return nil
		}
		var items []core.ResponsesInputElement
		if json.Unmarshal(encoded, &items) != nil {
			return nil
		}
		return responsesItemMessages(items)
	default:
		return nil
	}
}

func responsesItemMessages(items []core.ResponsesInputElement) []genAIMessage {
	out := make([]genAIMessage, 0, len(items))
	for _, item := range items {
		switch item.Type {
		case "", "message":
			role := item.Role
			if role == "" {
				role = "user"
			}
			out = append(out, genAIMessage{Role: role, Parts: appendContentParts(nil, item.Content)})
		case "function_call":
			out = append(out, genAIMessage{Role: "assistant", Parts: []genAIPart{toolCallPart(item.CallID, item.Name, item.Arguments)}})
		case "function_call_output":
			out = append(out, genAIMessage{Role: "tool", Parts: []genAIPart{{
				Type:     "tool_call_response",
				ID:       item.CallID,
				Response: truncateContent(item.Output),
			}}})
		}
	}
	return out
}

// responsesOutputMessages folds a response's output items into one
// assistant message, the Responses counterpart of a single chat choice.
func responsesOutputMessages(resp *core.ResponsesResponse) []genAIMessage {
	var parts []genAIPart
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				switch {
				case content.Text != "":
					parts = appendText(parts, content.Text)
				case content.Refusal != "":
					parts = appendText(parts, content.Refusal)
				}
			}
		case "function_call":
			parts = append(parts, toolCallPart(item.CallID, item.Name, item.Arguments))
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return []genAIMessage{{Role: "assistant", Parts: parts, FinishReason: responsesFinishReason(resp)}}
}

// appendContentParts converts chat or Responses message content: a string,
// typed content parts, or generic JSON parts.
func appendContentParts(parts []genAIPart, content any) []genAIPart {
	switch typed := content.(type) {
	case string:
		return appendText(parts, typed)
	case []core.ContentPart:
		for _, part := range typed {
			parts = appendTypedPart(parts, part.Type, part.Text)
		}
	case []any:
		for _, raw := range typed {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			partType, _ := part["type"].(string)
			text, _ := part["text"].(string)
			if text == "" {
				text, _ = part["refusal"].(string)
			}
			parts = appendTypedPart(parts, partType, text)
		}
	}
	return parts
}

// appendTypedPart keeps text and reduces any other part to its type.
func appendTypedPart(parts []genAIPart, partType, text string) []genAIPart {
	switch partType {
	case "text", "input_text", "output_text", "refusal":
		return appendText(parts, text)
	case "":
		return parts
	default:
		return append(parts, genAIPart{Type: partType})
	}
}

func appendText(parts []genAIPart, text string) []genAIPart {
	if text == "" {
		return parts
	}
	return append(parts, genAIPart{Type: "text", Content: truncateContent(text)})
}

func appendToolCalls(parts []genAIPart, calls []core.ToolCall) []genAIPart {
	for _, call := range calls {
		parts = append(parts, toolCallPart(call.ID, call.Function.Name, call.Function.Arguments))
	}
	return parts
}

// toolCallPart exports arguments as JSON when they parse, so backends render
// them as an object, and as text otherwise.
func toolCallPart(id, name, arguments string) genAIPart {
	part := genAIPart{Type: "tool_call", ID: id, Name: name}
	if arguments = strings.TrimSpace(arguments); arguments != "" {
		if len(arguments) <= maxContentBytes && json.Valid([]byte(arguments)) {
			part.Arguments = json.RawMessage(arguments)
		} else {
			part.Arguments = truncateContent(arguments)
		}
	}
	return part
}

func truncateContent(text string) string {
	return cutText(text, maxContentBytes)
}

// cutText keeps at most limit bytes of text, cut at a rune boundary and
// marked.
func cutText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := max(limit, 0)
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + truncatedMarker
}
