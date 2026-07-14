package guardrails

import (
	"errors"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

func rewriteGuardedChatBatchBody(originalBody json.RawMessage, original *core.ChatRequest, modified *core.ChatRequest) (json.RawMessage, error) {
	if modified == nil {
		return nil, core.NewInvalidRequestError("missing guarded chat request", nil)
	}
	if original == nil {
		return nil, core.NewInvalidRequestError("missing original chat request", nil)
	}
	body, err := patchGuardedChatBatchBody(originalBody, original, modified)
	if err == nil {
		return body, nil
	}
	// Validation errors (e.g. guardrails tried to insert/reorder messages)
	// must propagate — falling back to Marshal(modified) would silently
	// publish the invalid rewrite. Only fall back for raw-body preservation
	// failures (malformed original JSON, etc.).
	if gwErr, ok := errors.AsType[*core.GatewayError](err); ok && gwErr.Type == core.ErrorTypeInvalidRequest {
		return nil, err
	}
	return json.Marshal(modified)
}

func patchGuardedChatBatchBody(originalBody json.RawMessage, original *core.ChatRequest, modified *core.ChatRequest) (json.RawMessage, error) {
	if modified == nil {
		return nil, core.NewInvalidRequestError("missing guarded chat request", nil)
	}
	if original == nil {
		return nil, core.NewInvalidRequestError("missing original chat request", nil)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(originalBody, &raw); err != nil {
		return nil, err
	}

	patchedMessages, err := patchChatMessagesJSON(raw["messages"], original.Messages, modified.Messages)
	if err != nil {
		return nil, err
	}
	raw["messages"] = patchedMessages
	return json.Marshal(raw)
}

func patchChatMessagesJSON(originalRaw json.RawMessage, original, modified []core.Message) (json.RawMessage, error) {
	originalRawItems, err := unmarshalJSONArray(originalRaw)
	if err != nil {
		return nil, err
	}
	if len(originalRawItems) != len(original) {
		return nil, core.NewInvalidRequestError("guardrails chat message payload does not match parsed request", nil)
	}

	systemOriginals := make([]json.RawMessage, 0, len(original))
	nonSystemOriginals := make([]json.RawMessage, 0, len(original))
	nonSystemMessages := make([]core.Message, 0, len(original))
	for i, msg := range original {
		if msg.Role == "system" {
			systemOriginals = append(systemOriginals, originalRawItems[i])
			continue
		}
		nonSystemOriginals = append(nonSystemOriginals, originalRawItems[i])
		nonSystemMessages = append(nonSystemMessages, msg)
	}

	patched := make([]json.RawMessage, 0, len(modified))
	modifiedSystemCount := 0
	for _, msg := range modified {
		if msg.Role == "system" {
			modifiedSystemCount++
		}
	}
	systemMatchStart, originalSystemStart := tailMatchedSystemOffsets(len(systemOriginals), modifiedSystemCount)
	nextSystem := 0
	nextNonSystem := 0
	for _, msg := range modified {
		if msg.Role == "system" {
			if nextSystem >= systemMatchStart {
				item, err := patchRawChatMessage(systemOriginals[originalSystemStart+(nextSystem-systemMatchStart)], msg)
				if err != nil {
					return nil, err
				}
				patched = append(patched, item)
			} else {
				item, err := json.Marshal(msg)
				if err != nil {
					return nil, err
				}
				patched = append(patched, item)
			}
			nextSystem++
			continue
		}

		if nextNonSystem >= len(nonSystemOriginals) {
			return nil, core.NewInvalidRequestError("guardrails cannot insert non-system chat messages", nil)
		}
		if nonSystemMessages[nextNonSystem].Role != msg.Role {
			return nil, core.NewInvalidRequestError("guardrails cannot reorder non-system chat messages", nil)
		}
		item, err := patchRawChatMessage(nonSystemOriginals[nextNonSystem], msg)
		if err != nil {
			return nil, err
		}
		patched = append(patched, item)
		nextNonSystem++
	}
	if nextNonSystem != len(nonSystemOriginals) {
		return nil, core.NewInvalidRequestError("guardrails cannot add or remove non-system chat messages", nil)
	}

	return json.Marshal(patched)
}

func patchRawChatMessage(original json.RawMessage, modified core.Message) (json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(original, &raw); err != nil {
		return nil, err
	}

	updatedBody, err := json.Marshal(modified)
	if err != nil {
		return nil, err
	}

	var updated map[string]json.RawMessage
	if err := json.Unmarshal(updatedBody, &updated); err != nil {
		return nil, err
	}

	for _, field := range []string{"role", "content", "tool_calls", "tool_call_id"} {
		delete(raw, field)
		if value, ok := updated[field]; ok {
			raw[field] = value
		}
	}

	return json.Marshal(raw)
}

func rewriteGuardedResponsesBatchBody(originalBody json.RawMessage, modified *core.ResponsesRequest) (json.RawMessage, error) {
	if modified == nil {
		return nil, core.NewInvalidRequestError("missing guarded responses request", nil)
	}

	body, err := patchJSONObjectFields(originalBody, map[string]jsonFieldPatch{
		"instructions": {value: modified.Instructions, omitWhenEmpty: modified.Instructions == ""},
		"input":        {value: modified.Input},
	})
	if err == nil {
		return body, nil
	}
	return json.Marshal(modified)
}

type jsonFieldPatch struct {
	value         any
	omitWhenEmpty bool
}

func patchJSONObjectFields(originalBody json.RawMessage, patches map[string]jsonFieldPatch) (json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(originalBody, &raw); err != nil {
		return nil, err
	}

	for field, patch := range patches {
		if patch.omitWhenEmpty && isZeroJSONFieldValue(patch.value) {
			delete(raw, field)
			continue
		}

		encoded, err := json.Marshal(patch.value)
		if err != nil {
			return nil, err
		}
		raw[field] = encoded
	}

	return json.Marshal(raw)
}

func unmarshalJSONArray(raw json.RawMessage) ([]json.RawMessage, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func isZeroJSONFieldValue(value any) bool {
	switch v := value.(type) {
	case string:
		return v == ""
	default:
		return value == nil
	}
}
