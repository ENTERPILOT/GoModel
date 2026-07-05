package core

import (
	"bytes"
	"fmt"

	"github.com/goccy/go-json"
)

// Known-field lists are derived from the struct definitions (json tags) at
// package init, so adding a typed field automatically stops it from being
// captured as an unknown extra field. ContentSchema swagger phantoms share
// tags with real fields, which is harmless here (duplicates in the list).
var (
	responsesRequestFields        = jsonFieldNames(ResponsesRequest{})
	responsesUtilityRequestFields = jsonFieldNames(ResponseInputTokensRequest{})
)

// responsesExtrasAndInput finishes a responses-shaped decode: it captures
// unknown members and decodes the raw input union.
func responsesExtrasAndInput(data []byte, rawInput json.RawMessage, knownFields []string) (any, UnknownJSONFields, error) {
	extraFields, err := extractUnknownJSONFields(data, knownFields...)
	if err != nil {
		return nil, UnknownJSONFields{}, err
	}
	input, err := decodeResponsesInput(rawInput)
	if err != nil {
		return nil, UnknownJSONFields{}, err
	}
	return input, extraFields, nil
}

// UnmarshalJSON preserves dynamic input payloads while supporting Swagger-only schema fields.
// Array inputs are deserialized as []ResponsesInputElement for type-safe downstream handling.
// The body decodes through an alias embedding so every typed field (present and
// future) is populated by the JSON package directly — only Input (a raw union)
// and ExtraFields need explicit handling.
func (r *ResponsesRequest) UnmarshalJSON(data []byte) error {
	type alias ResponsesRequest
	var raw struct {
		alias
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	input, extraFields, err := responsesExtrasAndInput(data, raw.Input, responsesRequestFields)
	if err != nil {
		return err
	}

	*r = ResponsesRequest(raw.alias)
	r.Input = input
	r.ExtraFields = extraFields
	return nil
}

func decodeResponsesInput(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if IsJSONNull(trimmed) {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var elements []ResponsesInputElement
		if err := json.Unmarshal(trimmed, &elements); err != nil {
			return nil, err
		}
		return elements, nil
	}

	var input any
	if err := json.Unmarshal(trimmed, &input); err != nil {
		return nil, err
	}
	return input, nil
}

// UnmarshalJSON accepts the documented Responses conversation union: a string
// ID or an object with an id field.
func (c *ResponsesConversationRef) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if IsJSONNull(trimmed) {
		*c = ResponsesConversationRef{}
		return nil
	}

	c.Raw = cloneRawMessage(trimmed)
	switch trimmed[0] {
	case '"':
		return json.Unmarshal(trimmed, &c.ID)
	case '{':
		var ref struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(trimmed, &ref); err != nil {
			return err
		}
		c.ID = ref.ID
		return nil
	default:
		return fmt.Errorf("conversation must be a string or object")
	}
}

// MarshalJSON preserves whether the conversation was originally supplied as a
// string or object. The ID field is authoritative so callers can update or
// clear a decoded reference without leaking the original raw value.
func (c ResponsesConversationRef) MarshalJSON() ([]byte, error) {
	trimmed := bytes.TrimSpace(c.Raw)
	if c.ID == "" {
		return []byte("null"), nil
	}
	if len(trimmed) > 0 {
		switch trimmed[0] {
		case '"':
			return json.Marshal(c.ID)
		case '{':
			var obj map[string]any
			if err := json.Unmarshal(trimmed, &obj); err != nil {
				return nil, err
			}
			obj["id"] = c.ID
			return json.Marshal(obj)
		default:
			return nil, fmt.Errorf("conversation raw must be a string or object")
		}
	}
	return json.Marshal(c.ID)
}

// MarshalJSON preserves dynamic input payloads while supporting Swagger-only schema fields.
// alias inherits every field and json tag from ResponsesRequest but drops the
// MarshalJSON method (so json.Marshal does not recurse); ExtraFields is json:"-"
// and merged in separately. New typed fields round-trip automatically.
func (r ResponsesRequest) MarshalJSON() ([]byte, error) {
	type alias ResponsesRequest
	return marshalWithUnknownJSONFields(alias(r), r.ExtraFields)
}

// UnmarshalJSON preserves the dynamic input payload for gateway utility requests.
func (r *ResponseInputTokensRequest) UnmarshalJSON(data []byte) error {
	type alias ResponseInputTokensRequest
	var raw struct {
		alias
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	input, extraFields, err := responsesExtrasAndInput(data, raw.Input, responsesUtilityRequestFields)
	if err != nil {
		return err
	}

	*r = ResponseInputTokensRequest(raw.alias)
	r.Input = input
	r.ExtraFields = extraFields
	return nil
}

// MarshalJSON preserves the dynamic input payload while omitting Swagger-only schema fields.
func (r ResponseInputTokensRequest) MarshalJSON() ([]byte, error) {
	type alias ResponseInputTokensRequest
	return marshalWithUnknownJSONFields(alias(r), r.ExtraFields)
}

// UnmarshalJSON preserves the dynamic input payload for gateway utility requests.
func (r *ResponseCompactRequest) UnmarshalJSON(data []byte) error {
	var utility ResponseInputTokensRequest
	if err := utility.UnmarshalJSON(data); err != nil {
		return err
	}
	*r = ResponseCompactRequest(utility)
	return nil
}

// MarshalJSON preserves the dynamic input payload while omitting Swagger-only schema fields.
func (r ResponseCompactRequest) MarshalJSON() ([]byte, error) {
	return ResponseInputTokensRequest(r).MarshalJSON()
}

// UnmarshalJSON deserializes a ResponsesInputElement, switching on the "type"
// field to populate variant-specific fields.
func (e *ResponsesInputElement) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*e = ResponsesInputElement{}

	if v, ok := raw["type"]; ok {
		_ = json.Unmarshal(v, &e.Type)
	}

	switch e.Type {
	case "function_call":
		if v, ok := raw["name"]; ok {
			_ = json.Unmarshal(v, &e.Name)
		}
		// Accept both call_id and id for compatibility.
		if v, ok := raw["call_id"]; ok {
			_ = json.Unmarshal(v, &e.CallID)
		} else if v, ok := raw["id"]; ok {
			_ = json.Unmarshal(v, &e.CallID)
		}
		if v, ok := raw["status"]; ok {
			_ = json.Unmarshal(v, &e.Status)
		}
		if v, ok := raw["arguments"]; ok {
			e.Arguments = stringifyRawValue(v)
		}
	case "function_call_output":
		if v, ok := raw["call_id"]; ok {
			_ = json.Unmarshal(v, &e.CallID)
		}
		if v, ok := raw["status"]; ok {
			_ = json.Unmarshal(v, &e.Status)
		}
		if v, ok := raw["output"]; ok {
			e.Output = stringifyRawValue(v)
		}
	case "", "message":
		if v, ok := raw["role"]; ok {
			_ = json.Unmarshal(v, &e.Role)
		}
		if v, ok := raw["status"]; ok {
			_ = json.Unmarshal(v, &e.Status)
		}
		if v, ok := raw["content"]; ok {
			trimmed := bytes.TrimSpace(v)
			if len(trimmed) != 0 && !bytes.Equal(trimmed, []byte("null")) {
				var content any
				_ = json.Unmarshal(trimmed, &content)
				e.Content = content
			}
		}
	default:
		// Unknown item types are preserved verbatim in Raw, which already holds
		// every field. Skip ExtraFields extraction here so a round trip emits Raw
		// once; ExtraFields stays reserved for metadata added after decoding.
		e.Raw = cloneRawMessage(data)
		return nil
	}

	knownFields := []string{"type"}
	switch e.Type {
	case "function_call":
		knownFields = append(knownFields, "call_id", "id", "name", "arguments", "status")
	case "function_call_output":
		knownFields = append(knownFields, "call_id", "status", "output")
	case "", "message":
		knownFields = append(knownFields, "role", "status", "content")
	}

	extraFields, err := extractUnknownJSONFields(data, knownFields...)
	if err != nil {
		return err
	}
	e.ExtraFields = extraFields
	return nil
}

// MarshalJSON serializes a ResponsesInputElement, emitting only the fields
// relevant to its Type variant.
func (e ResponsesInputElement) MarshalJSON() ([]byte, error) {
	switch e.Type {
	case "function_call":
		return marshalWithUnknownJSONFields(struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id,omitempty"`
			Name      string `json:"name,omitempty"`
			Arguments string `json:"arguments,omitempty"`
			Status    string `json:"status,omitempty"`
		}{
			Type:      "function_call",
			CallID:    e.CallID,
			Name:      e.Name,
			Arguments: e.Arguments,
			Status:    e.Status,
		}, e.ExtraFields)
	case "function_call_output":
		return marshalWithUnknownJSONFields(struct {
			Type   string `json:"type"`
			CallID string `json:"call_id,omitempty"`
			Output string `json:"output,omitempty"`
			Status string `json:"status,omitempty"`
		}{
			Type:   "function_call_output",
			CallID: e.CallID,
			Output: e.Output,
			Status: e.Status,
		}, e.ExtraFields)
	case "", "message":
		type msg struct {
			Type    string `json:"type,omitempty"`
			Role    string `json:"role"`
			Content any    `json:"content"`
			Status  string `json:"status,omitempty"`
		}
		return marshalWithUnknownJSONFields(msg{
			Type:    e.Type,
			Role:    e.Role,
			Content: e.Content,
			Status:  e.Status,
		}, e.ExtraFields)
	default:
		if len(bytes.TrimSpace(e.Raw)) > 0 {
			if e.ExtraFields.IsEmpty() {
				return cloneRawMessage(e.Raw), nil
			}
			return mergeUnknownJSONObject(e.Raw, e.ExtraFields.raw)
		}
		return marshalWithUnknownJSONFields(struct {
			Type string `json:"type"`
		}{
			Type: e.Type,
		}, e.ExtraFields)
	}
}

// cloneRawMessage returns a detached, whitespace-trimmed copy of a raw JSON
// value so stored Raw fields stay independent of the decoder's backing buffer.
func cloneRawMessage(data []byte) json.RawMessage {
	return CloneRawJSON(bytes.TrimSpace(data))
}

// stringifyRawValue converts a json.RawMessage to a string.
// JSON strings are unwrapped; objects/arrays are returned as-is.
func stringifyRawValue(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if IsJSONNull(trimmed) {
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			return s
		}
	}
	return string(trimmed)
}
