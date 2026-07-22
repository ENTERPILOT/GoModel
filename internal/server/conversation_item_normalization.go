package server

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/goccy/go-json"
	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/core"
)

func normalizeConversationItems(items []json.RawMessage) ([]json.RawMessage, *core.GatewayError) {
	normalized := make([]json.RawMessage, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for index, raw := range items {
		param := fmt.Sprintf("items[%d]", index)
		item, err := decodeRawJSONObject(raw)
		if err != nil {
			return nil, core.NewInvalidRequestError("conversation items must be JSON objects", err).WithParam(param)
		}
		if err := normalizeConversationItem(item, param); err != nil {
			return nil, err
		}
		id := rawJSONString(item, "id")
		if _, exists := seen[id]; exists {
			return nil, core.NewInvalidRequestError("duplicate conversation item id: "+id, nil).WithParam(param)
		}
		seen[id] = struct{}{}
		encoded, err := json.Marshal(item)
		if err != nil {
			return nil, core.NewInvalidRequestError("invalid conversation item", err).WithParam(param)
		}
		normalized = append(normalized, encoded)
	}
	return normalized, nil
}

func normalizeConversationItem(item rawJSONObject, param string) *core.GatewayError {
	itemType := rawJSONString(item, "type")
	if itemType == "" {
		if _, hasContent := item["content"]; !hasContent && rawJSONString(item, "role") == "" {
			return core.NewInvalidRequestError("conversation item type or message content is required", nil).WithParam(param)
		}
		itemType = "message"
		_ = setRawJSONValue(item, "type", itemType)
	}

	switch itemType {
	case "message":
		role := rawJSONString(item, "role")
		if role == "" {
			role = "user"
			_ = setRawJSONValue(item, "role", role)
		}
		if role != "user" && role != "system" && role != "developer" && role != "assistant" {
			return core.NewInvalidRequestError("unsupported conversation message role: "+role, nil).WithParam(param)
		}
		content, exists := item["content"]
		if !exists || !rawJSONValuePresent(item, "content") {
			return core.NewInvalidRequestError("conversation message content is required", nil).WithParam(param)
		}
		trimmed := bytes.TrimSpace(content)
		if len(trimmed) == 0 || (trimmed[0] != '"' && trimmed[0] != '[') {
			return core.NewInvalidRequestError("conversation message content must be a string or array", nil).WithParam(param)
		}
		normalizedContent, err := normalizeResponseInputContentRaw(content)
		if err != nil {
			return core.NewInvalidRequestError("invalid conversation message content", err).WithParam(param)
		}
		item["content"] = normalizedContent
		if rawJSONString(item, "status") == "" {
			_ = setRawJSONValue(item, "status", "completed")
		}
	case "function_call":
		if rawJSONString(item, "call_id") == "" || rawJSONString(item, "name") == "" {
			return core.NewInvalidRequestError("function_call requires call_id and name", nil).WithParam(param)
		}
		arguments, exists := item["arguments"]
		if !exists {
			return core.NewInvalidRequestError("function_call requires arguments", nil).WithParam(param)
		}
		var argumentString string
		if err := json.Unmarshal(arguments, &argumentString); err != nil {
			if err := setRawJSONValue(item, "arguments", string(bytes.TrimSpace(arguments))); err != nil {
				return core.NewInvalidRequestError("function_call arguments must be JSON", err).WithParam(param)
			}
		}
		if rawJSONString(item, "status") == "" {
			_ = setRawJSONValue(item, "status", "completed")
		}
	case "function_call_output":
		if rawJSONString(item, "call_id") == "" {
			return core.NewInvalidRequestError("function_call_output requires call_id", nil).WithParam(param)
		}
		if _, exists := item["output"]; !exists {
			return core.NewInvalidRequestError("function_call_output requires output", nil).WithParam(param)
		}
		if rawJSONString(item, "status") == "" {
			_ = setRawJSONValue(item, "status", "completed")
		}
	case "reasoning":
		if !rawJSONValuePresent(item, "summary") {
			item["summary"] = json.RawMessage("[]")
		}
	default:
		// Forward-compatible item variants stay opaque; only an id is added so
		// list pagination, retrieval, and deletion remain well defined.
	}

	if rawJSONString(item, "id") == "" {
		_ = setRawJSONValue(item, "id", generatedConversationItemID(itemType))
	}
	return nil
}

func generatedConversationItemID(itemType string) string {
	prefix := "item_"
	switch itemType {
	case "message":
		prefix = "msg_"
	case "function_call":
		prefix = "fc_"
	case "function_call_output":
		prefix = "fco_"
	case "reasoning":
		prefix = "rs_"
	}
	return prefix + strings.ReplaceAll(uuid.NewString(), "-", "")
}
