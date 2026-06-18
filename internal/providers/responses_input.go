package providers

import (
	"fmt"
	"strings"

	"github.com/goccy/go-json"

	"gomodel/internal/core"
)

// ConvertResponsesInputToMessages converts a Responses API input payload into Chat API messages.
func ConvertResponsesInputToMessages(input any) ([]core.Message, error) {
	switch in := input.(type) {
	case string:
		return []core.Message{{Role: "user", Content: in}}, nil
	case []map[string]any:
		items := make([]any, 0, len(in))
		for _, item := range in {
			items = append(items, item)
		}
		return convertResponsesInputItems(items)
	case []any:
		return convertResponsesInputItems(in)
	case []core.ResponsesInputElement:
		items := make([]any, 0, len(in))
		for _, item := range in {
			items = append(items, item)
		}
		return convertResponsesInputItems(items)
	case nil:
		return nil, core.NewInvalidRequestError("invalid responses input: unsupported type", nil)
	default:
		return nil, core.NewInvalidRequestError("invalid responses input: unsupported type", nil)
	}
}

func convertResponsesInputItems(items []any) ([]core.Message, error) {
	messages := make([]core.Message, 0, len(items))
	var pendingAssistant *core.Message

	flushPendingAssistant := func() {
		if pendingAssistant == nil {
			return
		}
		messages = append(messages, *pendingAssistant)
		pendingAssistant = nil
	}

	for i, item := range items {
		msg, itemType, err := convertResponsesInputItem(item, i)
		if err != nil {
			return nil, err
		}

		if msg.Role == "assistant" {
			if itemType == "message" {
				flushPendingAssistant()
			}
			if pendingAssistant == nil {
				assistant := cloneResponsesMessage(msg)
				pendingAssistant = &assistant
			} else if canMergeAssistantMessages(*pendingAssistant, msg) {
				mergeAssistantMessage(pendingAssistant, msg)
			} else {
				flushPendingAssistant()
				assistant := cloneResponsesMessage(msg)
				pendingAssistant = &assistant
			}
			continue
		}

		flushPendingAssistant()
		messages = append(messages, msg)
	}

	flushPendingAssistant()
	return messages, nil
}

func convertResponsesInputItem(item any, index int) (core.Message, string, error) {
	switch typed := item.(type) {
	case core.ResponsesInputElement:
		return convertResponsesInputElement(typed, index)
	case map[string]any:
		return convertResponsesInputMap(typed, index)
	default:
		return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: expected object", index), nil)
	}
}

func convertResponsesInputElement(item core.ResponsesInputElement, index int) (core.Message, string, error) {
	switch item.Type {
	case "function_call":
		name := strings.TrimSpace(item.Name)
		if name == "" {
			return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: function_call name is required", index), nil)
		}
		callID := ResponsesFunctionCallCallID(item.CallID)
		return core.Message{
			Role:        "assistant",
			Content:     "",
			ContentNull: true,
			ToolCalls: []core.ToolCall{
				{
					ID:          callID,
					Type:        "function",
					ExtraFields: core.CloneUnknownJSONFields(item.ExtraFields),
					Function: core.FunctionCall{
						Name:      name,
						Arguments: item.Arguments,
					},
				},
			},
		}, "function_call", nil
	case "function_call_output":
		callID := strings.TrimSpace(item.CallID)
		if callID == "" {
			return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: function_call_output call_id is required", index), nil)
		}
		content, err := stringifyResponsesInputValueWithError(item.Output)
		if err != nil {
			return core.Message{}, "", core.NewInvalidRequestError(
				fmt.Sprintf("invalid responses input item at index %d: function_call_output.output must be JSON-serializable", index),
				err,
			)
		}
		return core.Message{
			Role:        "tool",
			ToolCallID:  callID,
			Content:     content,
			ExtraFields: core.CloneUnknownJSONFields(item.ExtraFields),
		}, "function_call_output", nil
	case "", "message":
		role := strings.TrimSpace(item.Role)
		if role == "" {
			return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: role is required", index), nil)
		}
		content, ok := ConvertResponsesContentToChatContent(item.Content)
		if !ok {
			return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: unsupported content", index), nil)
		}
		return core.Message{
			Role:        role,
			Content:     content,
			ExtraFields: core.CloneUnknownJSONFields(item.ExtraFields),
		}, "message", nil
	default:
		return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: unsupported input item type %q for chat-translated providers", index, item.Type), nil)
	}
}

func convertResponsesInputMap(item map[string]any, index int) (core.Message, string, error) {
	itemType, _ := item["type"].(string)
	switch itemType {
	case "function_call":
		name, _ := item["name"].(string)
		callID := firstNonEmptyString(item, "call_id", "id")
		if strings.TrimSpace(name) == "" {
			return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: function_call name is required", index), nil)
		}
		callID = ResponsesFunctionCallCallID(callID)
		return core.Message{
			Role:        "assistant",
			Content:     "",
			ContentNull: true,
			ToolCalls: []core.ToolCall{
				{
					ID:          callID,
					Type:        "function",
					ExtraFields: core.UnknownJSONFieldsFromMap(rawJSONMapFromUnknownKeys(item, "type", "call_id", "id", "name", "arguments", "status")),
					Function: core.FunctionCall{
						Name:      name,
						Arguments: stringifyResponsesInputValue(item["arguments"]),
					},
				},
			},
		}, "function_call", nil
	case "function_call_output":
		callID := firstNonEmptyString(item, "call_id")
		if callID == "" {
			return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: function_call_output call_id is required", index), nil)
		}
		content, err := stringifyResponsesInputValueWithError(item["output"])
		if err != nil {
			return core.Message{}, "", core.NewInvalidRequestError(
				fmt.Sprintf("invalid responses input item at index %d: function_call_output.output must be JSON-serializable", index),
				err,
			)
		}
		return core.Message{
			Role:        "tool",
			ToolCallID:  callID,
			Content:     content,
			ExtraFields: core.UnknownJSONFieldsFromMap(rawJSONMapFromUnknownKeys(item, "type", "call_id", "status", "output")),
		}, "function_call_output", nil
	case "", "message":
	default:
		return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: unsupported input item type %q for chat-translated providers", index, itemType), nil)
	}

	role, _ := item["role"].(string)
	role = strings.TrimSpace(role)
	if role == "" {
		return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: role is required", index), nil)
	}

	content, ok := ConvertResponsesContentToChatContent(item["content"])
	if !ok {
		return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: unsupported content", index), nil)
	}
	return core.Message{
		Role:        role,
		Content:     content,
		ExtraFields: core.UnknownJSONFieldsFromMap(rawJSONMapFromUnknownKeys(item, "type", "role", "status", "content")),
	}, "message", nil
}

func cloneResponsesMessage(msg core.Message) core.Message {
	cloned := msg
	if len(msg.ToolCalls) > 0 {
		cloned.ToolCalls = make([]core.ToolCall, len(msg.ToolCalls))
		for i, call := range msg.ToolCalls {
			cloned.ToolCalls[i] = cloneResponsesToolCall(call)
		}
	}
	if parts, ok := msg.Content.([]core.ContentPart); ok {
		clonedParts := make([]core.ContentPart, len(parts))
		for i, part := range parts {
			clonedParts[i] = cloneResponsesContentPart(part)
		}
		cloned.Content = clonedParts
	}
	cloned.ExtraFields = core.CloneUnknownJSONFields(msg.ExtraFields)
	return cloned
}

func canMergeAssistantMessages(current, next core.Message) bool {
	if !current.ExtraFields.IsEmpty() || !next.ExtraFields.IsEmpty() {
		return false
	}
	if !core.HasStructuredContent(current.Content) && !core.HasStructuredContent(next.Content) {
		return true
	}
	return isAssistantToolCallOnlyMessage(next)
}

func mergeAssistantMessage(dst *core.Message, src core.Message) {
	if text := core.ExtractTextContent(src.Content); text != "" {
		existing := core.ExtractTextContent(dst.Content)
		dst.Content = existing + text
		dst.ContentNull = false
	}
	if len(src.ToolCalls) > 0 {
		dst.ToolCalls = append(dst.ToolCalls, src.ToolCalls...)
		if core.ExtractTextContent(dst.Content) == "" {
			dst.ContentNull = dst.ContentNull || src.ContentNull
		}
	}
}

func isAssistantToolCallOnlyMessage(msg core.Message) bool {
	if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
		return false
	}
	if core.HasStructuredContent(msg.Content) {
		return false
	}
	return core.ExtractTextContent(msg.Content) == ""
}

func firstNonEmptyString(item map[string]any, keys ...string) string {
	for _, key := range keys {
		value, _ := item[key].(string)
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stringifyResponsesInputValue(value any) string {
	encoded, err := stringifyResponsesInputValueWithError(value)
	if err != nil {
		return ""
	}
	return encoded
}

func stringifyResponsesInputValueWithError(value any) (string, error) {
	switch v := value.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}
