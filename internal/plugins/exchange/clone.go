package exchange

import "maps"

import "github.com/enterpilot/gomodel/internal/core"

func cloneToolCalls(toolCalls []core.ToolCall) []core.ToolCall {
	if len(toolCalls) == 0 {
		return nil
	}
	cloned := make([]core.ToolCall, len(toolCalls))
	for i, tc := range toolCalls {
		cloned[i] = cloneToolCall(tc)
	}
	return cloned
}

func cloneToolCall(tc core.ToolCall) core.ToolCall {
	return core.ToolCall{
		ID:   tc.ID,
		Type: tc.Type,
		Function: core.FunctionCall{
			Name:        tc.Function.Name,
			Arguments:   tc.Function.Arguments,
			ExtraFields: core.CloneUnknownJSONFields(tc.Function.ExtraFields),
		},
		ExtraFields: core.CloneUnknownJSONFields(tc.ExtraFields),
	}
}

func cloneChatMessage(m core.Message) core.Message {
	return core.Message{
		Role:        m.Role,
		ToolCallID:  m.ToolCallID,
		ContentNull: m.ContentNull,
		Content:     cloneContent(m.Content),
		ToolCalls:   cloneToolCalls(m.ToolCalls),
		ExtraFields: core.CloneUnknownJSONFields(m.ExtraFields),
	}
}

func cloneContent(content any) any {
	switch value := content.(type) {
	case nil:
		return nil
	case string:
		return value
	case []core.ContentPart:
		return cloneContentParts(value)
	case []any:
		return cloneAnySlice(value)
	case []map[string]any:
		out := make([]map[string]any, len(value))
		for i, m := range value {
			out[i] = cloneAnyMap(m)
		}
		return out
	default:
		return value
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

// cloneAnySlice copies one level of a decoded JSON array; nested values are
// shared, since apply-back replaces whole values instead of mutating them.
func cloneAnySlice(items []any) []any {
	if items == nil {
		return nil
	}
	out := make([]any, len(items))
	for i, item := range items {
		if m, ok := item.(map[string]any); ok {
			out[i] = cloneAnyMap(m)
			continue
		}
		out[i] = item
	}
	return out
}

func cloneAnyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	maps.Copy(out, m)
	return out
}
