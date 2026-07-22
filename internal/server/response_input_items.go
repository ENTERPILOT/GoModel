package server

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/goccy/go-json"

	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/core"
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
	item, err := decodeRawJSONObject(raw)
	if err != nil {
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
	itemType := rawJSONString(item, "type")
	if itemType == "" {
		itemType = "message"
		_ = setRawJSONValue(item, "type", itemType)
	}

	switch itemType {
	case "message":
		if rawJSONString(item, "role") == "" {
			_ = setRawJSONValue(item, "role", "user")
		}
		normalizedContent, contentErr := normalizeResponseInputContentRaw(item["content"])
		if contentErr == nil {
			item["content"] = normalizedContent
		}
	case "function_call", "function_call_output":
		// The decoded request has already normalized call_id/id aliases.
	default:
		// Unknown item types are preserved with an ID attached for pagination.
	}

	if rawJSONString(item, "id") == "" {
		_ = setRawJSONValue(item, "id", generatedResponseInputItemID(responseID, index, itemType, rawJSONString(item, "call_id")))
	}

	return mustRawJSON(item)
}

func normalizeResponseInputContentRaw(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage("[]"), nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, err
		}
		return json.Marshal([]map[string]any{{"type": "input_text", "text": text}})
	}
	if trimmed[0] != '[' {
		return core.CloneRawJSON(trimmed), nil
	}

	var parts []json.RawMessage
	if err := json.Unmarshal(trimmed, &parts); err != nil {
		return nil, err
	}
	for index, rawPart := range parts {
		part, err := decodeRawJSONObject(rawPart)
		if err != nil {
			continue
		}
		switch rawJSONString(part, "type") {
		case "text":
			_ = setRawJSONValue(part, "type", "input_text")
		case "image_url", "input_image":
			_ = setRawJSONValue(part, "type", "input_image")
			if image, imageErr := decodeRawJSONObject(part["image_url"]); imageErr == nil {
				if url, exists := image["url"]; exists {
					part["image_url"] = core.CloneRawJSON(url)
				}
				if detail, exists := image["detail"]; exists {
					part["detail"] = core.CloneRawJSON(detail)
				}
			}
		}
		parts[index], err = json.Marshal(part)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(parts)
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
