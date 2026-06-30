package guardrails

import "gomodel/internal/core"

// cloneToolCalls deep-copies tool calls so guardrail rewrites never mutate the
// caller's original message slice.
func cloneToolCalls(toolCalls []core.ToolCall) []core.ToolCall {
	if len(toolCalls) == 0 {
		return nil
	}
	cloned := make([]core.ToolCall, len(toolCalls))
	for i, toolCall := range toolCalls {
		cloned[i] = core.ToolCall{
			ID:   toolCall.ID,
			Type: toolCall.Type,
			Function: core.FunctionCall{
				Name:        toolCall.Function.Name,
				Arguments:   toolCall.Function.Arguments,
				ExtraFields: core.CloneUnknownJSONFields(toolCall.Function.ExtraFields),
			},
			ExtraFields: core.CloneUnknownJSONFields(toolCall.ExtraFields),
		}
	}
	return cloned
}

func cloneChatMessageEnvelope(message core.Message) core.Message {
	return core.Message{
		Role:        message.Role,
		ToolCallID:  message.ToolCallID,
		ContentNull: message.ContentNull,
		Content:     cloneMessageContent(message.Content),
		ToolCalls:   cloneToolCalls(message.ToolCalls),
		ExtraFields: core.CloneUnknownJSONFields(message.ExtraFields),
	}
}

func cloneMessageContent(content any) any {
	switch value := content.(type) {
	case nil:
		return nil
	case string:
		return value
	case []core.ContentPart:
		return cloneContentParts(value)
	default:
		parts, ok := core.NormalizeContentParts(content)
		if !ok {
			// Unrecognized content shapes cannot be deep-copied generically, so
			// they are returned as-is. Guardrails replace whole content values
			// rather than mutating them in place, so sharing the reference is
			// safe; chat content is normalized to nil/string/[]ContentPart
			// before reaching here, making this branch defensive.
			return value
		}
		return cloneContentParts(parts)
	}
}

func cloneContentParts(parts []core.ContentPart) []core.ContentPart {
	if len(parts) == 0 {
		return nil
	}
	cloned := make([]core.ContentPart, len(parts))
	for i, part := range parts {
		cloned[i] = cloneContentPart(part)
	}
	return cloned
}

func cloneContentPart(part core.ContentPart) core.ContentPart {
	cloned := core.ContentPart{
		Type:        part.Type,
		Text:        part.Text,
		ExtraFields: core.CloneUnknownJSONFields(part.ExtraFields),
	}
	if part.ImageURL != nil {
		cloned.ImageURL = &core.ImageURLContent{
			URL:         part.ImageURL.URL,
			Detail:      part.ImageURL.Detail,
			MediaType:   part.ImageURL.MediaType,
			ExtraFields: core.CloneUnknownJSONFields(part.ImageURL.ExtraFields),
		}
	}
	if part.InputAudio != nil {
		cloned.InputAudio = &core.InputAudioContent{
			Data:        part.InputAudio.Data,
			Format:      part.InputAudio.Format,
			ExtraFields: core.CloneUnknownJSONFields(part.InputAudio.ExtraFields),
		}
	}
	return cloned
}
