package server

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"gomodel/internal/core"
)

func normalizedResponseInputItems(responseID string, req *core.ResponsesRequest) []json.RawMessage {
	if req == nil || req.Input == nil {
		return nil
	}

	switch input := req.Input.(type) {
	case string:
		return []json.RawMessage{mustRawJSON(map[string]any{
			"id":   generatedResponseInputItemID(responseID, 0, "message", ""),
			"type": "message",
			"role": "user",
			"content": []map[string]any{
				{"type": "input_text", "text": input},
			},
		})}
	case []core.ResponsesInputElement:
		items := make([]json.RawMessage, 0, len(input))
		for i, item := range input {
			if normalized := normalizedResponseInputElement(responseID, i, item); len(normalized) > 0 {
				items = append(items, normalized)
			}
		}
		return items
	case []any:
		items := make([]json.RawMessage, 0, len(input))
		for i, item := range input {
			if normalized := normalizedResponseInputAny(responseID, i, item); len(normalized) > 0 {
				items = append(items, normalized)
			}
		}
		return items
	default:
		normalized := normalizedResponseInputAny(responseID, 0, input)
		if len(normalized) == 0 {
			return nil
		}
		return []json.RawMessage{normalized}
	}
}

func normalizedResponseInputElement(responseID string, index int, item core.ResponsesInputElement) json.RawMessage {
	raw, err := json.Marshal(item)
	if err != nil {
		return nil
	}
	return normalizedResponseInputRaw(responseID, index, raw)
}

func normalizedResponseInputAny(responseID string, index int, item any) json.RawMessage {
	raw, err := json.Marshal(item)
	if err != nil {
		return nil
	}
	return normalizedResponseInputRaw(responseID, index, raw)
}

func normalizedResponseInputRaw(responseID string, index int, raw json.RawMessage) json.RawMessage {
	var item map[string]any
	if err := json.Unmarshal(raw, &item); err != nil {
		var decoded string
		text := strings.TrimSpace(string(raw))
		if stringErr := json.Unmarshal(raw, &decoded); stringErr == nil {
			text = strings.TrimSpace(decoded)
		}
		if text == "" || text == "null" {
			return nil
		}
		return mustRawJSON(map[string]any{
			"id":   generatedResponseInputItemID(responseID, index, "message", ""),
			"type": "message",
			"role": "user",
			"content": []map[string]any{
				{"type": "input_text", "text": text},
			},
		})
	}
	if item == nil {
		return nil
	}

	itemType := strings.TrimSpace(stringFromMap(item, "type"))
	if itemType == "" {
		itemType = "message"
		item["type"] = itemType
	}

	switch itemType {
	case "message":
		if strings.TrimSpace(stringFromMap(item, "role")) == "" {
			item["role"] = "user"
		}
		item["content"] = normalizeResponseInputContent(item["content"])
	case "function_call", "function_call_output":
		// The decoded request has already normalized call_id/id aliases.
	default:
		// Unknown item types are preserved with an ID attached for pagination.
	}

	if strings.TrimSpace(stringFromMap(item, "id")) == "" {
		item["id"] = generatedResponseInputItemID(responseID, index, itemType, stringFromMap(item, "call_id"))
	}

	return mustRawJSON(item)
}

func normalizeResponseInputContent(content any) any {
	switch value := content.(type) {
	case nil:
		return []map[string]any{}
	case string:
		return []map[string]any{{"type": "input_text", "text": value}}
	case []core.ContentPart:
		items := make([]map[string]any, 0, len(value))
		for _, part := range value {
			items = append(items, normalizeContentPart(part))
		}
		return items
	case []any:
		items := make([]any, 0, len(value))
		for _, part := range value {
			items = append(items, normalizeResponseInputContentPartAny(part))
		}
		return items
	default:
		return content
	}
}

func normalizeContentPart(part core.ContentPart) map[string]any {
	switch strings.TrimSpace(part.Type) {
	case "text", "input_text":
		return map[string]any{"type": "input_text", "text": part.Text}
	case "image_url", "input_image":
		item := map[string]any{"type": "input_image"}
		if part.ImageURL != nil {
			item["image_url"] = part.ImageURL.URL
			if detail := strings.TrimSpace(part.ImageURL.Detail); detail != "" {
				item["detail"] = detail
			}
		}
		return item
	case "input_audio":
		item := map[string]any{"type": "input_audio"}
		if part.InputAudio != nil {
			item["input_audio"] = map[string]any{
				"data":   part.InputAudio.Data,
				"format": part.InputAudio.Format,
			}
		}
		return item
	default:
		return map[string]any{"type": part.Type, "text": part.Text}
	}
}

func normalizeResponseInputContentPartAny(part any) any {
	item, ok := part.(map[string]any)
	if !ok {
		return part
	}

	partType := strings.TrimSpace(stringFromMap(item, "type"))
	switch partType {
	case "text":
		normalized := cloneAnyMap(item)
		normalized["type"] = "input_text"
		return normalized
	case "image_url":
		normalized := cloneAnyMap(item)
		normalized["type"] = "input_image"
		if image, ok := normalized["image_url"].(map[string]any); ok {
			if url, ok := image["url"]; ok {
				normalized["image_url"] = url
			}
			if detail, ok := image["detail"]; ok {
				normalized["detail"] = detail
			}
		}
		return normalized
	default:
		return item
	}
}

func generatedResponseInputItemID(responseID string, index int, itemType, callID string) string {
	callID = strings.TrimSpace(callID)
	switch itemType {
	case "function_call":
		if callID != "" {
			return "fc_" + callID
		}
	case "function_call_output":
		if callID != "" {
			return "fco_" + callID
		}
	}
	seed := fmt.Sprintf("%s|%s|%d", strings.TrimSpace(responseID), strings.TrimSpace(itemType), index)
	return "msg_" + uuid.NewSHA1(uuid.NameSpaceOID, []byte(seed)).String()
}

func mustRawJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return raw
}

func stringFromMap(item map[string]any, key string) string {
	value, _ := item[key].(string)
	return strings.TrimSpace(value)
}

func cloneAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
