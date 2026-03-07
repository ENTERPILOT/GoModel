package core

import (
	"bytes"
	"encoding/json"
)

// StreamOptions controls streaming behavior options.
// This is used to request usage data in streaming responses.
type StreamOptions struct {
	// IncludeUsage requests token usage information in streaming responses.
	// When true, the final streaming chunk will include usage statistics.
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// Reasoning configures reasoning behavior for models that support extended thinking.
// This is used with OpenAI's o-series models and other reasoning-capable models.
type Reasoning struct {
	// Effort controls how much reasoning effort the model should use.
	// Valid values are "low", "medium", and "high".
	Effort string `json:"effort,omitempty"`
}

// ChatRequest represents the incoming chat completion request
type ChatRequest struct {
	Temperature       *float64         `json:"temperature,omitempty"`
	MaxTokens         *int             `json:"max_tokens,omitempty"`
	Model             string           `json:"model"`
	Provider          string           `json:"provider,omitempty"`
	Messages          []Message        `json:"messages"`
	Tools             []map[string]any `json:"tools,omitempty"`
	ToolChoice        any              `json:"tool_choice,omitempty"` // string or object
	ParallelToolCalls *bool            `json:"parallel_tool_calls,omitempty"`
	Stream            bool             `json:"stream,omitempty"`
	StreamOptions     *StreamOptions   `json:"stream_options,omitempty"`
	Reasoning         *Reasoning       `json:"reasoning,omitempty"`
}

// WithStreaming returns a shallow copy of the request with Stream set to true.
// This avoids mutating the caller's request object.
func (r *ChatRequest) WithStreaming() *ChatRequest {
	cp := *r
	cp.Stream = true
	return &cp
}

// Message represents a single message in the chat
type Message struct {
	Role        string     `json:"role"`
	Content     string     `json:"content"`
	ToolCalls   []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID  string     `json:"tool_call_id,omitempty"`
	ContentNull bool       `json:"-"`
}

// UnmarshalJSON accepts content as string or null for compatibility with
// tool-calling responses that omit assistant text.
func (m *Message) UnmarshalJSON(data []byte) error {
	type rawMessage struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
		ToolCallID string          `json:"tool_call_id,omitempty"`
	}

	var raw rawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	m.Role = raw.Role
	m.ContentNull = false
	switch trimmed := bytes.TrimSpace(raw.Content); {
	case len(trimmed) == 0:
		m.Content = ""
	case bytes.Equal(trimmed, []byte("null")):
		m.Content = ""
		m.ContentNull = true
	default:
		if err := json.Unmarshal(trimmed, &m.Content); err != nil {
			return err
		}
	}
	m.ToolCalls = raw.ToolCalls
	m.ToolCallID = raw.ToolCallID

	return nil
}

// MarshalJSON preserves explicit null content for tool-calling assistant messages.
func (m Message) MarshalJSON() ([]byte, error) {
	type rawMessage struct {
		Role       string     `json:"role"`
		Content    any        `json:"content"`
		ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
	}

	content := any(m.Content)
	if m.ContentNull {
		content = nil
	}

	return json.Marshal(rawMessage{
		Role:       m.Role,
		Content:    content,
		ToolCalls:  m.ToolCalls,
		ToolCallID: m.ToolCallID,
	})
}

// ToolCall represents a single tool invocation emitted by a model.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall contains the function name and serialized arguments payload.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatResponse represents the chat completion response
type ChatResponse struct {
	ID       string   `json:"id"`
	Object   string   `json:"object"`
	Model    string   `json:"model"`
	Provider string   `json:"provider"`
	Choices  []Choice `json:"choices"`
	Usage    Usage    `json:"usage"`
	Created  int64    `json:"created"`
}

// Choice represents a single completion choice
type Choice struct {
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
	Index        int     `json:"index"`
}

// PromptTokensDetails holds extended input token breakdown (OpenAI/xAI).
type PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
	AudioTokens  int `json:"audio_tokens"`
	TextTokens   int `json:"text_tokens"`
	ImageTokens  int `json:"image_tokens"`
}

// CompletionTokensDetails holds extended output token breakdown (OpenAI/xAI).
type CompletionTokensDetails struct {
	ReasoningTokens          int `json:"reasoning_tokens"`
	AudioTokens              int `json:"audio_tokens"`
	AcceptedPredictionTokens int `json:"accepted_prediction_tokens"`
	RejectedPredictionTokens int `json:"rejected_prediction_tokens"`
}

// Usage represents token usage information
type Usage struct {
	PromptTokens            int                      `json:"prompt_tokens"`
	CompletionTokens        int                      `json:"completion_tokens"`
	TotalTokens             int                      `json:"total_tokens"`
	PromptTokensDetails     *PromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
	RawUsage                map[string]any           `json:"raw_usage,omitempty"`
}

// Model represents a single model in the models list
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
	Created int64  `json:"created"`
	// Metadata holds optional enrichment data (display name, pricing, capabilities, etc.).
	// May be nil if the model was not found in the external registry.
	Metadata *ModelMetadata `json:"metadata,omitempty"`
}

// ModelMetadata holds enriched metadata from the external model registry.
type ModelMetadata struct {
	DisplayName     string          `json:"display_name,omitempty"`
	Description     string          `json:"description,omitempty"`
	Family          string          `json:"family,omitempty"`
	Modes           []string        `json:"modes,omitempty"`
	Categories      []ModelCategory `json:"categories,omitempty"`
	Tags            []string        `json:"tags,omitempty"`
	ContextWindow   *int            `json:"context_window,omitempty"`
	MaxOutputTokens *int            `json:"max_output_tokens,omitempty"`
	Capabilities    map[string]bool `json:"capabilities,omitempty"`
	Pricing         *ModelPricing   `json:"pricing,omitempty"`
}

// ModelCategory represents a model's functional category for UI grouping.
type ModelCategory string

const (
	CategoryAll            ModelCategory = "all"
	CategoryTextGeneration ModelCategory = "text_generation"
	CategoryEmbedding      ModelCategory = "embedding"
	CategoryImage          ModelCategory = "image"
	CategoryAudio          ModelCategory = "audio"
	CategoryVideo          ModelCategory = "video"
	CategoryUtility        ModelCategory = "utility"
)

// modeToCategory maps mode strings from the external registry to categories.
var modeToCategory = map[string]ModelCategory{
	"chat":                CategoryTextGeneration,
	"completion":          CategoryTextGeneration,
	"responses":           CategoryTextGeneration,
	"embedding":           CategoryEmbedding,
	"rerank":              CategoryEmbedding,
	"image_generation":    CategoryImage,
	"image_edit":          CategoryImage,
	"audio_transcription": CategoryAudio,
	"audio_speech":        CategoryAudio,
	"video_generation":    CategoryVideo,
	"moderation":          CategoryUtility,
	"ocr":                 CategoryUtility,
	"search":              CategoryUtility,
}

// CategoriesForModes returns deduplicated ModelCategory values for the given mode strings.
// Unrecognized modes are silently skipped.
func CategoriesForModes(modes []string) []ModelCategory {
	seen := make(map[ModelCategory]struct{}, len(modes))
	result := make([]ModelCategory, 0, len(modes))
	for _, mode := range modes {
		cat, ok := modeToCategory[mode]
		if !ok {
			continue
		}
		if _, dup := seen[cat]; dup {
			continue
		}
		seen[cat] = struct{}{}
		result = append(result, cat)
	}
	return result
}

// AllCategories returns the ordered list of categories for UI rendering.
func AllCategories() []ModelCategory {
	return []ModelCategory{
		CategoryAll,
		CategoryTextGeneration,
		CategoryEmbedding,
		CategoryImage,
		CategoryAudio,
		CategoryVideo,
		CategoryUtility,
	}
}

// ModelPricing holds pricing information for cost calculation.
type ModelPricing struct {
	Currency               string             `json:"currency"`
	InputPerMtok           *float64           `json:"input_per_mtok,omitempty"`
	OutputPerMtok          *float64           `json:"output_per_mtok,omitempty"`
	CachedInputPerMtok     *float64           `json:"cached_input_per_mtok,omitempty"`
	CacheWritePerMtok      *float64           `json:"cache_write_per_mtok,omitempty"`
	ReasoningOutputPerMtok *float64           `json:"reasoning_output_per_mtok,omitempty"`
	BatchInputPerMtok      *float64           `json:"batch_input_per_mtok,omitempty"`
	BatchOutputPerMtok     *float64           `json:"batch_output_per_mtok,omitempty"`
	AudioInputPerMtok      *float64           `json:"audio_input_per_mtok,omitempty"`
	AudioOutputPerMtok     *float64           `json:"audio_output_per_mtok,omitempty"`
	PerImage               *float64           `json:"per_image,omitempty"`
	InputPerImage          *float64           `json:"input_per_image,omitempty"`
	PerSecondInput         *float64           `json:"per_second_input,omitempty"`
	PerSecondOutput        *float64           `json:"per_second_output,omitempty"`
	PerCharacterInput      *float64           `json:"per_character_input,omitempty"`
	PerRequest             *float64           `json:"per_request,omitempty"`
	PerPage                *float64           `json:"per_page,omitempty"`
	Tiers                  []ModelPricingTier `json:"tiers,omitempty"`
}

// ModelPricingTier represents a volume-based pricing tier.
type ModelPricingTier struct {
	UpToMtok      *float64 `json:"up_to_mtok,omitempty"`
	InputPerMtok  *float64 `json:"input_per_mtok,omitempty"`
	OutputPerMtok *float64 `json:"output_per_mtok,omitempty"`
}

// ModelsResponse represents the response from the /v1/models endpoint
type ModelsResponse struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

// EmbeddingRequest represents the incoming embeddings request (OpenAI-compatible).
type EmbeddingRequest struct {
	Model          string `json:"model"`
	Provider       string `json:"provider,omitempty"`
	Input          any    `json:"input"`
	EncodingFormat string `json:"encoding_format,omitempty"`
	Dimensions     *int   `json:"dimensions,omitempty"`
}

// EmbeddingResponse represents the embeddings response (OpenAI-compatible).
type EmbeddingResponse struct {
	Object   string          `json:"object"`
	Data     []EmbeddingData `json:"data"`
	Model    string          `json:"model"`
	Provider string          `json:"provider"`
	Usage    EmbeddingUsage  `json:"usage"`
}

// EmbeddingData represents a single embedding data point.
// Embedding is json.RawMessage to support both float arrays and base64-encoded strings.
type EmbeddingData struct {
	Object    string          `json:"object"`
	Embedding json.RawMessage `json:"embedding" swaggertype:"object"`
	Index     int             `json:"index"`
}

// EmbeddingUsage represents token usage information for embeddings.
type EmbeddingUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}
