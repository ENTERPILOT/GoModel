package core

import (
	"encoding/json"
	"strings"
)

// Message.UnmarshalJSON validates chat request message content, preserves
// unknown JSON members in ExtraFields, and keeps null content handling intact.
func (m *Message) UnmarshalJSON(data []byte) error {
	var raw struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
		ToolCallID string          `json:"tool_call_id,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	extraFields, err := extractUnknownJSONFieldsObject(data,
		"role",
		"content",
		"tool_calls",
		"tool_call_id",
	)
	if err != nil {
		return err
	}

	content, err := UnmarshalMessageContent(raw.Content)
	if err != nil {
		return err
	}

	m.Role = raw.Role
	m.Content = content
	m.ToolCalls = raw.ToolCalls
	m.ToolCallID = raw.ToolCallID
	m.ContentNull = content == nil
	m.ExtraFields = extraFields
	return nil
}

// Message.MarshalJSON emits validated chat request message content, preserves
// null handling, and includes unknown JSON members from ExtraFields.
func (m Message) MarshalJSON() ([]byte, error) {
	content := any(nil)
	var err error
	switch {
	case m.ContentNull && isNullEquivalentContent(m.Content):
		content = nil
	default:
		content, err = marshalMessageContent(m.Content, m.ToolCalls)
		if err != nil {
			return nil, err
		}
	}

	return marshalWithUnknownJSONFieldsObject(struct {
		Role       string     `json:"role"`
		Content    any        `json:"content"`
		ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
	}{
		Role:       m.Role,
		Content:    content,
		ToolCalls:  m.ToolCalls,
		ToolCallID: m.ToolCallID,
	}, m.ExtraFields)
}

// ResponseMessage.UnmarshalJSON validates chat response message content,
// preserves unknown JSON members in ExtraFields, and keeps tool-call null
// content handling intact.
func (m *ResponseMessage) UnmarshalJSON(data []byte) error {
	var raw struct {
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		ToolCalls []ToolCall      `json:"tool_calls,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	extraFields, err := extractUnknownJSONFieldsObject(data,
		"role",
		"content",
		"tool_calls",
	)
	if err != nil {
		return err
	}

	content, err := UnmarshalMessageContent(raw.Content)
	if err != nil {
		return err
	}

	m.Role = raw.Role
	m.Content = content
	m.ToolCalls = raw.ToolCalls
	m.ExtraFields = extraFields
	return nil
}

// ResponseMessage.MarshalJSON preserves OpenAI-compatible null content for
// tool-call response messages and includes unknown JSON members from
// ExtraFields.
func (m ResponseMessage) MarshalJSON() ([]byte, error) {
	content, err := marshalMessageContent(m.Content, m.ToolCalls)
	if err != nil {
		return nil, err
	}

	return marshalWithUnknownJSONFieldsObject(struct {
		Role      string     `json:"role"`
		Content   any        `json:"content"`
		ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	}{
		Role:      m.Role,
		Content:   content,
		ToolCalls: m.ToolCalls,
	}, m.ExtraFields)
}

// ToolCall.UnmarshalJSON unmarshals a ToolCall from JSON, preserving unknown
// JSON members in ExtraFields.
func (t *ToolCall) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID       string       `json:"id"`
		Type     string       `json:"type"`
		Function FunctionCall `json:"function"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	extraFields, err := extractUnknownJSONFieldsObject(data,
		"id",
		"type",
		"function",
	)
	if err != nil {
		return err
	}

	t.ID = raw.ID
	t.Type = raw.Type
	t.Function = raw.Function
	t.ExtraFields = extraFields
	return nil
}

// ToolCall.MarshalJSON marshals a ToolCall to JSON, including unknown JSON
// members from ExtraFields.
func (t ToolCall) MarshalJSON() ([]byte, error) {
	return marshalWithUnknownJSONFieldsObject(struct {
		ID       string       `json:"id"`
		Type     string       `json:"type"`
		Function FunctionCall `json:"function"`
	}{
		ID:       t.ID,
		Type:     t.Type,
		Function: t.Function,
	}, t.ExtraFields)
}

// FunctionCall.UnmarshalJSON unmarshals a FunctionCall from JSON, preserving
// unknown JSON members in ExtraFields.
func (f *FunctionCall) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	extraFields, err := extractUnknownJSONFieldsObject(data,
		"name",
		"arguments",
	)
	if err != nil {
		return err
	}

	f.Name = raw.Name
	f.Arguments = raw.Arguments
	f.ExtraFields = extraFields
	return nil
}

// FunctionCall.MarshalJSON marshals a FunctionCall to JSON, including unknown
// JSON members from ExtraFields.
func (f FunctionCall) MarshalJSON() ([]byte, error) {
	return marshalWithUnknownJSONFieldsObject(struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{
		Name:      f.Name,
		Arguments: f.Arguments,
	}, f.ExtraFields)
}

func marshalMessageContent(raw MessageContent, toolCalls []ToolCall) (any, error) {
	var (
		content any
		err     error
	)

	// OpenAI-compatible tool-call assistant messages use `content: null`.
	if len(toolCalls) > 0 && isNullEquivalentContent(raw) {
		content = nil
	} else {
		content, err = NormalizeMessageContent(raw)
		if err != nil {
			return nil, err
		}
	}
	return content, nil
}

func isNullEquivalentContent(raw MessageContent) bool {
	if raw == nil {
		return true
	}
	text, ok := raw.(string)
	if !ok {
		return false
	}
	return strings.TrimSpace(text) == ""
}
