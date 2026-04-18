package core

import (
	"bytes"
	"encoding/json"
)

// UnmarshalJSON preserves dynamic input payloads while supporting Swagger-only schema fields.
// Array inputs are deserialized as []ResponsesInputElement for type-safe downstream handling.
func (r *ResponsesRequest) UnmarshalJSON(data []byte) error {
	var raw struct {
		Model             string            `json:"model"`
		Provider          string            `json:"provider,omitempty"`
		Input             json.RawMessage   `json:"input"`
		Instructions      string            `json:"instructions,omitempty"`
		Tools             []map[string]any  `json:"tools,omitempty"`
		ToolChoice        any               `json:"tool_choice,omitempty"`
		ParallelToolCalls *bool             `json:"parallel_tool_calls,omitempty"`
		Temperature       *float64          `json:"temperature,omitempty"`
		MaxOutputTokens   *int              `json:"max_output_tokens,omitempty"`
		Stream            bool              `json:"stream,omitempty"`
		StreamOptions     *StreamOptions    `json:"stream_options,omitempty"`
		Metadata          map[string]string `json:"metadata,omitempty"`
		Reasoning         *Reasoning        `json:"reasoning,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	extraFields, err := extractUnknownJSONFields(data,
		"model",
		"provider",
		"input",
		"instructions",
		"tools",
		"tool_choice",
		"parallel_tool_calls",
		"temperature",
		"max_output_tokens",
		"stream",
		"stream_options",
		"metadata",
		"reasoning",
	)
	if err != nil {
		return err
	}

	var input any
	trimmed := bytes.TrimSpace(raw.Input)
	if len(trimmed) != 0 && !bytes.Equal(trimmed, []byte("null")) {
		if trimmed[0] == '[' {
			var elements []ResponsesInputElement
			if err := json.Unmarshal(trimmed, &elements); err != nil {
				return err
			}
			input = elements
		} else {
			if err := json.Unmarshal(trimmed, &input); err != nil {
				return err
			}
		}
	}

	r.Model = raw.Model
	r.Provider = raw.Provider
	r.Input = input
	r.Instructions = raw.Instructions
	r.Tools = raw.Tools
	r.ToolChoice = raw.ToolChoice
	r.ParallelToolCalls = raw.ParallelToolCalls
	r.Temperature = raw.Temperature
	r.MaxOutputTokens = raw.MaxOutputTokens
	r.Stream = raw.Stream
	r.StreamOptions = raw.StreamOptions
	r.Metadata = raw.Metadata
	r.Reasoning = raw.Reasoning
	r.ExtraFields = extraFields
	return nil
}

// MarshalJSON preserves dynamic input payloads while supporting Swagger-only schema fields.
func (r ResponsesRequest) MarshalJSON() ([]byte, error) {
	return marshalWithUnknownJSONFields(struct {
		Model             string            `json:"model"`
		Provider          string            `json:"provider,omitempty"`
		Input             any               `json:"input"`
		Instructions      string            `json:"instructions,omitempty"`
		Tools             []map[string]any  `json:"tools,omitempty"`
		ToolChoice        any               `json:"tool_choice,omitempty"`
		ParallelToolCalls *bool             `json:"parallel_tool_calls,omitempty"`
		Temperature       *float64          `json:"temperature,omitempty"`
		MaxOutputTokens   *int              `json:"max_output_tokens,omitempty"`
		Stream            bool              `json:"stream,omitempty"`
		StreamOptions     *StreamOptions    `json:"stream_options,omitempty"`
		Metadata          map[string]string `json:"metadata,omitempty"`
		Reasoning         *Reasoning        `json:"reasoning,omitempty"`
	}{
		Model:             r.Model,
		Provider:          r.Provider,
		Input:             r.Input,
		Instructions:      r.Instructions,
		Tools:             r.Tools,
		ToolChoice:        r.ToolChoice,
		ParallelToolCalls: r.ParallelToolCalls,
		Temperature:       r.Temperature,
		MaxOutputTokens:   r.MaxOutputTokens,
		Stream:            r.Stream,
		StreamOptions:     r.StreamOptions,
		Metadata:          r.Metadata,
		Reasoning:         r.Reasoning,
	}, r.ExtraFields)
}

// MarshalJSON preserves the dynamic input payload while omitting Swagger-only schema fields.
func (r ResponseInputTokensRequest) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Model        string            `json:"model,omitempty"`
		Input        any               `json:"input,omitempty"`
		Instructions string            `json:"instructions,omitempty"`
		Metadata     map[string]string `json:"metadata,omitempty"`
		Reasoning    *Reasoning        `json:"reasoning,omitempty"`
	}{
		Model:        r.Model,
		Input:        r.Input,
		Instructions: r.Instructions,
		Metadata:     r.Metadata,
		Reasoning:    r.Reasoning,
	})
}

// MarshalJSON preserves the dynamic input payload while omitting Swagger-only schema fields.
func (r ResponseCompactRequest) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Model        string            `json:"model,omitempty"`
		Input        any               `json:"input,omitempty"`
		Instructions string            `json:"instructions,omitempty"`
		Metadata     map[string]string `json:"metadata,omitempty"`
		Reasoning    *Reasoning        `json:"reasoning,omitempty"`
	}{
		Model:        r.Model,
		Input:        r.Input,
		Instructions: r.Instructions,
		Metadata:     r.Metadata,
		Reasoning:    r.Reasoning,
	})
}

// UnmarshalJSON deserializes a ResponsesInputElement, switching on the "type"
// field to populate variant-specific fields.
func (e *ResponsesInputElement) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

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
	default: // message (type="" or "message")
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
	}

	knownFields := []string{"type"}
	switch e.Type {
	case "function_call":
		knownFields = append(knownFields, "call_id", "id", "name", "arguments", "status")
	case "function_call_output":
		knownFields = append(knownFields, "call_id", "status", "output")
	default:
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
	default: // message
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
	}
}

// stringifyRawValue converts a json.RawMessage to a string.
// JSON strings are unwrapped; objects/arrays are returned as-is.
func stringifyRawValue(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
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
