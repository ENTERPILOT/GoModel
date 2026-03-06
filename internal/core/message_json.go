package core

import "encoding/json"

// UnmarshalJSON validates chat request message content while preserving multimodal payloads.
func (m *Message) UnmarshalJSON(data []byte) error {
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

// MarshalJSON ensures only supported chat request message content shapes are emitted.
func (m Message) MarshalJSON() ([]byte, error) {
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
	if len(toolCalls) > 0 && raw == nil {
		content = nil
	} else {
		content, err = NormalizeMessageContent(raw)
		if err != nil {
			return nil, err
		}
	}
	return content, nil
}
