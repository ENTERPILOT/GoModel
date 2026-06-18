package core

import (
	"bytes"
	"fmt"

	"github.com/goccy/go-json"
)

// responsesUtilityRequestFields lists the JSON fields recognized on responses
// utility requests; any other field is preserved as an unknown extra field.
var responsesUtilityRequestFields = []string{
	"model",
	"provider",
	"input",
	"instructions",
	"tools",
	"tool_choice",
	"parallel_tool_calls",
	"temperature",
	"top_p",
	"top_logprobs",
	"max_output_tokens",
	"metadata",
	"reasoning",
	"text",
	"include",
	"truncation",
	"store",
	"previous_response_id",
	"conversation",
	"prompt",
	"prompt_cache_retention",
	"context_management",
	"user",
	"service_tier",
	"safety_identifier",
}

// responsesRequestFields additionally recognizes the streaming controls that
// only apply to full responses requests.
var responsesRequestFields = append([]string{"stream", "stream_options"}, responsesUtilityRequestFields...)

// UnmarshalJSON preserves dynamic input payloads while supporting Swagger-only schema fields.
// Array inputs are deserialized as []ResponsesInputElement for type-safe downstream handling.
func (r *ResponsesRequest) UnmarshalJSON(data []byte) error {
	var raw struct {
		Model                string                    `json:"model"`
		Provider             string                    `json:"provider,omitempty"`
		Input                json.RawMessage           `json:"input"`
		Instructions         string                    `json:"instructions,omitempty"`
		Tools                []map[string]any          `json:"tools,omitempty"`
		ToolChoice           any                       `json:"tool_choice,omitempty"`
		ParallelToolCalls    *bool                     `json:"parallel_tool_calls,omitempty"`
		Temperature          *float64                  `json:"temperature,omitempty"`
		TopP                 *float64                  `json:"top_p,omitempty"`
		TopLogprobs          *int                      `json:"top_logprobs,omitempty"`
		MaxOutputTokens      *int                      `json:"max_output_tokens,omitempty"`
		Stream               bool                      `json:"stream,omitempty"`
		StreamOptions        *StreamOptions            `json:"stream_options,omitempty"`
		Metadata             map[string]string         `json:"metadata,omitempty"`
		Reasoning            *Reasoning                `json:"reasoning,omitempty"`
		Text                 any                       `json:"text,omitempty"`
		Include              []string                  `json:"include,omitempty"`
		Truncation           string                    `json:"truncation,omitempty"`
		Store                *bool                     `json:"store,omitempty"`
		PreviousResponseID   string                    `json:"previous_response_id,omitempty"`
		Conversation         *ResponsesConversationRef `json:"conversation,omitempty"`
		Prompt               any                       `json:"prompt,omitempty"`
		PromptCacheRetention string                    `json:"prompt_cache_retention,omitempty"`
		ContextManagement    any                       `json:"context_management,omitempty"`
		User                 string                    `json:"user,omitempty"`
		ServiceTier          string                    `json:"service_tier,omitempty"`
		SafetyIdentifier     string                    `json:"safety_identifier,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	extraFields, err := extractUnknownJSONFields(data, responsesRequestFields...)
	if err != nil {
		return err
	}

	input, err := decodeResponsesInput(raw.Input)
	if err != nil {
		return err
	}

	r.Model = raw.Model
	r.Provider = raw.Provider
	r.Input = input
	r.Instructions = raw.Instructions
	r.Tools = raw.Tools
	r.ToolChoice = raw.ToolChoice
	r.ParallelToolCalls = raw.ParallelToolCalls
	r.Temperature = raw.Temperature
	r.TopP = raw.TopP
	r.TopLogprobs = raw.TopLogprobs
	r.MaxOutputTokens = raw.MaxOutputTokens
	r.Stream = raw.Stream
	r.StreamOptions = raw.StreamOptions
	r.Metadata = raw.Metadata
	r.Reasoning = raw.Reasoning
	r.Text = raw.Text
	r.Include = raw.Include
	r.Truncation = raw.Truncation
	r.Store = raw.Store
	r.PreviousResponseID = raw.PreviousResponseID
	r.Conversation = raw.Conversation
	r.Prompt = raw.Prompt
	r.PromptCacheRetention = raw.PromptCacheRetention
	r.ContextManagement = raw.ContextManagement
	r.User = raw.User
	r.ServiceTier = raw.ServiceTier
	r.SafetyIdentifier = raw.SafetyIdentifier
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
func (r ResponsesRequest) MarshalJSON() ([]byte, error) {
	return marshalWithUnknownJSONFields(struct {
		Model                string                    `json:"model"`
		Provider             string                    `json:"provider,omitempty"`
		Input                any                       `json:"input"`
		Instructions         string                    `json:"instructions,omitempty"`
		Tools                []map[string]any          `json:"tools,omitempty"`
		ToolChoice           any                       `json:"tool_choice,omitempty"`
		ParallelToolCalls    *bool                     `json:"parallel_tool_calls,omitempty"`
		Temperature          *float64                  `json:"temperature,omitempty"`
		TopP                 *float64                  `json:"top_p,omitempty"`
		TopLogprobs          *int                      `json:"top_logprobs,omitempty"`
		MaxOutputTokens      *int                      `json:"max_output_tokens,omitempty"`
		Stream               bool                      `json:"stream,omitempty"`
		StreamOptions        *StreamOptions            `json:"stream_options,omitempty"`
		Metadata             map[string]string         `json:"metadata,omitempty"`
		Reasoning            *Reasoning                `json:"reasoning,omitempty"`
		Text                 any                       `json:"text,omitempty"`
		Include              []string                  `json:"include,omitempty"`
		Truncation           string                    `json:"truncation,omitempty"`
		Store                *bool                     `json:"store,omitempty"`
		PreviousResponseID   string                    `json:"previous_response_id,omitempty"`
		Conversation         *ResponsesConversationRef `json:"conversation,omitempty"`
		Prompt               any                       `json:"prompt,omitempty"`
		PromptCacheRetention string                    `json:"prompt_cache_retention,omitempty"`
		ContextManagement    any                       `json:"context_management,omitempty"`
		User                 string                    `json:"user,omitempty"`
		ServiceTier          string                    `json:"service_tier,omitempty"`
		SafetyIdentifier     string                    `json:"safety_identifier,omitempty"`
	}{
		Model:                r.Model,
		Provider:             r.Provider,
		Input:                r.Input,
		Instructions:         r.Instructions,
		Tools:                r.Tools,
		ToolChoice:           r.ToolChoice,
		ParallelToolCalls:    r.ParallelToolCalls,
		Temperature:          r.Temperature,
		TopP:                 r.TopP,
		TopLogprobs:          r.TopLogprobs,
		MaxOutputTokens:      r.MaxOutputTokens,
		Stream:               r.Stream,
		StreamOptions:        r.StreamOptions,
		Metadata:             r.Metadata,
		Reasoning:            r.Reasoning,
		Text:                 r.Text,
		Include:              r.Include,
		Truncation:           r.Truncation,
		Store:                r.Store,
		PreviousResponseID:   r.PreviousResponseID,
		Conversation:         r.Conversation,
		Prompt:               r.Prompt,
		PromptCacheRetention: r.PromptCacheRetention,
		ContextManagement:    r.ContextManagement,
		User:                 r.User,
		ServiceTier:          r.ServiceTier,
		SafetyIdentifier:     r.SafetyIdentifier,
	}, r.ExtraFields)
}

type responseUtilityRequestJSON struct {
	Model                string
	Provider             string
	Input                any
	Instructions         string
	Tools                []map[string]any
	ToolChoice           any
	ParallelToolCalls    *bool
	Temperature          *float64
	TopP                 *float64
	TopLogprobs          *int
	MaxOutputTokens      *int
	Metadata             map[string]string
	Reasoning            *Reasoning
	Text                 any
	Include              []string
	Truncation           string
	Store                *bool
	PreviousResponseID   string
	Conversation         *ResponsesConversationRef
	Prompt               any
	PromptCacheRetention string
	ContextManagement    any
	User                 string
	ServiceTier          string
	SafetyIdentifier     string
	ExtraFields          UnknownJSONFields
}

func decodeResponseUtilityRequestJSON(data []byte) (responseUtilityRequestJSON, error) {
	var raw struct {
		Model                string                    `json:"model,omitempty"`
		Provider             string                    `json:"provider,omitempty"`
		Input                json.RawMessage           `json:"input,omitempty"`
		Instructions         string                    `json:"instructions,omitempty"`
		Tools                []map[string]any          `json:"tools,omitempty"`
		ToolChoice           any                       `json:"tool_choice,omitempty"`
		ParallelToolCalls    *bool                     `json:"parallel_tool_calls,omitempty"`
		Temperature          *float64                  `json:"temperature,omitempty"`
		TopP                 *float64                  `json:"top_p,omitempty"`
		TopLogprobs          *int                      `json:"top_logprobs,omitempty"`
		MaxOutputTokens      *int                      `json:"max_output_tokens,omitempty"`
		Metadata             map[string]string         `json:"metadata,omitempty"`
		Reasoning            *Reasoning                `json:"reasoning,omitempty"`
		Text                 any                       `json:"text,omitempty"`
		Include              []string                  `json:"include,omitempty"`
		Truncation           string                    `json:"truncation,omitempty"`
		Store                *bool                     `json:"store,omitempty"`
		PreviousResponseID   string                    `json:"previous_response_id,omitempty"`
		Conversation         *ResponsesConversationRef `json:"conversation,omitempty"`
		Prompt               any                       `json:"prompt,omitempty"`
		PromptCacheRetention string                    `json:"prompt_cache_retention,omitempty"`
		ContextManagement    any                       `json:"context_management,omitempty"`
		User                 string                    `json:"user,omitempty"`
		ServiceTier          string                    `json:"service_tier,omitempty"`
		SafetyIdentifier     string                    `json:"safety_identifier,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return responseUtilityRequestJSON{}, err
	}

	extraFields, err := extractUnknownJSONFields(data, responsesUtilityRequestFields...)
	if err != nil {
		return responseUtilityRequestJSON{}, err
	}

	input, err := decodeResponsesInput(raw.Input)
	if err != nil {
		return responseUtilityRequestJSON{}, err
	}
	return responseUtilityRequestJSON{
		Model:                raw.Model,
		Provider:             raw.Provider,
		Input:                input,
		Instructions:         raw.Instructions,
		Tools:                raw.Tools,
		ToolChoice:           raw.ToolChoice,
		ParallelToolCalls:    raw.ParallelToolCalls,
		Temperature:          raw.Temperature,
		TopP:                 raw.TopP,
		TopLogprobs:          raw.TopLogprobs,
		MaxOutputTokens:      raw.MaxOutputTokens,
		Metadata:             raw.Metadata,
		Reasoning:            raw.Reasoning,
		Text:                 raw.Text,
		Include:              raw.Include,
		Truncation:           raw.Truncation,
		Store:                raw.Store,
		PreviousResponseID:   raw.PreviousResponseID,
		Conversation:         raw.Conversation,
		Prompt:               raw.Prompt,
		PromptCacheRetention: raw.PromptCacheRetention,
		ContextManagement:    raw.ContextManagement,
		User:                 raw.User,
		ServiceTier:          raw.ServiceTier,
		SafetyIdentifier:     raw.SafetyIdentifier,
		ExtraFields:          extraFields,
	}, nil
}

func marshalResponseUtilityRequestJSON(raw responseUtilityRequestJSON) ([]byte, error) {
	return marshalWithUnknownJSONFields(struct {
		Model                string                    `json:"model,omitempty"`
		Provider             string                    `json:"provider,omitempty"`
		Input                any                       `json:"input,omitempty"`
		Instructions         string                    `json:"instructions,omitempty"`
		Tools                []map[string]any          `json:"tools,omitempty"`
		ToolChoice           any                       `json:"tool_choice,omitempty"`
		ParallelToolCalls    *bool                     `json:"parallel_tool_calls,omitempty"`
		Temperature          *float64                  `json:"temperature,omitempty"`
		TopP                 *float64                  `json:"top_p,omitempty"`
		TopLogprobs          *int                      `json:"top_logprobs,omitempty"`
		MaxOutputTokens      *int                      `json:"max_output_tokens,omitempty"`
		Metadata             map[string]string         `json:"metadata,omitempty"`
		Reasoning            *Reasoning                `json:"reasoning,omitempty"`
		Text                 any                       `json:"text,omitempty"`
		Include              []string                  `json:"include,omitempty"`
		Truncation           string                    `json:"truncation,omitempty"`
		Store                *bool                     `json:"store,omitempty"`
		PreviousResponseID   string                    `json:"previous_response_id,omitempty"`
		Conversation         *ResponsesConversationRef `json:"conversation,omitempty"`
		Prompt               any                       `json:"prompt,omitempty"`
		PromptCacheRetention string                    `json:"prompt_cache_retention,omitempty"`
		ContextManagement    any                       `json:"context_management,omitempty"`
		User                 string                    `json:"user,omitempty"`
		ServiceTier          string                    `json:"service_tier,omitempty"`
		SafetyIdentifier     string                    `json:"safety_identifier,omitempty"`
	}{
		Model:                raw.Model,
		Provider:             raw.Provider,
		Input:                raw.Input,
		Instructions:         raw.Instructions,
		Tools:                raw.Tools,
		ToolChoice:           raw.ToolChoice,
		ParallelToolCalls:    raw.ParallelToolCalls,
		Temperature:          raw.Temperature,
		TopP:                 raw.TopP,
		TopLogprobs:          raw.TopLogprobs,
		MaxOutputTokens:      raw.MaxOutputTokens,
		Metadata:             raw.Metadata,
		Reasoning:            raw.Reasoning,
		Text:                 raw.Text,
		Include:              raw.Include,
		Truncation:           raw.Truncation,
		Store:                raw.Store,
		PreviousResponseID:   raw.PreviousResponseID,
		Conversation:         raw.Conversation,
		Prompt:               raw.Prompt,
		PromptCacheRetention: raw.PromptCacheRetention,
		ContextManagement:    raw.ContextManagement,
		User:                 raw.User,
		ServiceTier:          raw.ServiceTier,
		SafetyIdentifier:     raw.SafetyIdentifier,
	}, raw.ExtraFields)
}

// UnmarshalJSON preserves the dynamic input payload for gateway utility requests.
func (r *ResponseInputTokensRequest) UnmarshalJSON(data []byte) error {
	raw, err := decodeResponseUtilityRequestJSON(data)
	if err != nil {
		return err
	}
	*r = ResponseInputTokensRequest(raw)
	return nil
}

// MarshalJSON preserves the dynamic input payload while omitting Swagger-only schema fields.
func (r ResponseInputTokensRequest) MarshalJSON() ([]byte, error) {
	return marshalResponseUtilityRequestJSON(responseUtilityRequestJSON(r))
}

// UnmarshalJSON preserves the dynamic input payload for gateway utility requests.
func (r *ResponseCompactRequest) UnmarshalJSON(data []byte) error {
	raw, err := decodeResponseUtilityRequestJSON(data)
	if err != nil {
		return err
	}
	*r = ResponseCompactRequest(raw)
	return nil
}

// MarshalJSON preserves the dynamic input payload while omitting Swagger-only schema fields.
func (r ResponseCompactRequest) MarshalJSON() ([]byte, error) {
	return marshalResponseUtilityRequestJSON(responseUtilityRequestJSON(r))
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
