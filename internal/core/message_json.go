package core

import (
	"encoding/json"
	"strings"
)

// UnmarshalJSON validates chat request message content while preserving multimodal payloads.
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
	extraFields, err := extractUnknownJSONFields(data,
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

// MarshalJSON ensures only supported chat request message content shapes are emitted.
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

	return marshalWithUnknownJSONFields(struct {
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

// UnmarshalJSON validates chat response message content while preserving tool-call null content.
func (m *ResponseMessage) UnmarshalJSON(data []byte) error {
	var raw struct {
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		ToolCalls []ToolCall      `json:"tool_calls,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	extraFields, err := extractUnknownJSONFields(data,
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

// MarshalJSON preserves OpenAI-compatible null content for tool-call response messages.
func (m ResponseMessage) MarshalJSON() ([]byte, error) {
	content, err := marshalMessageContent(m.Content, m.ToolCalls)
	if err != nil {
		return nil, err
	}

	return marshalWithUnknownJSONFields(struct {
		Role      string     `json:"role"`
		Content   any        `json:"content"`
		ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	}{
		Role:      m.Role,
		Content:   content,
		ToolCalls: m.ToolCalls,
	}, m.ExtraFields)
}

func (t *ToolCall) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID       string       `json:"id"`
		Type     string       `json:"type"`
		Function FunctionCall `json:"function"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	extraFields, err := extractUnknownJSONFields(data,
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

func (t ToolCall) MarshalJSON() ([]byte, error) {
	return marshalWithUnknownJSONFields(struct {
		ID       string       `json:"id"`
		Type     string       `json:"type"`
		Function FunctionCall `json:"function"`
	}{
		ID:       t.ID,
		Type:     t.Type,
		Function: t.Function,
	}, t.ExtraFields)
}

func (f *FunctionCall) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	extraFields, err := extractUnknownJSONFields(data,
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

func (f FunctionCall) MarshalJSON() ([]byte, error) {
	return marshalWithUnknownJSONFields(struct {
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
