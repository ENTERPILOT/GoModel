package cohere

import "github.com/goccy/go-json"

type chatRequest struct {
	Model            string           `json:"model"`
	Messages         []chatMessage    `json:"messages"`
	Tools            []map[string]any `json:"tools,omitempty"`
	StrictTools      json.RawMessage  `json:"strict_tools,omitempty"`
	Documents        json.RawMessage  `json:"documents,omitempty"`
	CitationOptions  json.RawMessage  `json:"citation_options,omitempty"`
	ResponseFormat   json.RawMessage  `json:"response_format,omitempty"`
	SafetyMode       json.RawMessage  `json:"safety_mode,omitempty"`
	MaxTokens        *int             `json:"max_tokens,omitempty"`
	StopSequences    json.RawMessage  `json:"stop_sequences,omitempty"`
	Temperature      *float64         `json:"temperature,omitempty"`
	Seed             json.RawMessage  `json:"seed,omitempty"`
	FrequencyPenalty json.RawMessage  `json:"frequency_penalty,omitempty"`
	PresencePenalty  json.RawMessage  `json:"presence_penalty,omitempty"`
	K                json.RawMessage  `json:"k,omitempty"`
	P                *float64         `json:"p,omitempty"`
	Logprobs         json.RawMessage  `json:"logprobs,omitempty"`
	ToolChoice       string           `json:"tool_choice,omitempty"`
	Thinking         json.RawMessage  `json:"thinking,omitempty"`
	Priority         json.RawMessage  `json:"priority,omitempty"`
	Stream           bool             `json:"stream"`
}

type chatMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type contentPart struct {
	Type     string             `json:"type"`
	Text     string             `json:"text,omitempty"`
	Thinking string             `json:"thinking,omitempty"`
	ImageURL *imageURLContainer `json:"image_url,omitempty"`
}

type imageURLContainer struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatResponse struct {
	ID           string           `json:"id"`
	FinishReason string           `json:"finish_reason"`
	Message      assistantMessage `json:"message"`
	Usage        usage            `json:"usage"`
	Logprobs     json.RawMessage  `json:"logprobs,omitempty"`
}

type assistantMessage struct {
	Role      string          `json:"role"`
	Content   []contentPart   `json:"content,omitempty"`
	ToolCalls []toolCall      `json:"tool_calls,omitempty"`
	ToolPlan  string          `json:"tool_plan,omitempty"`
	Citations json.RawMessage `json:"citations,omitempty"`
}

type usage struct {
	BilledUnits  billedUnits `json:"billed_units"`
	Tokens       tokenUsage  `json:"tokens"`
	CachedTokens float64     `json:"cached_tokens,omitempty"`
}

type billedUnits struct {
	InputTokens     float64 `json:"input_tokens,omitempty"`
	OutputTokens    float64 `json:"output_tokens,omitempty"`
	SearchUnits     float64 `json:"search_units,omitempty"`
	Classifications float64 `json:"classifications,omitempty"`
}

type tokenUsage struct {
	InputTokens  float64 `json:"input_tokens,omitempty"`
	OutputTokens float64 `json:"output_tokens,omitempty"`
}

type modelsResponse struct {
	Models []modelInfo `json:"models"`
}

type modelInfo struct {
	Name          string   `json:"name"`
	Endpoints     []string `json:"endpoints,omitempty"`
	ContextLength float64  `json:"context_length,omitempty"`
}

type embedRequest struct {
	Texts           []string        `json:"texts"`
	Model           string          `json:"model"`
	InputType       string          `json:"input_type"`
	EmbeddingTypes  []string        `json:"embedding_types"`
	OutputDimension *int            `json:"output_dimension,omitempty"`
	MaxTokens       json.RawMessage `json:"max_tokens,omitempty"`
	Truncate        json.RawMessage `json:"truncate,omitempty"`
	Priority        json.RawMessage `json:"priority,omitempty"`
}

type embedResponse struct {
	ID           string       `json:"id"`
	ResponseType string       `json:"response_type,omitempty"`
	Embeddings   embedVectors `json:"embeddings"`
	Meta         embedMeta    `json:"meta"`
}

type embedVectors struct {
	Float  [][]float64 `json:"float,omitempty"`
	Base64 []string    `json:"base64,omitempty"`
}

type embedMeta struct {
	BilledUnits billedUnits `json:"billed_units"`
}

type streamEvent struct {
	Type  string           `json:"type"`
	ID    string           `json:"id,omitempty"`
	Index int              `json:"index,omitempty"`
	Delta streamEventDelta `json:"delta"`
}

type streamEventDelta struct {
	Message      streamDeltaMessage `json:"message"`
	FinishReason string             `json:"finish_reason,omitempty"`
	Usage        usage              `json:"usage"`
	Error        string             `json:"error,omitempty"`
}

type streamDeltaMessage struct {
	Role      string             `json:"role,omitempty"`
	Content   streamDeltaContent `json:"content"`
	ToolCalls *toolCall          `json:"tool_calls,omitempty"`
	ToolPlan  string             `json:"tool_plan,omitempty"`
	Citations json.RawMessage    `json:"citations,omitempty"`
}

type streamDeltaContent struct {
	Type     string `json:"type,omitempty"`
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`
}
