// Package geminiapi implements the Gemini API ingress dialect
// (generateContent and streamGenerateContent): it translates client requests
// into the canonical chat request and renders canonical responses back in the
// Gemini shape. It depends only on internal/core; talking to the Gemini
// upstream is the gemini provider's job (see ADR-0017).
package geminiapi

import "github.com/goccy/go-json"

// The Gemini API is defined in protobuf, so its JSON accepts every field in
// both lowerCamelCase and snake_case, and Google's own examples mix the two
// (system_instruction, inline_data). Request types therefore carry the
// snake_case spelling in a second field; the accessors below return whichever
// the client sent. Responses always use lowerCamelCase.

// GenerateContentRequest is the body of generateContent and
// streamGenerateContent.
type GenerateContentRequest struct {
	Contents             []Content         `json:"contents"`
	SystemInstruction    *Content          `json:"systemInstruction,omitempty"`
	SystemInstructionAlt *Content          `json:"system_instruction,omitempty" swaggerignore:"true"`
	Tools                []Tool            `json:"tools,omitempty"`
	ToolConfig           *ToolConfig       `json:"toolConfig,omitempty"`
	ToolConfigAlt        *ToolConfig       `json:"tool_config,omitempty" swaggerignore:"true"`
	GenerationConfig     *GenerationConfig `json:"generationConfig,omitempty"`
	GenerationConfigAlt  *GenerationConfig `json:"generation_config,omitempty" swaggerignore:"true"`
	// SafetySettings are accepted and ignored: they have no portable
	// equivalent, and a provider's own defaults apply.
	SafetySettings    json.RawMessage `json:"safetySettings,omitempty" swaggertype:"array,object"`
	SafetySettingsAlt json.RawMessage `json:"safety_settings,omitempty" swaggerignore:"true"`
	CachedContent     string          `json:"cachedContent,omitempty"`
	CachedContentAlt  string          `json:"cached_content,omitempty" swaggerignore:"true"`
}

func (r *GenerateContentRequest) systemInstruction() *Content {
	if r.SystemInstruction != nil {
		return r.SystemInstruction
	}
	return r.SystemInstructionAlt
}

func (r *GenerateContentRequest) toolConfig() *ToolConfig {
	if r.ToolConfig != nil {
		return r.ToolConfig
	}
	return r.ToolConfigAlt
}

func (r *GenerateContentRequest) generationConfig() *GenerationConfig {
	if r.GenerationConfig != nil {
		return r.GenerationConfig
	}
	return r.GenerationConfigAlt
}

func (r *GenerateContentRequest) cachedContent() string {
	if r.CachedContent != "" {
		return r.CachedContent
	}
	return r.CachedContentAlt
}

// Content is one turn of the conversation: a role ("user" or "model") and its
// parts.
type Content struct {
	Role  string `json:"role,omitempty"`
	Parts []Part `json:"parts"`
}

// Part is one piece of a Content. Exactly one of the data members is set.
type Part struct {
	Text                string            `json:"text,omitempty"`
	InlineData          *Blob             `json:"inlineData,omitempty"`
	InlineDataAlt       *Blob             `json:"inline_data,omitempty" swaggerignore:"true"`
	FunctionCall        *FunctionCall     `json:"functionCall,omitempty"`
	FunctionCallAlt     *FunctionCall     `json:"function_call,omitempty" swaggerignore:"true"`
	FunctionResponse    *FunctionResponse `json:"functionResponse,omitempty"`
	FunctionResponseAlt *FunctionResponse `json:"function_response,omitempty" swaggerignore:"true"`
	// Thought marks model reasoning, as opposed to the answer.
	Thought bool `json:"thought,omitempty"`
	// ThoughtSignature is opaque state Gemini 3 and later return on a part and
	// require back unchanged when the conversation continues.
	ThoughtSignature    string `json:"thoughtSignature,omitempty"`
	ThoughtSignatureAlt string `json:"thought_signature,omitempty" swaggerignore:"true"`

	// Members that carry content the translation cannot represent. They are
	// decoded only so a request carrying one is refused by name instead of
	// losing it silently. Members that only tune how a part is read
	// (videoMetadata, mediaResolution) are not declared and are ignored.
	FileData               json.RawMessage `json:"fileData,omitempty" swaggerignore:"true"`
	FileDataAlt            json.RawMessage `json:"file_data,omitempty" swaggerignore:"true"`
	ExecutableCode         json.RawMessage `json:"executableCode,omitempty" swaggerignore:"true"`
	ExecutableCodeAlt      json.RawMessage `json:"executable_code,omitempty" swaggerignore:"true"`
	CodeExecutionResult    json.RawMessage `json:"codeExecutionResult,omitempty" swaggerignore:"true"`
	CodeExecutionResultAlt json.RawMessage `json:"code_execution_result,omitempty" swaggerignore:"true"`
}

func (p Part) inlineData() *Blob {
	if p.InlineData != nil {
		return p.InlineData
	}
	return p.InlineDataAlt
}

func (p Part) functionCall() *FunctionCall {
	if p.FunctionCall != nil {
		return p.FunctionCall
	}
	return p.FunctionCallAlt
}

func (p Part) functionResponse() *FunctionResponse {
	if p.FunctionResponse != nil {
		return p.FunctionResponse
	}
	return p.FunctionResponseAlt
}

func (p Part) thoughtSignature() string {
	if p.ThoughtSignature != "" {
		return p.ThoughtSignature
	}
	return p.ThoughtSignatureAlt
}

// unsupportedMember names the first member of p the translation cannot
// represent, or "".
func (p Part) unsupportedMember() string {
	switch {
	case len(p.FileData) > 0 || len(p.FileDataAlt) > 0:
		return "fileData"
	case len(p.ExecutableCode) > 0 || len(p.ExecutableCodeAlt) > 0:
		return "executableCode"
	case len(p.CodeExecutionResult) > 0 || len(p.CodeExecutionResultAlt) > 0:
		return "codeExecutionResult"
	}
	return ""
}

// Blob is inline media: base64 data with its MIME type.
type Blob struct {
	MimeType    string `json:"mimeType,omitempty"`
	MimeTypeAlt string `json:"mime_type,omitempty" swaggerignore:"true"`
	Data        string `json:"data,omitempty"`
}

func (b *Blob) mimeType() string {
	if b.MimeType != "" {
		return b.MimeType
	}
	return b.MimeTypeAlt
}

// FunctionCall is a function the model wants called.
type FunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty" swaggertype:"object"`
}

// FunctionResponse is the client's result for a FunctionCall.
type FunctionResponse struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response,omitempty" swaggertype:"object"`
}

// Tool is a set of tools the model may use. Only function declarations
// translate; a tool with any other member (googleSearch, codeExecution, …) is
// a Gemini built-in and is refused.
type Tool struct {
	FunctionDeclarations    []FunctionDeclaration `json:"functionDeclarations,omitempty"`
	FunctionDeclarationsAlt []FunctionDeclaration `json:"function_declarations,omitempty" swaggerignore:"true"`
	builtIn                 string
}

// UnmarshalJSON decodes the function declarations and remembers the first
// other member, which names a built-in tool.
func (t *Tool) UnmarshalJSON(data []byte) error {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	*t = Tool{}
	for name, raw := range members {
		switch name {
		case "functionDeclarations":
			if err := json.Unmarshal(raw, &t.FunctionDeclarations); err != nil {
				return err
			}
		case "function_declarations":
			if err := json.Unmarshal(raw, &t.FunctionDeclarationsAlt); err != nil {
				return err
			}
		default:
			// Map order is random; keep the smallest name so the error
			// message is stable.
			if t.builtIn == "" || name < t.builtIn {
				t.builtIn = name
			}
		}
	}
	return nil
}

func (t Tool) functionDeclarations() []FunctionDeclaration {
	if len(t.FunctionDeclarations) > 0 {
		return t.FunctionDeclarations
	}
	return t.FunctionDeclarationsAlt
}

// FunctionDeclaration describes one callable function. Parameters is Gemini's
// OpenAPI-style Schema; ParametersJSONSchema is standard JSON Schema.
type FunctionDeclaration struct {
	Name                    string          `json:"name"`
	Description             string          `json:"description,omitempty"`
	Parameters              json.RawMessage `json:"parameters,omitempty" swaggertype:"object"`
	ParametersJSONSchema    json.RawMessage `json:"parametersJsonSchema,omitempty" swaggertype:"object"`
	ParametersJSONSchemaAlt json.RawMessage `json:"parameters_json_schema,omitempty" swaggerignore:"true"`
}

// ToolConfig controls function calling.
type ToolConfig struct {
	FunctionCallingConfig    *FunctionCallingConfig `json:"functionCallingConfig,omitempty"`
	FunctionCallingConfigAlt *FunctionCallingConfig `json:"function_calling_config,omitempty" swaggerignore:"true"`
}

func (c *ToolConfig) functionCallingConfig() *FunctionCallingConfig {
	if c == nil {
		return nil
	}
	if c.FunctionCallingConfig != nil {
		return c.FunctionCallingConfig
	}
	return c.FunctionCallingConfigAlt
}

// FunctionCallingConfig selects the function calling mode: AUTO, ANY, NONE or
// VALIDATED, optionally limited to AllowedFunctionNames.
type FunctionCallingConfig struct {
	Mode                    string   `json:"mode,omitempty"`
	AllowedFunctionNames    []string `json:"allowedFunctionNames,omitempty"`
	AllowedFunctionNamesAlt []string `json:"allowed_function_names,omitempty" swaggerignore:"true"`
}

func (c *FunctionCallingConfig) allowedFunctionNames() []string {
	if len(c.AllowedFunctionNames) > 0 {
		return c.AllowedFunctionNames
	}
	return c.AllowedFunctionNamesAlt
}

// GenerationConfig holds the sampling and output options.
type GenerationConfig struct {
	Temperature           *float64        `json:"temperature,omitempty"`
	TopP                  *float64        `json:"topP,omitempty"`
	TopPAlt               *float64        `json:"top_p,omitempty" swaggerignore:"true"`
	MaxOutputTokens       *int            `json:"maxOutputTokens,omitempty"`
	MaxOutputTokensAlt    *int            `json:"max_output_tokens,omitempty" swaggerignore:"true"`
	StopSequences         []string        `json:"stopSequences,omitempty"`
	StopSequencesAlt      []string        `json:"stop_sequences,omitempty" swaggerignore:"true"`
	CandidateCount        *int            `json:"candidateCount,omitempty"`
	CandidateCountAlt     *int            `json:"candidate_count,omitempty" swaggerignore:"true"`
	Seed                  *int64          `json:"seed,omitempty"`
	PresencePenalty       *float64        `json:"presencePenalty,omitempty"`
	PresencePenaltyAlt    *float64        `json:"presence_penalty,omitempty" swaggerignore:"true"`
	FrequencyPenalty      *float64        `json:"frequencyPenalty,omitempty"`
	FrequencyPenaltyAlt   *float64        `json:"frequency_penalty,omitempty" swaggerignore:"true"`
	ResponseMimeType      string          `json:"responseMimeType,omitempty"`
	ResponseMimeTypeAlt   string          `json:"response_mime_type,omitempty" swaggerignore:"true"`
	ResponseSchema        json.RawMessage `json:"responseSchema,omitempty" swaggertype:"object"`
	ResponseSchemaAlt     json.RawMessage `json:"response_schema,omitempty" swaggerignore:"true"`
	ResponseJSONSchema    json.RawMessage `json:"responseJsonSchema,omitempty" swaggertype:"object"`
	ResponseJSONSchemaAlt json.RawMessage `json:"response_json_schema,omitempty" swaggerignore:"true"`
	ResponseModalities    []string        `json:"responseModalities,omitempty"`
	ResponseModalitiesAlt []string        `json:"response_modalities,omitempty" swaggerignore:"true"`
	ThinkingConfig        *ThinkingConfig `json:"thinkingConfig,omitempty"`
	ThinkingConfigAlt     *ThinkingConfig `json:"thinking_config,omitempty" swaggerignore:"true"`
}

func (c *GenerationConfig) topP() *float64 {
	if c.TopP != nil {
		return c.TopP
	}
	return c.TopPAlt
}

func (c *GenerationConfig) maxOutputTokens() *int {
	if c.MaxOutputTokens != nil {
		return c.MaxOutputTokens
	}
	return c.MaxOutputTokensAlt
}

func (c *GenerationConfig) stopSequences() []string {
	if len(c.StopSequences) > 0 {
		return c.StopSequences
	}
	return c.StopSequencesAlt
}

func (c *GenerationConfig) presencePenalty() *float64 {
	if c.PresencePenalty != nil {
		return c.PresencePenalty
	}
	return c.PresencePenaltyAlt
}

func (c *GenerationConfig) frequencyPenalty() *float64 {
	if c.FrequencyPenalty != nil {
		return c.FrequencyPenalty
	}
	return c.FrequencyPenaltyAlt
}

func (c *GenerationConfig) candidateCount() *int {
	if c.CandidateCount != nil {
		return c.CandidateCount
	}
	return c.CandidateCountAlt
}

func (c *GenerationConfig) responseMimeType() string {
	if c.ResponseMimeType != "" {
		return c.ResponseMimeType
	}
	return c.ResponseMimeTypeAlt
}

func (c *GenerationConfig) responseSchema() json.RawMessage {
	if len(c.ResponseSchema) > 0 {
		return c.ResponseSchema
	}
	return c.ResponseSchemaAlt
}

func (c *GenerationConfig) responseJSONSchema() json.RawMessage {
	if len(c.ResponseJSONSchema) > 0 {
		return c.ResponseJSONSchema
	}
	return c.ResponseJSONSchemaAlt
}

func (c *GenerationConfig) responseModalities() []string {
	if len(c.ResponseModalities) > 0 {
		return c.ResponseModalities
	}
	return c.ResponseModalitiesAlt
}

func (c *GenerationConfig) thinkingConfig() *ThinkingConfig {
	if c.ThinkingConfig != nil {
		return c.ThinkingConfig
	}
	return c.ThinkingConfigAlt
}

// ThinkingConfig controls model reasoning: a token budget (Gemini 2.5) or a
// level (Gemini 3 and later), and whether thoughts are returned.
type ThinkingConfig struct {
	ThinkingBudget     *int   `json:"thinkingBudget,omitempty"`
	ThinkingBudgetAlt  *int   `json:"thinking_budget,omitempty" swaggerignore:"true"`
	ThinkingLevel      string `json:"thinkingLevel,omitempty"`
	ThinkingLevelAlt   string `json:"thinking_level,omitempty" swaggerignore:"true"`
	IncludeThoughts    bool   `json:"includeThoughts,omitempty"`
	IncludeThoughtsAlt bool   `json:"include_thoughts,omitempty" swaggerignore:"true"`
}

func (c *ThinkingConfig) thinkingBudget() *int {
	if c.ThinkingBudget != nil {
		return c.ThinkingBudget
	}
	return c.ThinkingBudgetAlt
}

func (c *ThinkingConfig) thinkingLevel() string {
	if c.ThinkingLevel != "" {
		return c.ThinkingLevel
	}
	return c.ThinkingLevelAlt
}

func (c *ThinkingConfig) includeThoughts() bool {
	return c.IncludeThoughts || c.IncludeThoughtsAlt
}

// GenerateContentResponse is the generateContent response body and each
// streamGenerateContent event.
type GenerateContentResponse struct {
	Candidates    []Candidate    `json:"candidates"`
	UsageMetadata *UsageMetadata `json:"usageMetadata,omitempty"`
	ModelVersion  string         `json:"modelVersion,omitempty"`
	ResponseID    string         `json:"responseId,omitempty"`
}

// Candidate is one generated answer.
type Candidate struct {
	Content      ResponseContent `json:"content"`
	FinishReason string          `json:"finishReason,omitempty"`
	Index        int             `json:"index"`
}

// ResponseContent is the content of a Candidate. Its role is always "model".
type ResponseContent struct {
	Parts []ResponsePart `json:"parts"`
	Role  string         `json:"role"`
}

// ResponsePart is one part of a generated answer: text (reasoning when
// Thought is set) or a function call.
type ResponsePart struct {
	Text             *string       `json:"text,omitempty"`
	Thought          bool          `json:"thought,omitempty"`
	FunctionCall     *FunctionCall `json:"functionCall,omitempty"`
	ThoughtSignature string        `json:"thoughtSignature,omitempty"`
}

// UsageMetadata reports token usage in the Gemini shape: promptTokenCount is
// the whole prompt including cached tokens, and thoughtsTokenCount is counted
// apart from candidatesTokenCount.
type UsageMetadata struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount,omitempty"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount,omitempty"`
	TotalTokenCount         int `json:"totalTokenCount"`
}

// ErrorResponse is the Gemini (google.rpc.Status) error envelope.
type ErrorResponse struct {
	Error ErrorObject `json:"error"`
}

// ErrorObject is the inner error of a Gemini error envelope.
type ErrorObject struct {
	// Code is the HTTP status code.
	Code    int    `json:"code"`
	Message string `json:"message"`
	// Status is the canonical gRPC status name, e.g. INVALID_ARGUMENT.
	Status string `json:"status"`
}

// ModelsList is the GET /v1beta/models response body.
type ModelsList struct {
	Models        []ModelInfo `json:"models"`
	NextPageToken string      `json:"nextPageToken,omitempty"`
}

// ModelInfo is one model in the Gemini model shape.
type ModelInfo struct {
	// Name is the resource name: "models/" followed by the model ID.
	Name                       string   `json:"name"`
	DisplayName                string   `json:"displayName,omitempty"`
	Description                string   `json:"description,omitempty"`
	InputTokenLimit            int      `json:"inputTokenLimit,omitempty"`
	OutputTokenLimit           int      `json:"outputTokenLimit,omitempty"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
}
