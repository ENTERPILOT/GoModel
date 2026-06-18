// Package anthropicapi translates the Anthropic Messages API dialect to and from
// GoModel's canonical chat types. It is an ingress concern: it accepts the
// Anthropic wire format from clients and renders responses back in that format,
// independent of which provider ultimately serves the request.
package anthropicapi

import "github.com/goccy/go-json"

// MessagesRequest is the Anthropic Messages API request body.
// System and message content fields are polymorphic on the wire (string or
// array), so they are kept raw and decoded in request.go.
type MessagesRequest struct {
	Model         string          `json:"model"`
	Messages      []Message       `json:"messages"`
	MaxTokens     int             `json:"max_tokens"`
	System        json.RawMessage `json:"system,omitempty"`
	Metadata      *Metadata       `json:"metadata,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	TopK          *int            `json:"top_k,omitempty"`
	Tools         []Tool          `json:"tools,omitempty"`
	ToolChoice    *ToolChoice     `json:"tool_choice,omitempty"`
	Thinking      *Thinking       `json:"thinking,omitempty"`
}

// Metadata carries the optional Anthropic request metadata object.
type Metadata struct {
	UserID string `json:"user_id,omitempty"`
}

// Message is a single Anthropic conversation message. Content is a string or an
// array of ContentBlock values.
type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// ContentBlock is one element of an Anthropic message content array. The struct
// is a union; only the fields relevant to Type are populated.
type ContentBlock struct {
	Type string `json:"type"`
	// text / thinking
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`
	// image
	Source *Source `json:"source,omitempty"`
	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// Source describes an Anthropic image source (base64 inline data or a URL).
type Source struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// Tool is an Anthropic tool definition. A custom tool has no Type (or Type
// "custom"); a server/built-in tool (web search, code execution, …) carries a
// versioned Type and is rejected during translation — see convertTools.
type Tool struct {
	Type        string          `json:"type,omitempty"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// ToolChoice constrains how the model selects tools.
type ToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse *bool  `json:"disable_parallel_tool_use,omitempty"`
}

// Thinking is the Anthropic extended-thinking configuration.
type Thinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

// MessagesResponse is the Anthropic Messages API response body.
type MessagesResponse struct {
	ID           string                 `json:"id"`
	Type         string                 `json:"type"`
	Role         string                 `json:"role"`
	Model        string                 `json:"model"`
	Content      []ResponseContentBlock `json:"content"`
	StopReason   string                 `json:"stop_reason"`
	StopSequence *string                `json:"stop_sequence"`
	Usage        Usage                  `json:"usage"`
}

// ResponseContentBlock is one element of an Anthropic response content array.
type ResponseContentBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	Thinking string          `json:"thinking,omitempty"`
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
}

// Usage reports Anthropic-style token usage.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// CountTokensResponse is the Anthropic /v1/messages/count_tokens response body.
type CountTokensResponse struct {
	InputTokens int `json:"input_tokens"`
}

// ErrorResponse is the Anthropic error envelope.
type ErrorResponse struct {
	Type  string      `json:"type"`
	Error ErrorObject `json:"error"`
}

// ErrorObject is the inner error detail of an Anthropic error envelope.
type ErrorObject struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
