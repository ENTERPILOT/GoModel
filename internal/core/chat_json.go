package core

import "encoding/json"

func (r *ChatRequest) UnmarshalJSON(data []byte) error {
	var raw struct {
		Temperature       *float64         `json:"temperature,omitempty"`
		MaxTokens         *int             `json:"max_tokens,omitempty"`
		Model             string           `json:"model"`
		Provider          string           `json:"provider,omitempty"`
		Messages          []Message        `json:"messages"`
		Tools             []map[string]any `json:"tools,omitempty"`
		ToolChoice        any              `json:"tool_choice,omitempty"`
		ParallelToolCalls *bool            `json:"parallel_tool_calls,omitempty"`
		Stream            bool             `json:"stream,omitempty"`
		StreamOptions     *StreamOptions   `json:"stream_options,omitempty"`
		Reasoning         *Reasoning       `json:"reasoning,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	extraFields, err := extractUnknownJSONFields(data,
		"temperature",
		"max_tokens",
		"model",
		"provider",
		"messages",
		"tools",
		"tool_choice",
		"parallel_tool_calls",
		"stream",
		"stream_options",
		"reasoning",
	)
	if err != nil {
		return err
	}

	r.Temperature = raw.Temperature
	r.MaxTokens = raw.MaxTokens
	r.Model = raw.Model
	r.Provider = raw.Provider
	r.Messages = raw.Messages
	r.Tools = raw.Tools
	r.ToolChoice = raw.ToolChoice
	r.ParallelToolCalls = raw.ParallelToolCalls
	r.Stream = raw.Stream
	r.StreamOptions = raw.StreamOptions
	r.Reasoning = raw.Reasoning
	r.ExtraFields = extraFields
	return nil
}

func (r ChatRequest) MarshalJSON() ([]byte, error) {
	type chatRequestAlias struct {
		Temperature       *float64         `json:"temperature,omitempty"`
		MaxTokens         *int             `json:"max_tokens,omitempty"`
		Model             string           `json:"model"`
		Provider          string           `json:"provider,omitempty"`
		Messages          []Message        `json:"messages"`
		Tools             []map[string]any `json:"tools,omitempty"`
		ToolChoice        any              `json:"tool_choice,omitempty"`
		ParallelToolCalls *bool            `json:"parallel_tool_calls,omitempty"`
		Stream            bool             `json:"stream,omitempty"`
		StreamOptions     *StreamOptions   `json:"stream_options,omitempty"`
		Reasoning         *Reasoning       `json:"reasoning,omitempty"`
	}

	return marshalWithUnknownJSONFields(chatRequestAlias{
		Temperature:       r.Temperature,
		MaxTokens:         r.MaxTokens,
		Model:             r.Model,
		Provider:          r.Provider,
		Messages:          r.Messages,
		Tools:             r.Tools,
		ToolChoice:        r.ToolChoice,
		ParallelToolCalls: r.ParallelToolCalls,
		Stream:            r.Stream,
		StreamOptions:     r.StreamOptions,
		Reasoning:         r.Reasoning,
	}, r.ExtraFields)
}
