package core

import (
	"bytes"
	"encoding/json"
)

// UnmarshalJSON preserves dynamic input payloads while supporting Swagger-only schema fields.
func (r *ResponsesRequest) UnmarshalJSON(data []byte) error {
	var raw struct {
		Model           string            `json:"model"`
		Provider        string            `json:"provider,omitempty"`
		Input           json.RawMessage   `json:"input"`
		Instructions    string            `json:"instructions,omitempty"`
		Tools           []map[string]any  `json:"tools,omitempty"`
		Temperature     *float64          `json:"temperature,omitempty"`
		MaxOutputTokens *int              `json:"max_output_tokens,omitempty"`
		Stream          bool              `json:"stream,omitempty"`
		StreamOptions   *StreamOptions    `json:"stream_options,omitempty"`
		Metadata        map[string]string `json:"metadata,omitempty"`
		Reasoning       *Reasoning        `json:"reasoning,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	var input any
	trimmed := bytes.TrimSpace(raw.Input)
	if len(trimmed) != 0 && !bytes.Equal(trimmed, []byte("null")) {
		if err := json.Unmarshal(trimmed, &input); err != nil {
			return err
		}
	}

	r.Model = raw.Model
	r.Provider = raw.Provider
	r.Input = input
	r.Instructions = raw.Instructions
	r.Tools = raw.Tools
	r.Temperature = raw.Temperature
	r.MaxOutputTokens = raw.MaxOutputTokens
	r.Stream = raw.Stream
	r.StreamOptions = raw.StreamOptions
	r.Metadata = raw.Metadata
	r.Reasoning = raw.Reasoning
	return nil
}

// MarshalJSON preserves dynamic input payloads while supporting Swagger-only schema fields.
func (r ResponsesRequest) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Model           string            `json:"model"`
		Provider        string            `json:"provider,omitempty"`
		Input           any               `json:"input"`
		Instructions    string            `json:"instructions,omitempty"`
		Tools           []map[string]any  `json:"tools,omitempty"`
		Temperature     *float64          `json:"temperature,omitempty"`
		MaxOutputTokens *int              `json:"max_output_tokens,omitempty"`
		Stream          bool              `json:"stream,omitempty"`
		StreamOptions   *StreamOptions    `json:"stream_options,omitempty"`
		Metadata        map[string]string `json:"metadata,omitempty"`
		Reasoning       *Reasoning        `json:"reasoning,omitempty"`
	}{
		Model:           r.Model,
		Provider:        r.Provider,
		Input:           r.Input,
		Instructions:    r.Instructions,
		Tools:           r.Tools,
		Temperature:     r.Temperature,
		MaxOutputTokens: r.MaxOutputTokens,
		Stream:          r.Stream,
		StreamOptions:   r.StreamOptions,
		Metadata:        r.Metadata,
		Reasoning:       r.Reasoning,
	})
}
