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

	content, err := UnmarshalMessageContent(raw.Content)
	if err != nil {
		return err
	}

	m.Role = raw.Role
	m.Content = content
	m.ToolCalls = raw.ToolCalls
	m.ToolCallID = raw.ToolCallID
	m.ContentNull = content == nil
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

	return json.Marshal(struct {
		Role       string     `json:"role"`
		Content    any        `json:"content"`
		ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
	}{
		Role:       m.Role,
		Content:    content,
		ToolCalls:  m.ToolCalls,
		ToolCallID: m.ToolCallID,
	})
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

	content, err := UnmarshalMessageContent(raw.Content)
	if err != nil {
		return err
	}

	m.Role = raw.Role
	m.Content = content
	m.ToolCalls = raw.ToolCalls
	return nil
}

// MarshalJSON preserves OpenAI-compatible null content for tool-call response messages.
func (m ResponseMessage) MarshalJSON() ([]byte, error) {
	content, err := marshalMessageContent(m.Content, m.ToolCalls)
	if err != nil {
		return nil, err
	}

	return json.Marshal(struct {
		Role      string     `json:"role"`
		Content   any        `json:"content"`
		ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	}{
		Role:      m.Role,
		Content:   content,
		ToolCalls: m.ToolCalls,
	})
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
