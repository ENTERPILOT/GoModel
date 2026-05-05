package gemini

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gomodel/internal/core"
)

type geminiGenerateContentRequest struct {
	SystemInstruction *geminiContent    `json:"system_instruction,omitempty"`
	Contents          []geminiContent   `json:"contents"`
	Tools             []geminiTool      `json:"tools,omitempty"`
	ToolConfig        *geminiToolConfig `json:"toolConfig,omitempty"`
	GenerationConfig  map[string]any    `json:"generationConfig,omitempty"`
	SafetySettings    []map[string]any  `json:"safetySettings,omitempty"`
	CachedContent     string            `json:"cachedContent,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text                string                  `json:"text,omitempty"`
	InlineData          *geminiBlob             `json:"inline_data,omitempty"`
	FileData            *geminiFileData         `json:"file_data,omitempty"`
	FunctionCall        *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionCallAlt     *geminiFunctionCall     `json:"function_call,omitempty"`
	FunctionResponse    *geminiFunctionResponse `json:"functionResponse,omitempty"`
	FunctionResponseAlt *geminiFunctionResponse `json:"function_response,omitempty"`
	Thought             bool                    `json:"thought,omitempty"`
	ThoughtSignature    string                  `json:"thoughtSignature,omitempty"`
}

func (p geminiPart) functionCall() *geminiFunctionCall {
	if p.FunctionCall != nil {
		return p.FunctionCall
	}
	return p.FunctionCallAlt
}

type geminiBlob struct {
	MimeType string `json:"mime_type,omitempty"`
	Data     string `json:"data,omitempty"`
}

type geminiFileData struct {
	MimeType string `json:"mime_type,omitempty"`
	FileURI  string `json:"file_uri,omitempty"`
}

type geminiFunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`
}

type geminiFunctionResponse struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations,omitempty"`
}

type geminiFunctionDeclaration struct {
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type geminiToolConfig struct {
	FunctionCallingConfig geminiFunctionCallingConfig `json:"functionCallingConfig"`
}

type geminiFunctionCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

type geminiGenerateContentResponse struct {
	Candidates     []geminiCandidate   `json:"candidates,omitempty"`
	PromptFeedback json.RawMessage     `json:"promptFeedback,omitempty"`
	UsageMetadata  geminiUsageMetadata `json:"usageMetadata,omitempty"`
	ModelVersion   string              `json:"modelVersion,omitempty"`
	ResponseID     string              `json:"responseId,omitempty"`
	ModelStatus    json.RawMessage     `json:"modelStatus,omitempty"`
}

type geminiPromptFeedback struct {
	BlockReason        string `json:"blockReason,omitempty"`
	BlockReasonMessage string `json:"blockReasonMessage,omitempty"`
}

type geminiCandidate struct {
	Content       geminiContent   `json:"content,omitempty"`
	FinishReason  string          `json:"finishReason,omitempty"`
	Index         int             `json:"index,omitempty"`
	SafetyRatings json.RawMessage `json:"safetyRatings,omitempty"`
}

type geminiUsageMetadata struct {
	PromptTokenCount           int             `json:"promptTokenCount,omitempty"`
	CachedContentTokenCount    int             `json:"cachedContentTokenCount,omitempty"`
	CandidatesTokenCount       int             `json:"candidatesTokenCount,omitempty"`
	ToolUsePromptTokenCount    int             `json:"toolUsePromptTokenCount,omitempty"`
	ThoughtsTokenCount         int             `json:"thoughtsTokenCount,omitempty"`
	TotalTokenCount            int             `json:"totalTokenCount,omitempty"`
	PromptTokensDetails        json.RawMessage `json:"promptTokensDetails,omitempty"`
	CacheTokensDetails         json.RawMessage `json:"cacheTokensDetails,omitempty"`
	CandidatesTokensDetails    json.RawMessage `json:"candidatesTokensDetails,omitempty"`
	ToolUsePromptTokensDetails json.RawMessage `json:"toolUsePromptTokensDetails,omitempty"`
}

func convertChatRequestToGemini(req *core.ChatRequest) (*geminiGenerateContentRequest, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("chat request is required", nil)
	}

	out := &geminiGenerateContentRequest{
		Contents: make([]geminiContent, 0, len(req.Messages)),
	}

	systemParts := make([]geminiPart, 0)
	toolCallNames := make(map[string]string)
	for _, msg := range req.Messages {
		var (
			parts []geminiPart
			err   error
		)
		if strings.TrimSpace(msg.Role) == "tool" {
			parts, err = geminiPartsFromToolMessage(msg, toolCallNames[msg.ToolCallID])
		} else {
			parts, err = geminiPartsFromMessage(msg)
		}
		if err != nil {
			return nil, err
		}
		if len(parts) == 0 {
			continue
		}

		switch strings.TrimSpace(msg.Role) {
		case "system", "developer":
			systemParts = append(systemParts, parts...)
		case "assistant":
			out.Contents = append(out.Contents, geminiContent{Role: "model", Parts: parts})
			for _, call := range msg.ToolCalls {
				if call.ID != "" && call.Function.Name != "" {
					toolCallNames[call.ID] = call.Function.Name
				}
			}
		case "tool":
			out.Contents = append(out.Contents, geminiContent{Role: "user", Parts: parts})
		default:
			out.Contents = append(out.Contents, geminiContent{Role: "user", Parts: parts})
		}
	}
	if len(systemParts) > 0 {
		out.SystemInstruction = &geminiContent{Parts: systemParts}
	}

	tools, err := geminiToolsFromOpenAI(req.Tools)
	if err != nil {
		return nil, err
	}
	out.Tools = tools
	out.ToolConfig = geminiToolConfigFromOpenAI(req.ToolChoice)
	out.GenerationConfig = geminiGenerationConfig(req)
	out.SafetySettings = geminiSafetySettings(req)
	out.CachedContent = geminiCachedContent(req)
	return out, nil
}

func geminiPartsFromMessage(msg core.Message) ([]geminiPart, error) {
	if len(msg.ToolCalls) > 0 {
		parts := make([]geminiPart, 0, len(msg.ToolCalls)+1)
		if text := strings.TrimSpace(core.ExtractTextContent(msg.Content)); text != "" {
			parts = append(parts, geminiPart{Text: text})
		}
		for _, call := range msg.ToolCalls {
			args := json.RawMessage(strings.TrimSpace(call.Function.Arguments))
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			parts = append(parts, geminiPart{FunctionCall: &geminiFunctionCall{
				ID:   call.ID,
				Name: call.Function.Name,
				Args: args,
			}})
		}
		return parts, nil
	}

	switch content := msg.Content.(type) {
	case nil:
		return nil, nil
	case string:
		if content == "" {
			return nil, nil
		}
		return []geminiPart{{Text: content}}, nil
	default:
		parts, ok := core.NormalizeContentParts(content)
		if !ok {
			text := core.ExtractTextContent(content)
			if text == "" {
				return nil, nil
			}
			return []geminiPart{{Text: text}}, nil
		}
		return geminiPartsFromContentParts(parts)
	}
}

func geminiPartsFromToolMessage(msg core.Message, functionName string) ([]geminiPart, error) {
	if functionName == "" {
		functionName = msg.ToolCallID
	}
	response, err := geminiToolResponsePayload(core.ExtractTextContent(msg.Content))
	if err != nil {
		return nil, err
	}
	return []geminiPart{{
		FunctionResponse: &geminiFunctionResponse{
			ID:       msg.ToolCallID,
			Name:     functionName,
			Response: response,
		},
	}}, nil
}

func geminiToolResponsePayload(content string) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return json.RawMessage(`{}`), nil
	}
	if json.Valid([]byte(trimmed)) {
		if strings.HasPrefix(trimmed, "{") {
			return json.RawMessage(trimmed), nil
		}
		encoded, err := json.Marshal(map[string]json.RawMessage{"result": json.RawMessage(trimmed)})
		return json.RawMessage(encoded), err
	}
	encoded, err := json.Marshal(map[string]string{"result": content})
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to marshal Gemini tool response", err)
	}
	return json.RawMessage(encoded), nil
}

func geminiPartsFromContentParts(parts []core.ContentPart) ([]geminiPart, error) {
	out := make([]geminiPart, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "text", "input_text":
			if part.Text != "" {
				out = append(out, geminiPart{Text: part.Text})
			}
		case "image_url", "input_image":
			if part.ImageURL == nil || part.ImageURL.URL == "" {
				continue
			}
			geminiPart, err := geminiPartFromImageURL(part.ImageURL)
			if err != nil {
				return nil, err
			}
			out = append(out, geminiPart)
		case "input_audio":
			if part.InputAudio == nil {
				continue
			}
			out = append(out, geminiPart{InlineData: &geminiBlob{
				MimeType: mimeTypeForAudioFormat(part.InputAudio.Format),
				Data:     part.InputAudio.Data,
			}})
		}
	}
	return out, nil
}

func geminiPartFromImageURL(image *core.ImageURLContent) (geminiPart, error) {
	rawURL := strings.TrimSpace(image.URL)
	if strings.HasPrefix(rawURL, "data:") {
		mimeType, data, err := parseDataURL(rawURL)
		if err != nil {
			return geminiPart{}, err
		}
		if mimeType == "" {
			mimeType = image.MediaType
		}
		if mimeType == "" {
			mimeType = "image/jpeg"
		}
		return geminiPart{InlineData: &geminiBlob{MimeType: mimeType, Data: data}}, nil
	}

	return geminiPart{}, core.NewInvalidRequestError(
		"gemini native image_url supports only data: URLs; remote URLs must be uploaded via the Gemini Files API or fetched by a future adapter path",
		nil,
	)
}

func parseDataURL(rawURL string) (string, string, error) {
	header, data, ok := strings.Cut(rawURL, ",")
	if !ok {
		return "", "", core.NewInvalidRequestError("invalid data URL in image_url", nil)
	}
	mediaType := strings.TrimPrefix(header, "data:")
	mediaType = strings.TrimSuffix(mediaType, ";base64")
	if parsed, _, err := mime.ParseMediaType(mediaType); err == nil {
		mediaType = parsed
	}
	if _, err := base64.StdEncoding.DecodeString(data); err != nil {
		return "", "", core.NewInvalidRequestError("invalid base64 data in image_url", err)
	}
	return mediaType, data, nil
}

func mimeTypeForAudioFormat(format string) string {
	format = strings.Trim(strings.ToLower(strings.TrimSpace(format)), ".")
	if format == "" {
		return "audio/mpeg"
	}
	if strings.Contains(format, "/") {
		return format
	}
	return "audio/" + format
}

func geminiToolsFromOpenAI(tools []map[string]any) ([]geminiTool, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	declarations := make([]geminiFunctionDeclaration, 0, len(tools))
	for _, tool := range tools {
		if strings.TrimSpace(fmt.Sprint(tool["type"])) != "function" {
			continue
		}
		fn, ok := tool["function"].(map[string]any)
		if !ok {
			continue
		}
		name, _ := fn["name"].(string)
		if strings.TrimSpace(name) == "" {
			continue
		}
		description, _ := fn["description"].(string)
		var parameters json.RawMessage
		if raw, ok := fn["parameters"]; ok {
			encoded, err := json.Marshal(raw)
			if err != nil {
				return nil, core.NewInvalidRequestError("failed to marshal Gemini tool parameters", err)
			}
			parameters = encoded
		}
		declarations = append(declarations, geminiFunctionDeclaration{
			Name:        name,
			Description: description,
			Parameters:  parameters,
		})
	}
	if len(declarations) == 0 {
		return nil, nil
	}
	return []geminiTool{{FunctionDeclarations: declarations}}, nil
}

func geminiToolConfigFromOpenAI(choice any) *geminiToolConfig {
	mode := ""
	var allowed []string

	switch value := choice.(type) {
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "none":
			mode = "NONE"
		case "required":
			mode = "ANY"
		case "auto":
			mode = "AUTO"
		}
	case map[string]any:
		choiceType, _ := value["type"].(string)
		if strings.TrimSpace(choiceType) == "function" {
			mode = "ANY"
			if fn, ok := value["function"].(map[string]any); ok {
				if name, _ := fn["name"].(string); name != "" {
					allowed = []string{name}
				}
			}
		}
	}

	if mode == "" {
		return nil
	}
	return &geminiToolConfig{FunctionCallingConfig: geminiFunctionCallingConfig{
		Mode:                 mode,
		AllowedFunctionNames: allowed,
	}}
}

func geminiGenerationConfig(req *core.ChatRequest) map[string]any {
	cfg := make(map[string]any)
	if req.MaxTokens != nil {
		cfg["maxOutputTokens"] = *req.MaxTokens
	} else if raw := req.ExtraFields.Lookup("max_completion_tokens"); len(raw) > 0 {
		var maxTokens int
		if err := json.Unmarshal(raw, &maxTokens); err == nil && maxTokens > 0 {
			cfg["maxOutputTokens"] = maxTokens
		}
	}
	if req.Temperature != nil {
		cfg["temperature"] = *req.Temperature
	}
	copyJSONNumber(req.ExtraFields.Lookup("top_p"), cfg, "topP")
	copyJSONNumber(req.ExtraFields.Lookup("top_k"), cfg, "topK")
	copyJSONNumber(req.ExtraFields.Lookup("candidate_count"), cfg, "candidateCount")
	copyJSONNumber(req.ExtraFields.Lookup("presence_penalty"), cfg, "presencePenalty")
	copyJSONNumber(req.ExtraFields.Lookup("frequency_penalty"), cfg, "frequencyPenalty")
	copyStopSequences(req.ExtraFields.Lookup("stop"), cfg)
	copyResponseFormat(req.ExtraFields.Lookup("response_format"), cfg)
	copyGoogleThinkingConfig(req.ExtraFields.Lookup("extra_body"), cfg)
	if req.Reasoning != nil && strings.TrimSpace(req.Reasoning.Effort) != "" {
		if _, exists := cfg["thinkingConfig"]; !exists {
			if thinkingConfig := thinkingConfigForEffort(req.Model, req.Reasoning.Effort); len(thinkingConfig) > 0 {
				cfg["thinkingConfig"] = thinkingConfig
			}
		}
	}
	if len(cfg) == 0 {
		return nil
	}
	return cfg
}

func copyJSONNumber(raw json.RawMessage, cfg map[string]any, key string) {
	if len(raw) == 0 {
		return
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return
	}
	switch v := value.(type) {
	case float64:
		cfg[key] = v
	case json.Number:
		if parsed, err := v.Float64(); err == nil {
			cfg[key] = parsed
		}
	case string:
		if parsed, err := strconv.ParseFloat(v, 64); err == nil {
			cfg[key] = parsed
		}
	}
}

func copyStopSequences(raw json.RawMessage, cfg map[string]any) {
	if len(raw) == 0 {
		return
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil && one != "" {
		cfg["stopSequences"] = []string{one}
		return
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil && len(many) > 0 {
		cfg["stopSequences"] = many
	}
}

func copyResponseFormat(raw json.RawMessage, cfg map[string]any) {
	if len(raw) == 0 {
		return
	}
	var responseFormat map[string]any
	if err := json.Unmarshal(raw, &responseFormat); err != nil {
		return
	}
	formatType, _ := responseFormat["type"].(string)
	switch formatType {
	case "json_object":
		cfg["responseMimeType"] = "application/json"
	case "json_schema":
		cfg["responseMimeType"] = "application/json"
		if schemaObj, ok := responseFormat["json_schema"].(map[string]any); ok {
			if schema, ok := schemaObj["schema"]; ok {
				cfg["responseSchema"] = schema
			}
		}
	}
}

func copyGoogleThinkingConfig(raw json.RawMessage, cfg map[string]any) {
	if len(raw) == 0 {
		return
	}
	var extra struct {
		Google struct {
			ThinkingConfig map[string]any `json:"thinking_config"`
		} `json:"google"`
	}
	if err := json.Unmarshal(raw, &extra); err == nil && len(extra.Google.ThinkingConfig) > 0 {
		cfg["thinkingConfig"] = normalizeSnakeMapKeys(extra.Google.ThinkingConfig)
	}
}

func normalizeSnakeMapKeys(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for key, value := range src {
		switch key {
		case "thinking_budget":
			out["thinkingBudget"] = value
		case "thinking_level":
			out["thinkingLevel"] = value
		case "include_thoughts":
			out["includeThoughts"] = value
		default:
			out[key] = value
		}
	}
	return out
}

func thinkingConfigForEffort(model, effort string) map[string]any {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if strings.Contains(strings.ToLower(model), "gemini-2.5") {
		switch effort {
		case "none":
			return map[string]any{"thinkingBudget": 0}
		case "minimal", "low":
			return map[string]any{"thinkingBudget": 1024}
		case "medium":
			return map[string]any{"thinkingBudget": 8192}
		case "high":
			return map[string]any{"thinkingBudget": 24576}
		default:
			return nil
		}
	}
	if effort == "none" {
		effort = "minimal"
	}
	return map[string]any{"thinkingLevel": effort}
}

func geminiSafetySettings(req *core.ChatRequest) []map[string]any {
	raw := req.ExtraFields.Lookup("safety_settings")
	if len(raw) == 0 {
		return nil
	}
	var settings []map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil
	}
	return settings
}

func geminiCachedContent(req *core.ChatRequest) string {
	raw := req.ExtraFields.Lookup("cached_content")
	if len(raw) == 0 {
		return ""
	}
	var cached string
	_ = json.Unmarshal(raw, &cached)
	return cached
}

func nativeChatResponse(req *core.ChatRequest, geminiResp *geminiGenerateContentResponse, providerName string) (*core.ChatResponse, error) {
	if providerName == "" {
		providerName = "gemini"
	}
	if err := geminiBlockedPromptError(geminiResp, providerName); err != nil {
		return nil, err
	}

	created := time.Now().Unix()
	respID := geminiResp.ResponseID
	if respID == "" {
		respID = "chatcmpl-gemini-" + strconv.FormatInt(created, 10)
	}
	resp := &core.ChatResponse{
		ID:       respID,
		Object:   "chat.completion",
		Created:  created,
		Model:    req.Model,
		Provider: providerName,
		Choices:  make([]core.Choice, 0, len(geminiResp.Candidates)),
		Usage:    usageFromGemini(geminiResp.UsageMetadata),
	}
	for i, candidate := range geminiResp.Candidates {
		index := candidate.Index
		if index == 0 && i > 0 {
			index = i
		}
		content, toolCalls := openAIMessageFromGeminiParts(candidate.Content.Parts)
		resp.Choices = append(resp.Choices, core.Choice{
			Index: index,
			Message: core.ResponseMessage{
				Role:      "assistant",
				Content:   content,
				ToolCalls: toolCalls,
			},
			FinishReason: finishReasonFromGemini(candidate.FinishReason, len(toolCalls) > 0),
		})
	}
	return resp, nil
}

func geminiBlockedPromptError(resp *geminiGenerateContentResponse, providerName string) *core.GatewayError {
	if resp == nil || len(resp.Candidates) > 0 {
		return nil
	}
	reason := geminiPromptBlockReason(resp.PromptFeedback)
	if reason == "" {
		return nil
	}
	return nativeProviderError(providerName, "Gemini blocked prompt: "+reason, nil)
}

func geminiPromptBlockReason(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var feedback geminiPromptFeedback
	if err := json.Unmarshal(raw, &feedback); err != nil {
		return ""
	}
	reason := strings.TrimSpace(feedback.BlockReason)
	message := strings.TrimSpace(feedback.BlockReasonMessage)
	switch {
	case reason != "" && message != "":
		return fmt.Sprintf("%s: %s", reason, message)
	case reason != "":
		return reason
	default:
		return message
	}
}

func openAIMessageFromGeminiParts(parts []geminiPart) (string, []core.ToolCall) {
	var text strings.Builder
	toolCalls := make([]core.ToolCall, 0)
	for i, part := range parts {
		if part.Text != "" && !part.Thought {
			text.WriteString(part.Text)
		}
		if call := part.functionCall(); call != nil {
			id := call.ID
			if id == "" {
				id = "call_" + strconv.Itoa(i)
			}
			args := strings.TrimSpace(string(call.Args))
			if args == "" {
				args = "{}"
			} else {
				var compact bytes.Buffer
				if err := json.Compact(&compact, []byte(args)); err == nil {
					args = compact.String()
				}
			}
			toolCalls = append(toolCalls, core.ToolCall{
				ID:   id,
				Type: "function",
				Function: core.FunctionCall{
					Name:      call.Name,
					Arguments: args,
				},
			})
		}
	}
	return text.String(), toolCalls
}

func usageFromGemini(usage geminiUsageMetadata) core.Usage {
	completionTokens := usage.CandidatesTokenCount + usage.ThoughtsTokenCount
	out := core.Usage{
		PromptTokens:     usage.PromptTokenCount,
		CompletionTokens: completionTokens,
		TotalTokens:      usage.TotalTokenCount,
	}
	minimumTotal := out.PromptTokens + out.CompletionTokens
	if out.TotalTokens < minimumTotal {
		out.TotalTokens = out.PromptTokens + out.CompletionTokens
	}

	raw := make(map[string]any)
	promptDetails := promptTokenDetailsFromGemini(usage.PromptTokensDetails)
	if usage.CachedContentTokenCount > 0 {
		raw["cached_content_token_count"] = usage.CachedContentTokenCount
		raw["prompt_cached_tokens"] = usage.CachedContentTokenCount
		if promptDetails == nil {
			promptDetails = &core.PromptTokensDetails{}
		}
		promptDetails.CachedTokens = usage.CachedContentTokenCount
	}
	if promptDetails != nil {
		out.PromptTokensDetails = promptDetails
	}

	if usage.ToolUsePromptTokenCount > 0 {
		raw["tool_use_prompt_token_count"] = usage.ToolUsePromptTokenCount
	}
	completionDetails := completionTokenDetailsFromGemini(usage.CandidatesTokensDetails)
	if usage.ThoughtsTokenCount > 0 {
		raw["thoughts_token_count"] = usage.ThoughtsTokenCount
		raw["completion_reasoning_tokens"] = usage.ThoughtsTokenCount
		if completionDetails == nil {
			completionDetails = &core.CompletionTokensDetails{}
		}
		completionDetails.ReasoningTokens = usage.ThoughtsTokenCount
	}
	if completionDetails != nil {
		out.CompletionTokensDetails = completionDetails
	}
	if len(raw) > 0 {
		out.RawUsage = raw
	}
	return out
}

func promptTokenDetailsFromGemini(raw json.RawMessage) *core.PromptTokensDetails {
	if len(raw) == 0 {
		return nil
	}
	var counts []geminiModalityTokenCount
	if err := json.Unmarshal(raw, &counts); err != nil {
		return nil
	}
	var out core.PromptTokensDetails
	for _, count := range counts {
		switch strings.ToUpper(strings.TrimSpace(count.Modality)) {
		case "AUDIO":
			out.AudioTokens += count.TokenCount
		case "IMAGE":
			out.ImageTokens += count.TokenCount
		case "TEXT":
			out.TextTokens += count.TokenCount
		}
	}
	if out == (core.PromptTokensDetails{}) {
		return nil
	}
	return &out
}

func completionTokenDetailsFromGemini(raw json.RawMessage) *core.CompletionTokensDetails {
	if len(raw) == 0 {
		return nil
	}
	var counts []geminiModalityTokenCount
	if err := json.Unmarshal(raw, &counts); err != nil {
		return nil
	}
	var out core.CompletionTokensDetails
	for _, count := range counts {
		if strings.EqualFold(strings.TrimSpace(count.Modality), "AUDIO") {
			out.AudioTokens += count.TokenCount
		}
	}
	if out == (core.CompletionTokensDetails{}) {
		return nil
	}
	return &out
}

type geminiModalityTokenCount struct {
	Modality   string `json:"modality"`
	TokenCount int    `json:"tokenCount"`
}

func finishReasonFromGemini(reason string, hasToolCalls bool) string {
	if hasToolCalls {
		return "tool_calls"
	}
	switch strings.ToUpper(strings.TrimSpace(reason)) {
	case "", "FINISH_REASON_UNSPECIFIED":
		return ""
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "LANGUAGE", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY", "IMAGE_PROHIBITED_CONTENT", "IMAGE_RECITATION":
		return "content_filter"
	default:
		return strings.ToLower(reason)
	}
}

func nativeGenerateEndpoint(model string) string {
	return "/models/" + url.PathEscape(normalizeGeminiModelID(model)) + ":generateContent"
}

func nativeStreamEndpoint(model string) string {
	return "/models/" + url.PathEscape(normalizeGeminiModelID(model)) + ":streamGenerateContent?alt=sse"
}

func normalizeGeminiModelID(model string) string {
	model = strings.TrimSpace(model)
	if idx := strings.LastIndex(model, "/models/"); idx >= 0 {
		model = model[idx+len("/models/"):]
	}
	model = strings.TrimPrefix(model, "models/")
	model = strings.TrimPrefix(model, "google/")
	return model
}

func vertexOpenAIModelID(model string) string {
	model = normalizeGeminiModelID(model)
	if model == "" {
		return ""
	}
	return "google/" + model
}

func displayModelIDFromGemini(model, backend string) string {
	model = normalizeGeminiModelID(model)
	if backend == geminiBackendVertex && model != "" {
		return "google/" + model
	}
	return model
}

// isGeminiExposedModel normalizes provider model names and exposes only the
// model families reachable through Gemini/OpenAI-compatible text endpoints.
// Families such as imagen-* use different upstream endpoints.
func isGeminiExposedModel(modelID string) bool {
	modelID = normalizeGeminiModelID(modelID)
	return strings.HasPrefix(modelID, "gemini-") || strings.HasPrefix(modelID, "text-embedding-")
}

func nativeProviderError(providerName, message string, err error) *core.GatewayError {
	if providerName == "" {
		providerName = "gemini"
	}
	return core.NewProviderError(providerName, http.StatusBadGateway, message, err)
}
