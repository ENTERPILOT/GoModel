package core

import "github.com/goccy/go-json"

func (r *ChatRequest) UnmarshalJSON(data []byte) error {
	var raw struct {
		Temperature       *float64         `json:"temperature,omitempty"`
		TopP              *float64         `json:"top_p,omitempty"`
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
		User              string           `json:"user,omitempty"`
		ServiceTier       string           `json:"service_tier,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	extraFields, err := extractUnknownJSONFields(data,
		"temperature",
		"top_p",
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
		"user",
		"service_tier",
	)
	if err != nil {
		return err
	}

	r.Temperature = raw.Temperature
	r.TopP = raw.TopP
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
	r.User = raw.User
	r.ServiceTier = raw.ServiceTier
	r.ExtraFields = extraFields
	return nil
}

func (r ChatRequest) MarshalJSON() ([]byte, error) {
	// alias inherits every field (and json tag) from ChatRequest but drops the
	// MarshalJSON method, so json.Marshal uses default struct encoding without
	// recursing. ExtraFields is json:"-", so it is skipped here and merged in
	// separately. Adding a typed field to ChatRequest therefore round-trips
	// automatically instead of being silently dropped.
	type alias ChatRequest
	return marshalWithUnknownJSONFields(alias(r), r.ExtraFields)
}
