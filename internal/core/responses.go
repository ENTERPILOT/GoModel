package core

import "encoding/json"

// ResponsesRequest represents the request body for the Responses API.
// This is the OpenAI-compatible /v1/responses endpoint. Unknown JSON members
// encountered during unmarshaling are preserved in ExtraFields
// (UnknownJSONFields) and emitted again during marshaling so callers
// can round-trip extensions; Swagger ignores ExtraFields, and typed fields
// should be preferred when available.
type ResponsesRequest struct {
	Model    string `json:"model"`
	Provider string `json:"provider,omitempty"`
	Input    any    `json:"input"` // string or []ResponsesInputElement — see docs for array form
	//nolint:govet // Intentional duplicate json tag for Swagger docs: input is string OR []ResponsesInputElement.
	InputSchema       []ResponsesInputElement `json:"input,omitempty" extensions:"x-oneOf=[{\"type\":\"string\"},{\"type\":\"array\",\"items\":{\"$ref\":\"#/definitions/core.ResponsesInputElement\"}}]"`
	Instructions      string                  `json:"instructions,omitempty"`
	Tools             []map[string]any        `json:"tools,omitempty"`
	ToolChoice        any                     `json:"tool_choice,omitempty"` // string or object
	ParallelToolCalls *bool                   `json:"parallel_tool_calls,omitempty"`
	Temperature       *float64                `json:"temperature,omitempty"`
	MaxOutputTokens   *int                    `json:"max_output_tokens,omitempty"`
	Stream            bool                    `json:"stream,omitempty"`
	StreamOptions     *StreamOptions          `json:"stream_options,omitempty"`
	Metadata          map[string]string       `json:"metadata,omitempty"`
	Reasoning         *Reasoning              `json:"reasoning,omitempty"`
	ExtraFields       UnknownJSONFields       `json:"-" swaggerignore:"true"`
}

func (r *ResponsesRequest) semanticSelector() (string, string) {
	if r == nil {
		return "", ""
	}
	return r.Model, r.Provider
}

// WithStreaming returns a shallow copy of the request with Stream set to true.
// This avoids mutating the caller's request object.
func (r *ResponsesRequest) WithStreaming() *ResponsesRequest {
	cp := *r
	cp.Stream = true
	return &cp
}

// ResponsesInputElement represents a single item in the Responses API input array.
// It is a discriminated union keyed on Type:
//   - "" or "message": a chat-style message with Role and Content
//   - "function_call": a tool invocation with CallID, Name, and Arguments
//   - "function_call_output": a tool result with CallID and Output
//
// Unknown JSON members encountered during unmarshaling are preserved in
// ExtraFields (UnknownJSONFields) and marshaled back out unchanged so
// extensions can round-trip; Swagger ignores ExtraFields, and typed fields
// should be preferred when available.
type ResponsesInputElement struct {
	Type string `json:"type,omitempty"` // "message", "function_call", "function_call_output"

	// Message fields (type="" or "message")
	Role    string `json:"role,omitempty"`
	Status  string `json:"status,omitempty"`
	Content any    `json:"content,omitempty"` // Can be string or []ContentPart
	//nolint:govet // Intentional duplicate json tag for Swagger docs: content is string OR []ContentPart.
	ContentSchema []ContentPart `json:"content,omitempty" extensions:"x-oneOf=[{\"type\":\"string\"},{\"type\":\"array\",\"items\":{\"$ref\":\"#/definitions/core.ContentPart\"}}]"`

	// Function call fields (type="function_call")
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`

	// Function call output fields (type="function_call_output") — CallID shared above
	Output      string            `json:"output,omitempty"`
	ExtraFields UnknownJSONFields `json:"-" swaggerignore:"true"`
}

// ResponsesResponse represents the response from the Responses API.
type ResponsesResponse struct {
	ID        string                `json:"id"`
	Object    string                `json:"object"` // "response"
	CreatedAt int64                 `json:"created_at"`
	Model     string                `json:"model"`
	Provider  string                `json:"provider"`
	Status    string                `json:"status"` // "completed", "failed", "in_progress"
	Output    []ResponsesOutputItem `json:"output"`
	Usage     *ResponsesUsage       `json:"usage,omitempty"`
	Error     *ResponsesError       `json:"error,omitempty"`
}

// ResponsesOutputItem represents an item in the output array.
type ResponsesOutputItem struct {
	ID        string                 `json:"id"`
	Type      string                 `json:"type"` // "message", "function_call", etc.
	Role      string                 `json:"role,omitempty"`
	Status    string                 `json:"status,omitempty"`
	CallID    string                 `json:"call_id,omitempty"`
	Name      string                 `json:"name,omitempty"`
	Arguments string                 `json:"arguments,omitempty"`
	Content   []ResponsesContentItem `json:"content,omitempty"`
}

// ResponsesContentItem represents a content item in the output.
type ResponsesContentItem struct {
	Type       string             `json:"type"` // "output_text", "input_image", "input_audio", etc.
	Text       string             `json:"text,omitempty"`
	ImageURL   *ImageURLContent   `json:"image_url,omitempty"`
	InputAudio *InputAudioContent `json:"input_audio,omitempty"`
	// Providers can return structured annotation objects here (for example
	// citations from native tools), so keep the payload shape liberal.
	Annotations []json.RawMessage `json:"annotations,omitempty"`
}

// ResponsesUsage represents token usage for the Responses API.
type ResponsesUsage struct {
	InputTokens             int                      `json:"input_tokens"`
	OutputTokens            int                      `json:"output_tokens"`
	TotalTokens             int                      `json:"total_tokens"`
	PromptTokensDetails     *PromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
	RawUsage                map[string]any           `json:"raw_usage,omitempty"`
}

// ResponsesError represents an error in the response.
type ResponsesError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
