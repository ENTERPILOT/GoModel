package core

import "github.com/goccy/go-json"

// The decoders as they were before the chat types decoded their nested
// members in place: every level goes through json.Unmarshal with typed
// fields, so the decoder matches keys, picks duplicates and orders errors.
// The parity tests decode every input with both and require the same result.
// Each ref type mirrors the fields of the type it stands for; the parity tests
// check that the mirrors have not drifted.

type refMessage struct{ v Message }

func (m *refMessage) UnmarshalJSON(data []byte) error {
	// The struct the message decoder used before content was read in place.
	var raw messageJSONFields
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	extraFields, err := extractUnknownJSONFields(data, "role", "content", "tool_calls", "tool_call_id")
	if err != nil {
		return err
	}
	content, err := UnmarshalMessageContent(raw.Content)
	if err != nil {
		return err
	}
	m.v.Role = raw.Role
	m.v.Content = content
	m.v.ToolCalls = raw.ToolCalls
	m.v.ToolCallID = raw.ToolCallID
	m.v.ContentNull = content == nil
	m.v.ExtraFields = extraFields
	return nil
}

type refResponseMessage struct{ v ResponseMessage }

func (m *refResponseMessage) UnmarshalJSON(data []byte) error {
	var raw responseMessageJSONFields
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	extraFields, err := extractUnknownJSONFields(data, "role", "content", "tool_calls")
	if err != nil {
		return err
	}
	content, err := UnmarshalMessageContent(raw.Content)
	if err != nil {
		return err
	}
	m.v.Role = raw.Role
	m.v.Content = content
	m.v.ToolCalls = raw.ToolCalls
	m.v.ExtraFields = extraFields
	return nil
}

// refChoiceFields mirrors Choice with the reference message decoder.
type refChoiceFields struct {
	Message      refResponseMessage `json:"message"`
	FinishReason string             `json:"finish_reason"`
	Index        int                `json:"index"`
	Logprobs     json.RawMessage    `json:"logprobs,omitempty"`
	StopSequence string             `json:"stop_sequence,omitempty"`
}

type refChoice struct{ v Choice }

func (c *refChoice) UnmarshalJSON(data []byte) error {
	type alias refChoiceFields // named as the decoder it mirrors, for its error text
	var raw alias
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	extraFields, err := extractUnknownResponseFields(data, choiceFields)
	if err != nil {
		return err
	}
	c.v = Choice{
		Message:      raw.Message.v,
		FinishReason: raw.FinishReason,
		Index:        raw.Index,
		Logprobs:     raw.Logprobs,
		StopSequence: raw.StopSequence,
		ExtraFields:  extraFields,
	}
	return nil
}

// refChatResponseFields mirrors ChatResponse with the reference choice decoder.
type refChatResponseFields struct {
	ID                string      `json:"id"`
	Object            string      `json:"object"`
	Model             string      `json:"model"`
	Provider          string      `json:"provider"`
	SystemFingerprint string      `json:"system_fingerprint,omitempty"`
	Choices           []refChoice `json:"choices"`
	Usage             Usage       `json:"usage"`
	Created           int64       `json:"created"`
}

type refChatResponse struct{ v ChatResponse }

func (r *refChatResponse) UnmarshalJSON(data []byte) error {
	type alias refChatResponseFields
	var raw alias
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	extraFields, err := extractUnknownResponseFields(data, chatResponseFields)
	if err != nil {
		return err
	}
	var choices []Choice
	if raw.Choices != nil {
		choices = make([]Choice, len(raw.Choices))
		for i := range raw.Choices {
			choices[i] = raw.Choices[i].v
		}
	}
	r.v = ChatResponse{
		ID:                raw.ID,
		Object:            raw.Object,
		Model:             raw.Model,
		Provider:          raw.Provider,
		SystemFingerprint: raw.SystemFingerprint,
		Choices:           choices,
		Usage:             raw.Usage,
		Created:           raw.Created,
		ExtraFields:       extraFields,
	}
	return nil
}

// refChatRequestFields mirrors ChatRequest with the reference message decoder.
type refChatRequestFields struct {
	Temperature       *float64         `json:"temperature,omitempty"`
	TopP              *float64         `json:"top_p,omitempty"`
	MaxTokens         *int             `json:"max_tokens,omitempty"`
	Model             string           `json:"model"`
	Provider          string           `json:"provider,omitempty"`
	Messages          []refMessage     `json:"messages"`
	Tools             []map[string]any `json:"tools,omitempty"`
	ToolChoice        any              `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool            `json:"parallel_tool_calls,omitempty"`
	Stream            bool             `json:"stream,omitempty"`
	StreamOptions     *StreamOptions   `json:"stream_options,omitempty"`
	Reasoning         *Reasoning       `json:"reasoning,omitempty"`
	User              string           `json:"user,omitempty"`
	ServiceTier       string           `json:"service_tier,omitempty"`
}

type refChatRequest struct{ v ChatRequest }

func (r *refChatRequest) UnmarshalJSON(data []byte) error {
	type alias refChatRequestFields
	var raw alias
	if err := json.Unmarshal(data, &raw); err != nil {
		return wrapJSONDecodeError(err)
	}
	extraFields, err := extractUnknownJSONFieldsSet(data, chatRequestFields)
	if err != nil {
		return err
	}
	var messages []Message
	if raw.Messages != nil {
		messages = make([]Message, len(raw.Messages))
		for i := range raw.Messages {
			messages[i] = raw.Messages[i].v
		}
	}
	r.v = ChatRequest{
		Temperature:       raw.Temperature,
		TopP:              raw.TopP,
		MaxTokens:         raw.MaxTokens,
		Model:             raw.Model,
		Provider:          raw.Provider,
		Messages:          messages,
		Tools:             raw.Tools,
		ToolChoice:        raw.ToolChoice,
		ParallelToolCalls: raw.ParallelToolCalls,
		Stream:            raw.Stream,
		StreamOptions:     raw.StreamOptions,
		Reasoning:         raw.Reasoning,
		User:              raw.User,
		ServiceTier:       raw.ServiceTier,
		ExtraFields:       extraFields,
	}
	return nil
}
