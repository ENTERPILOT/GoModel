package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"gomodel/internal/core"
	"gomodel/internal/providers"
)

// defaultMaxTokensEnvVar overrides the fallback applied when callers omit
// max_tokens. Anthropic requires the field on every /v1/messages request, so
// GoModel injects this value to keep the OpenAI-compatible surface lenient.
const defaultMaxTokensEnvVar = "ANTHROPIC_DEFAULT_MAX_TOKENS"

// fallbackMaxTokens is the safe default used when the env var is unset or
// invalid.
const fallbackMaxTokens = 4096

var invalidDefaultMaxTokensWarnOnce sync.Once

func resolveDefaultMaxTokens() int {
	raw := strings.TrimSpace(os.Getenv(defaultMaxTokensEnvVar))
	if raw == "" {
		return fallbackMaxTokens
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		invalidDefaultMaxTokensWarnOnce.Do(func() {
			slog.Warn("invalid "+defaultMaxTokensEnvVar+"; using fallback",
				"value", raw, "fallback", fallbackMaxTokens)
		})
		return fallbackMaxTokens
	}
	return n
}

// applyReasoning configures thinking and effort on an anthropicRequest.
// Opus 4.6 and Sonnet 4.6 use adaptive thinking with output_config.effort.
// Older models and Haiku 4.6 use manual thinking with budget_tokens.
func applyReasoning(req *anthropicRequest, model, effort string) {
	if isAdaptiveThinkingModel(model) {
		req.Thinking = &anthropicThinking{Type: "adaptive"}
		req.OutputConfig = &anthropicOutputConfig{Effort: normalizeEffort(effort)}
	} else {
		budget := reasoningEffortToBudgetTokens(effort)
		req.Thinking = &anthropicThinking{
			Type:         "enabled",
			BudgetTokens: budget,
		}
		if req.MaxTokens <= budget {
			adjusted := budget + 1024
			slog.Info("MaxTokens adjusted for extended thinking",
				"original", req.MaxTokens, "adjusted", adjusted)
			req.MaxTokens = adjusted
		}
	}

	if req.Temperature != nil {
		if *req.Temperature != 1.0 {
			slog.Warn("temperature overridden to nil; extended thinking requires temperature=1",
				"original_temperature", *req.Temperature)
			req.Temperature = nil
		}
	}
}

func reasoningEffortToBudgetTokens(effort string) int {
	switch normalizeEffort(effort) {
	case "medium":
		return 10000
	case "high":
		return 20000
	default:
		return 5000
	}
}

func convertOpenAIToolsToAnthropic(tools []map[string]any) ([]anthropicTool, error) {
	out := make([]anthropicTool, 0, len(tools))
	for _, tool := range tools {
		toolType, _ := tool["type"].(string)
		if toolType != "function" {
			return nil, core.NewInvalidRequestError("unsupported tool type", nil)
		}

		function, ok := tool["function"].(map[string]any)
		if !ok {
			return nil, core.NewInvalidRequestError("tool.function must be an object", nil)
		}

		name, _ := function["name"].(string)
		if strings.TrimSpace(name) == "" {
			return nil, core.NewInvalidRequestError("tool.function.name is required", nil)
		}

		description, _ := function["description"].(string)
		inputSchema, hasParameters := function["parameters"]
		if !hasParameters || inputSchema == nil {
			inputSchema = map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}
		} else {
			schema, ok := inputSchema.(map[string]any)
			if !ok {
				return nil, core.NewInvalidRequestError("tool.function.parameters must be an object", nil)
			}
			if schemaType, ok := schema["type"].(string); ok && schemaType != "" && schemaType != "object" {
				return nil, core.NewInvalidRequestError("tool.function.parameters must define an object schema", nil)
			}
			inputSchema = schema
		}

		out = append(out, anthropicTool{
			Name:        name,
			Description: description,
			InputSchema: inputSchema.(map[string]any),
		})
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func convertOpenAIToolChoiceToAnthropic(choice any) (*anthropicToolChoice, bool, error) {
	switch c := choice.(type) {
	case nil:
		return nil, false, nil
	case string:
		switch strings.TrimSpace(c) {
		case "", "auto":
			return &anthropicToolChoice{Type: "auto"}, false, nil
		case "required":
			return &anthropicToolChoice{Type: "any"}, false, nil
		case "none":
			return nil, true, nil
		default:
			return nil, false, core.NewInvalidRequestError("unsupported tool_choice value", nil)
		}
	case map[string]any:
		choiceType, _ := c["type"].(string)
		switch choiceType {
		case "auto", "any":
			return &anthropicToolChoice{Type: choiceType}, false, nil
		case "none":
			return nil, true, nil
		case "function":
			if function, ok := c["function"].(map[string]any); ok {
				name, _ := function["name"].(string)
				if strings.TrimSpace(name) != "" {
					return &anthropicToolChoice{Type: "tool", Name: name}, false, nil
				}
			}
			return nil, false, core.NewInvalidRequestError("tool_choice.function.name is required", nil)
		case "tool":
			name, _ := c["name"].(string)
			if name == "" {
				if function, ok := c["function"].(map[string]any); ok {
					name, _ = function["name"].(string)
				}
			}
			if strings.TrimSpace(name) == "" {
				return nil, false, core.NewInvalidRequestError("tool_choice.name is required", nil)
			}
			return &anthropicToolChoice{Type: "tool", Name: name}, false, nil
		default:
			return nil, false, core.NewInvalidRequestError("unsupported tool_choice type", nil)
		}
	default:
		return nil, false, core.NewInvalidRequestError("tool_choice must be a string or object", nil)
	}
}

func applyParallelToolCalls(choice *anthropicToolChoice, parallelToolCalls *bool) *anthropicToolChoice {
	if choice == nil || parallelToolCalls == nil || *parallelToolCalls {
		return choice
	}

	out := *choice
	disableParallelToolUse := true
	out.DisableParallelToolUse = &disableParallelToolUse
	return &out
}

func parseToolCallArguments(arguments string) (any, error) {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return map[string]any{}, nil
	}

	var parsed any
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&parsed); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("tool arguments must contain exactly one JSON object")
		}
		return nil, err
	}
	if _, ok := parsed.(map[string]any); !ok {
		return nil, fmt.Errorf("tool arguments must be a JSON object")
	}
	return parsed, nil
}

func buildAnthropicMessageContent(msg core.Message) (any, error) {
	const maxToolCallsPerMessage = 1024

	if msg.Role == "tool" {
		toolUseID := strings.TrimSpace(msg.ToolCallID)
		if toolUseID == "" {
			return nil, core.NewInvalidRequestError("tool message is missing tool_call_id", nil)
		}
		return []anthropicContentBlock{
			{
				Type:      "tool_result",
				ToolUseID: toolUseID,
				Content:   core.ExtractTextContent(msg.Content),
			},
		}, nil
	}

	content, err := convertMessageContentToAnthropic(msg.Content)
	if err != nil {
		return nil, err
	}
	if len(msg.ToolCalls) == 0 {
		return content, nil
	}
	if len(msg.ToolCalls) > maxToolCallsPerMessage {
		return nil, core.NewInvalidRequestError("too many tool calls in message", nil)
	}

	blocks := make([]anthropicContentBlock, 0, len(msg.ToolCalls)+1)
	switch c := content.(type) {
	case string:
		if strings.TrimSpace(c) != "" {
			blocks = append(blocks, anthropicContentBlock{
				Type: "text",
				Text: c,
			})
		}
	case []anthropicContentBlock:
		blocks = append(blocks, c...)
	}
	for _, toolCall := range msg.ToolCalls {
		toolCallID := providers.ResponsesFunctionCallCallID(strings.TrimSpace(toolCall.ID))
		toolName := strings.TrimSpace(toolCall.Function.Name)
		if toolName == "" {
			return nil, core.NewInvalidRequestError("tool_call.function.name is required", nil)
		}
		input, err := parseToolCallArguments(toolCall.Function.Arguments)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, anthropicContentBlock{
			Type:  "tool_use",
			ID:    toolCallID,
			Name:  toolName,
			Input: input,
		})
	}
	return blocks, nil
}

// convertToAnthropicRequest converts core.ChatRequest to Anthropic format.
func convertToAnthropicRequest(req *core.ChatRequest) (*anthropicRequest, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("anthropic chat request is required", nil)
	}

	anthropicReq := &anthropicRequest{
		Model:       req.Model,
		Messages:    make([]anthropicMessage, 0, len(req.Messages)),
		Temperature: req.Temperature,
		Stream:      req.Stream,
	}

	if req.MaxTokens != nil {
		anthropicReq.MaxTokens = *req.MaxTokens
	} else {
		anthropicReq.MaxTokens = resolveDefaultMaxTokens()
	}

	if req.Reasoning != nil && req.Reasoning.Effort != "" {
		applyReasoning(anthropicReq, req.Model, req.Reasoning.Effort)
	}

	tools, err := convertOpenAIToolsToAnthropic(req.Tools)
	if err != nil {
		return nil, err
	}
	anthropicReq.Tools = tools
	if toolChoice, disableTools, err := convertOpenAIToolChoiceToAnthropic(req.ToolChoice); err != nil {
		return nil, err
	} else if err := validateAnthropicToolChoice(toolChoice, anthropicReq.Tools, disableTools); err != nil {
		return nil, err
	} else if disableTools {
		anthropicReq.Tools = nil
	} else if len(anthropicReq.Tools) > 0 {
		if toolChoice == nil && req.ParallelToolCalls != nil && !*req.ParallelToolCalls {
			toolChoice = &anthropicToolChoice{Type: "auto"}
		}
		toolChoice = applyParallelToolCalls(toolChoice, req.ParallelToolCalls)
		anthropicReq.ToolChoice = toolChoice
	}

	for _, msg := range req.Messages {
		if msg.Role == "system" {
			systemContent, err := buildAnthropicSystemContent(msg.Content)
			if err != nil {
				return nil, err
			}
			anthropicReq.System = appendAnthropicSystemContent(anthropicReq.System, systemContent)
			continue
		}

		content, err := buildAnthropicMessageContent(msg)
		if err != nil {
			return nil, normalizeAnthropicRequestError(err)
		}
		role := msg.Role
		if role == "tool" {
			role = "user"
		}
		anthropicReq.Messages = append(anthropicReq.Messages, anthropicMessage{
			Role:    role,
			Content: content,
		})
	}

	return anthropicReq, nil
}

// convertResponsesRequestToAnthropic converts a canonical Responses request by
// first mapping it onto shared chat semantics and then translating that semantic
// request into Anthropic's native message payload.
func convertResponsesRequestToAnthropic(req *core.ResponsesRequest) (*anthropicRequest, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("anthropic responses request is required", nil)
	}

	chatReq, err := providers.ConvertResponsesRequestToChat(req)
	if err != nil {
		return nil, err
	}
	return convertToAnthropicRequest(chatReq)
}

// convertDecodedBatchItemToAnthropic translates a canonical known batch item
// using the same semantic mapping path as normal chat and responses requests.
func convertDecodedBatchItemToAnthropic(decoded *core.DecodedBatchItemRequest) (*anthropicRequest, error) {
	if decoded == nil {
		return nil, core.NewInvalidRequestError("decoded anthropic batch request is required", nil)
	}

	return core.DispatchDecodedBatchItem(decoded, core.DecodedBatchItemHandlers[*anthropicRequest]{
		Chat: func(req *core.ChatRequest) (*anthropicRequest, error) {
			if req == nil {
				return nil, core.NewInvalidRequestError("anthropic chat request is required", nil)
			}
			if req.Stream {
				return nil, core.NewInvalidRequestError("streaming is not supported for native batch", nil)
			}
			params, err := convertToAnthropicRequest(req)
			if err != nil {
				return nil, err
			}
			params.Stream = false
			return params, nil
		},
		Responses: func(req *core.ResponsesRequest) (*anthropicRequest, error) {
			if req == nil {
				return nil, core.NewInvalidRequestError("anthropic responses request is required", nil)
			}
			if req.Stream {
				return nil, core.NewInvalidRequestError("streaming is not supported for native batch", nil)
			}
			params, err := convertResponsesRequestToAnthropic(req)
			if err != nil {
				return nil, err
			}
			params.Stream = false
			return params, nil
		},
		Embeddings: func(*core.EmbeddingRequest) (*anthropicRequest, error) {
			return nil, core.NewInvalidRequestError("anthropic does not support native embedding batches", nil)
		},
		Default: func(decoded *core.DecodedBatchItemRequest) (*anthropicRequest, error) {
			return nil, core.NewInvalidRequestError(fmt.Sprintf("unsupported anthropic batch url: %s", decoded.Endpoint), nil)
		},
	})
}

func appendAnthropicSystemText(existing, next string) string {
	if next == "" {
		return existing
	}
	if existing == "" {
		return next
	}
	return existing + "\n\n" + next
}

func appendAnthropicSystemContent(existing, next any) any {
	if isEmptyAnthropicSystemContent(next) {
		return existing
	}
	if isEmptyAnthropicSystemContent(existing) {
		return next
	}

	if existingText, ok := existing.(string); ok {
		if nextText, ok := next.(string); ok {
			return appendAnthropicSystemText(existingText, nextText)
		}
	}

	blocks := make([]anthropicContentBlock, 0)
	blocks = append(blocks, anthropicSystemBlocks(existing)...)
	blocks = append(blocks, anthropicSystemBlocks(next)...)
	if len(blocks) == 0 {
		return nil
	}
	return blocks
}

func isEmptyAnthropicSystemContent(content any) bool {
	switch c := content.(type) {
	case nil:
		return true
	case string:
		return c == ""
	case []anthropicContentBlock:
		return len(c) == 0
	default:
		return false
	}
}

func anthropicSystemBlocks(content any) []anthropicContentBlock {
	switch c := content.(type) {
	case string:
		if c == "" {
			return nil
		}
		return []anthropicContentBlock{{Type: "text", Text: c}}
	case []anthropicContentBlock:
		return c
	default:
		return nil
	}
}

func buildAnthropicSystemContent(content any) (any, error) {
	if !core.HasStructuredContent(content) {
		text := core.ExtractTextContent(content)
		if text == "" {
			return nil, nil
		}
		return text, nil
	}

	parts, ok := core.NormalizeContentParts(content)
	if !ok {
		return nil, core.NewInvalidRequestError("unsupported anthropic chat content format", nil)
	}

	blocks := make([]anthropicContentBlock, 0, len(parts))
	hasCacheControl := false
	for _, part := range parts {
		if part.Type != "text" {
			return nil, core.NewInvalidRequestError("anthropic system messages only support text content", nil)
		}
		if part.Text == "" {
			continue
		}
		cacheControl, err := anthropicCacheControlFromExtra(part.ExtraFields)
		if err != nil {
			return nil, err
		}
		if len(cacheControl) > 0 {
			hasCacheControl = true
		}
		blocks = append(blocks, anthropicContentBlock{
			Type:         "text",
			Text:         part.Text,
			CacheControl: cacheControl,
		})
	}
	if len(blocks) == 0 {
		return nil, nil
	}
	if !hasCacheControl {
		return core.ExtractTextContent(parts), nil
	}
	return blocks, nil
}

func anthropicCacheControlFromExtra(extraFields core.UnknownJSONFields) (json.RawMessage, error) {
	raw := extraFields.Lookup("cache_control")
	if len(raw) == 0 {
		return nil, nil
	}

	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] != '{' {
		return nil, core.NewInvalidRequestError("anthropic cache_control must be an object", nil)
	}
	return core.CloneRawJSON(trimmed), nil
}

func convertMessageContentToAnthropic(content any) (any, error) {
	if !core.HasStructuredContent(content) {
		return core.ExtractTextContent(content), nil
	}

	parts, ok := core.NormalizeContentParts(content)
	if !ok {
		return nil, core.NewInvalidRequestError("unsupported anthropic chat content format", nil)
	}

	blocks := make([]anthropicContentBlock, 0, len(parts))
	for _, part := range parts {
		cacheControl, err := anthropicCacheControlFromExtra(part.ExtraFields)
		if err != nil {
			return nil, err
		}
		switch part.Type {
		case "text":
			if part.Text == "" {
				continue
			}
			blocks = append(blocks, anthropicContentBlock{
				Type:         "text",
				Text:         part.Text,
				CacheControl: cacheControl,
			})
		case "image_url":
			if part.ImageURL == nil || part.ImageURL.URL == "" {
				return nil, core.NewInvalidRequestError("anthropic image content requires image_url.url", nil)
			}
			source, err := anthropicImageSource(part.ImageURL.URL, part.ImageURL.MediaType)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, anthropicContentBlock{
				Type:         "image",
				Source:       source,
				CacheControl: cacheControl,
			})
		case "input_audio":
			return nil, core.NewInvalidRequestError("anthropic chat does not support input_audio content", nil)
		default:
			return nil, core.NewInvalidRequestError("unsupported anthropic chat content part type: "+part.Type, nil)
		}
	}
	if len(blocks) == 0 {
		return "", nil
	}
	return blocks, nil
}

func anthropicImageSource(raw, mediaTypeHint string) (*anthropicContentSource, error) {
	if strings.HasPrefix(raw, "data:") {
		comma := strings.IndexByte(raw, ',')
		if comma < 0 {
			return nil, core.NewInvalidRequestError("invalid anthropic image data URL", nil)
		}

		meta := raw[len("data:"):comma]
		tokens := strings.Split(meta, ";")
		if len(tokens) == 0 {
			return nil, core.NewInvalidRequestError("anthropic image data URL is missing a media type", nil)
		}

		mediaType := strings.TrimSpace(tokens[0])
		if mediaType == "" {
			mediaType = strings.TrimSpace(mediaTypeHint)
		}

		hasBase64 := false
		for _, token := range tokens[1:] {
			if strings.EqualFold(strings.TrimSpace(token), "base64") {
				hasBase64 = true
				break
			}
		}
		if !hasBase64 {
			return nil, core.NewInvalidRequestError("anthropic image data URL must be base64-encoded", nil)
		}

		if mediaType == "" {
			return nil, core.NewInvalidRequestError("anthropic image data URL is missing a media type", nil)
		}
		if !isAllowedAnthropicImageMediaType(mediaType) {
			return nil, core.NewInvalidRequestError("anthropic image media type is not supported: "+mediaType, nil)
		}

		data := raw[comma+1:]
		if data == "" {
			return nil, core.NewInvalidRequestError("anthropic image data URL is missing image data", nil)
		}

		return &anthropicContentSource{
			Type:      "base64",
			MediaType: mediaType,
			Data:      data,
		}, nil
	}

	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, core.NewInvalidRequestError("anthropic chat image_url must be a data: URL or http/https URL", nil)
	}

	return &anthropicContentSource{
		Type: "url",
		URL:  raw,
	}, nil
}

func isAllowedAnthropicImageMediaType(mediaType string) bool {
	_, ok := allowedAnthropicImageMediaTypes[strings.ToLower(strings.TrimSpace(mediaType))]
	return ok
}

func normalizeAnthropicRequestError(err error) error {
	if gatewayErr, ok := err.(*core.GatewayError); ok {
		return gatewayErr
	}
	message := "invalid tool_call.function.arguments JSON"
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		message = err.Error()
	}
	return core.NewInvalidRequestError(message, err)
}

func validateAnthropicToolChoice(toolChoice *anthropicToolChoice, tools []anthropicTool, disableTools bool) error {
	if disableTools || toolChoice == nil || len(tools) > 0 {
		return nil
	}
	return core.NewInvalidRequestError("tool_choice requires at least one tool", nil)
}

func prefixAnthropicBatchItemError(index int, err error) error {
	if gatewayErr, ok := errors.AsType[*core.GatewayError](err); ok {
		prefixed := *gatewayErr
		prefixed.Message = fmt.Sprintf("batch item %d: %s", index, gatewayErr.Message)
		return &prefixed
	}
	return core.NewInvalidRequestError(fmt.Sprintf("batch item %d: %v", index, err), err)
}
