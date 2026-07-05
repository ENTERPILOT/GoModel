package core

import "github.com/goccy/go-json"

// ResponsesRequest represents the request body for the Responses API.
// This is the OpenAI-compatible /v1/responses endpoint. Unknown JSON members
// encountered during unmarshaling are preserved in ExtraFields
// (UnknownJSONFields) and emitted again during marshaling so callers
// can round-trip extensions; Swagger ignores ExtraFields, and typed fields
// should be preferred when available.
type ResponsesRequest struct {
	Model              string            `json:"model"`
	Provider           string            `json:"provider,omitempty"` // Gateway routing hint; stripped before upstream execution.
	Input              any               `json:"input"`              // string or []ResponsesInputElement — see docs for array form
	Instructions       string            `json:"instructions,omitempty"`
	Tools              []map[string]any  `json:"tools,omitempty"`
	ToolChoice         any               `json:"tool_choice,omitempty"` // string or object
	ParallelToolCalls  *bool             `json:"parallel_tool_calls,omitempty"`
	Temperature        *float64          `json:"temperature,omitempty"`
	TopP               *float64          `json:"top_p,omitempty"`
	TopLogprobs        *int              `json:"top_logprobs,omitempty"`
	MaxOutputTokens    *int              `json:"max_output_tokens,omitempty"`
	Stream             bool              `json:"stream,omitempty"`
	StreamOptions      *StreamOptions    `json:"stream_options,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
	Reasoning          *Reasoning        `json:"reasoning,omitempty"`
	Text               any               `json:"text,omitempty"`
	Include            []string          `json:"include,omitempty"`
	Truncation         string            `json:"truncation,omitempty"`
	Store              *bool             `json:"store,omitempty"`
	PreviousResponseID string            `json:"previous_response_id,omitempty"`
	// Conversation accepts either a conversation ID string or an object with id.
	Conversation         *ResponsesConversationRef `json:"conversation,omitempty"`
	Prompt               any                       `json:"prompt,omitempty"`
	PromptCacheRetention string                    `json:"prompt_cache_retention,omitempty"`
	ContextManagement    any                       `json:"context_management,omitempty"`
	User                 string                    `json:"user,omitempty"`
	ServiceTier          string                    `json:"service_tier,omitempty"`
	SafetyIdentifier     string                    `json:"safety_identifier,omitempty"`
	ExtraFields          UnknownJSONFields         `json:"-" swaggerignore:"true"`
}

// ResponsesConversationRef represents the Responses API conversation request
// field. OpenAI accepts either a conversation ID string or an object with id.
// Raw preserves the original string/object shape across JSON round trips.
type ResponsesConversationRef struct {
	ID  string          `json:"id,omitempty"`
	Raw json.RawMessage `json:"-" swaggerignore:"true"`
}

// ResponseInputTokensRequest documents the request body accepted by
// POST /v1/responses/input_tokens.
type ResponseInputTokensRequest struct {
	Model              string            `json:"model,omitempty"`
	Provider           string            `json:"provider,omitempty"` // Gateway routing hint; stripped before upstream execution.
	Input              any               `json:"input,omitempty"`    // string or []ResponsesInputElement — see docs for array form
	Instructions       string            `json:"instructions,omitempty"`
	Tools              []map[string]any  `json:"tools,omitempty"`
	ToolChoice         any               `json:"tool_choice,omitempty"` // string or object
	ParallelToolCalls  *bool             `json:"parallel_tool_calls,omitempty"`
	Temperature        *float64          `json:"temperature,omitempty"`
	TopP               *float64          `json:"top_p,omitempty"`
	TopLogprobs        *int              `json:"top_logprobs,omitempty"`
	MaxOutputTokens    *int              `json:"max_output_tokens,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
	Reasoning          *Reasoning        `json:"reasoning,omitempty"`
	Text               any               `json:"text,omitempty"`
	Include            []string          `json:"include,omitempty"`
	Truncation         string            `json:"truncation,omitempty"`
	Store              *bool             `json:"store,omitempty"`
	PreviousResponseID string            `json:"previous_response_id,omitempty"`
	// Conversation accepts either a conversation ID string or an object with id.
	Conversation         *ResponsesConversationRef `json:"conversation,omitempty"`
	Prompt               any                       `json:"prompt,omitempty"`
	PromptCacheRetention string                    `json:"prompt_cache_retention,omitempty"`
	ContextManagement    any                       `json:"context_management,omitempty"`
	User                 string                    `json:"user,omitempty"`
	ServiceTier          string                    `json:"service_tier,omitempty"`
	SafetyIdentifier     string                    `json:"safety_identifier,omitempty"`
	ExtraFields          UnknownJSONFields         `json:"-" swaggerignore:"true"`
}

// ResponseCompactRequest documents the request body accepted by
// POST /v1/responses/compact. It accepts exactly the same members as
// ResponseInputTokensRequest, so it is defined from that struct: the field
// set is written once and both utility endpoints stay in lockstep.
type ResponseCompactRequest ResponseInputTokensRequest

// InputTokensRequest reduces a full Responses request to the field set shared
// with the utility endpoints (the streaming controls are dropped — utility
// endpoints never stream). ExtraFields are cloned. The reduction is defined
// here, next to the types, so the field knowledge stays in one file; a guard
// test asserts it covers every non-streaming ResponsesRequest field.
func (r *ResponsesRequest) InputTokensRequest() *ResponseInputTokensRequest {
	if r == nil {
		return nil
	}
	return &ResponseInputTokensRequest{
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
		ExtraFields:          CloneUnknownJSONFields(r.ExtraFields),
	}
}

// CompactRequest reduces a full Responses request for the compact endpoint;
// see InputTokensRequest.
func (r *ResponsesRequest) CompactRequest() *ResponseCompactRequest {
	utility := r.InputTokensRequest()
	if utility == nil {
		return nil
	}
	compact := ResponseCompactRequest(*utility)
	return &compact
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
	Raw         json.RawMessage   `json:"-" swaggerignore:"true"`
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
	Annotations []json.RawMessage `json:"annotations,omitempty" swaggertype:"array,object"`
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

// ResponseRetrieveParams contains query parameters accepted by
// GET /v1/responses/{id}.
type ResponseRetrieveParams struct {
	Include            []string
	IncludeObfuscation *bool
	StartingAfter      *int
	Stream             bool
}

// ResponseInputItemsParams contains query parameters accepted by
// GET /v1/responses/{id}/input_items.
type ResponseInputItemsParams struct {
	After   string
	Include []string
	Limit   int
	Order   string
}

// ResponseInputItemListResponse is returned by
// GET /v1/responses/{id}/input_items.
type ResponseInputItemListResponse struct {
	Object  string            `json:"object"`
	Data    []json.RawMessage `json:"data" swaggertype:"array,object"`
	FirstID string            `json:"first_id,omitempty"`
	LastID  string            `json:"last_id,omitempty"`
	HasMore bool              `json:"has_more"`
}

// ResponseInputTokensResponse is returned by POST /v1/responses/input_tokens.
type ResponseInputTokensResponse struct {
	Object      string `json:"object"`
	InputTokens int    `json:"input_tokens"`
}

// ResponseCompactResponse is returned by POST /v1/responses/compact.
type ResponseCompactResponse struct {
	ID        string                `json:"id"`
	Object    string                `json:"object"`
	CreatedAt int64                 `json:"created_at"`
	Output    []ResponsesOutputItem `json:"output"`
	Usage     *ResponsesUsage       `json:"usage,omitempty"`
	Error     *ResponsesError       `json:"error,omitempty"`
	Metadata  map[string]string     `json:"metadata,omitempty"`
	Provider  string                `json:"provider,omitempty"`
}

// ResponseDeleteResponse is returned by DELETE /v1/responses/{id}.
type ResponseDeleteResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Deleted bool   `json:"deleted"`
}
