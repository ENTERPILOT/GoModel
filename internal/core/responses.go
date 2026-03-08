package core

// ResponsesRequest represents the request body for the Responses API.
// This is the OpenAI-compatible /v1/responses endpoint.
type ResponsesRequest struct {
	Model    string      `json:"model"`
	Provider string      `json:"provider,omitempty"`
	Input    interface{} `json:"input"` // string or []ResponsesInputItem — see docs for array form
	//nolint:govet // Intentional duplicate json tag for Swagger docs: input is string OR []ResponsesInputItem.
	InputSchema       []ResponsesInputItem `json:"input,omitempty" extensions:"x-oneOf=[{\"type\":\"string\"},{\"type\":\"array\",\"items\":{\"$ref\":\"#/definitions/core.ResponsesInputItem\"}}]"`
	Instructions      string               `json:"instructions,omitempty"`
	Tools             []map[string]any     `json:"tools,omitempty"`
	ToolChoice        any                  `json:"tool_choice,omitempty"` // string or object
	ParallelToolCalls *bool                `json:"parallel_tool_calls,omitempty"`
	Temperature       *float64             `json:"temperature,omitempty"`
	MaxOutputTokens   *int                 `json:"max_output_tokens,omitempty"`
	Stream            bool                 `json:"stream,omitempty"`
	StreamOptions     *StreamOptions       `json:"stream_options,omitempty"`
	Metadata          map[string]string    `json:"metadata,omitempty"`
	Reasoning         *Reasoning           `json:"reasoning,omitempty"`
}

// WithStreaming returns a shallow copy of the request with Stream set to true.
// This avoids mutating the caller's request object.
func (r *ResponsesRequest) WithStreaming() *ResponsesRequest {
	cp := *r
	cp.Stream = true
	return &cp
}

// ResponsesInputItem represents an input item when Input is an array.
type ResponsesInputItem struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // Can be string or []ContentPart
	//nolint:govet // Intentional duplicate json tag for Swagger docs: content is string OR []ContentPart.
	ContentSchema []ContentPart `json:"content,omitempty" extensions:"x-oneOf=[{\"type\":\"string\"},{\"type\":\"array\",\"items\":{\"$ref\":\"#/definitions/core.ContentPart\"}}]"`
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
	Type        string             `json:"type"` // "output_text", "input_image", "input_audio", etc.
	Text        string             `json:"text,omitempty"`
	ImageURL    *ImageURLContent   `json:"image_url,omitempty"`
	InputAudio  *InputAudioContent `json:"input_audio,omitempty"`
	Annotations []string           `json:"annotations,omitempty"`
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
