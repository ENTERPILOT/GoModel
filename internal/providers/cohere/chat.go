package cohere

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// ChatCompletion translates an OpenAI-compatible chat request to Cohere v2.
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	upstreamReq, err := toCohereChatRequest(req, false)
	if err != nil {
		return nil, err
	}
	var upstream chatResponse
	if err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/v2/chat",
		Body:     upstreamReq,
	}, &upstream); err != nil {
		return nil, err
	}
	return fromCohereChatResponse(&upstream, req.Model), nil
}

// StreamChatCompletion translates Cohere's event stream to OpenAI chat chunks.
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	upstreamReq, err := toCohereChatRequest(req, true)
	if err != nil {
		return nil, err
	}
	body, err := p.client.DoStream(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/v2/chat",
		Body:     upstreamReq,
	})
	if err != nil {
		return nil, err
	}
	includeUsage := req.StreamOptions != nil && req.StreamOptions.IncludeUsage
	return newChatStream(body, req.Model, includeUsage), nil
}

func toCohereChatRequest(req *core.ChatRequest, stream bool) (*chatRequest, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("chat request is required", nil)
	}
	messages := make([]chatMessage, 0, len(req.Messages))
	for i, message := range req.Messages {
		converted, err := toCohereMessage(message)
		if err != nil {
			return nil, core.NewInvalidRequestError(fmt.Sprintf("messages[%d]: %v", i, err), err)
		}
		messages = append(messages, converted)
	}

	tools, choice, err := toCohereTools(req.Tools, req.ToolChoice)
	if err != nil {
		return nil, err
	}
	out := &chatRequest{
		Model:       req.Model,
		Messages:    messages,
		Tools:       tools,
		ToolChoice:  choice,
		MaxTokens:   cohereMaxTokens(req),
		Temperature: req.Temperature,
		P:           req.TopP,
		Stream:      stream,
	}
	copyChatExtraFields(out, req.ExtraFields)
	return out, nil
}

func cohereMaxTokens(req *core.ChatRequest) *int {
	if req.MaxTokens != nil {
		return req.MaxTokens
	}
	raw := req.ExtraFields.Lookup("max_completion_tokens")
	var value int
	if len(raw) > 0 && json.Unmarshal(raw, &value) == nil && value > 0 {
		return &value
	}
	return nil
}

func toCohereMessage(message core.Message) (chatMessage, error) {
	role := strings.ToLower(strings.TrimSpace(message.Role))
	switch role {
	case "developer":
		role = "system"
	case "system", "user", "assistant", "tool":
	default:
		role = "user"
	}

	out := chatMessage{Role: role}
	if role == "tool" {
		if strings.TrimSpace(message.ToolCallID) == "" {
			return chatMessage{}, fmt.Errorf("tool_call_id is required for tool messages")
		}
		out.ToolCallID = message.ToolCallID
		out.Content = cohereToolResult(core.ExtractTextContent(message.Content))
		return out, nil
	}

	content, err := toCohereContent(message, role)
	if err != nil {
		return chatMessage{}, err
	}
	if content == nil && (role == "system" || role == "user") {
		content = ""
	}
	out.Content = content
	if role == "assistant" {
		out.ToolCalls = make([]toolCall, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			arguments := strings.TrimSpace(call.Function.Arguments)
			if arguments == "" {
				arguments = "{}"
			}
			if !json.Valid([]byte(arguments)) {
				return chatMessage{}, fmt.Errorf("tool_call.function.arguments must be valid JSON")
			}
			out.ToolCalls = append(out.ToolCalls, toolCall{
				ID:   call.ID,
				Type: "function",
				Function: toolFunction{
					Name:      call.Function.Name,
					Arguments: arguments,
				},
			})
		}
	}
	return out, nil
}

func toCohereContent(message core.Message, role string) (any, error) {
	text := core.ExtractTextContent(message.Content)
	parts, structured := core.NormalizeContentParts(message.Content)
	reasoning := rawString(message.ExtraFields.Lookup("reasoning_content"))

	if structured && role == "system" {
		for _, part := range parts {
			if part.Type != "text" {
				return nil, fmt.Errorf("system messages only support text content with Cohere")
			}
		}
		return text, nil
	}

	if !structured {
		if role == "assistant" && reasoning != "" {
			content := []contentPart{{Type: "thinking", Thinking: reasoning}}
			if text != "" {
				content = append(content, contentPart{Type: "text", Text: text})
			}
			return content, nil
		}
		if text == "" {
			return nil, nil
		}
		return text, nil
	}

	content := make([]contentPart, 0, len(parts)+1)
	if role == "assistant" && reasoning != "" {
		content = append(content, contentPart{Type: "thinking", Thinking: reasoning})
	}
	for _, part := range parts {
		switch part.Type {
		case "text":
			content = append(content, contentPart{Type: "text", Text: part.Text})
		case "image_url":
			if role != "user" {
				return nil, fmt.Errorf("image content is only supported in user messages")
			}
			content = append(content, contentPart{
				Type: "image_url",
				ImageURL: &imageURLContainer{
					URL:    part.ImageURL.URL,
					Detail: part.ImageURL.Detail,
				},
			})
		default:
			return nil, fmt.Errorf("content part type %q is not supported by Cohere", part.Type)
		}
	}
	if len(content) == 0 {
		return nil, nil
	}
	return content, nil
}

func cohereToolResult(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed != "" && json.Valid([]byte(trimmed)) && strings.HasPrefix(trimmed, "{") {
		return trimmed
	}
	body, _ := json.Marshal(map[string]string{"result": text})
	return string(body)
}

func toCohereTools(tools []map[string]any, toolChoice any) ([]map[string]any, string, error) {
	converted := make([]map[string]any, 0, len(tools))
	for i, tool := range tools {
		function, ok := tool["function"].(map[string]any)
		if !ok {
			return nil, "", core.NewInvalidRequestError(fmt.Sprintf("tools[%d].function must be an object", i), nil)
		}
		name, _ := function["name"].(string)
		if strings.TrimSpace(name) == "" {
			return nil, "", core.NewInvalidRequestError(fmt.Sprintf("tools[%d].function.name is required", i), nil)
		}
		clonedFunction := make(map[string]any, len(function)+1)
		maps.Copy(clonedFunction, function)
		if _, ok := clonedFunction["parameters"]; !ok {
			clonedFunction["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		converted = append(converted, map[string]any{"type": "function", "function": clonedFunction})
	}

	choice, selectedName, disableTools, err := cohereToolChoice(toolChoice)
	if err != nil {
		return nil, "", err
	}
	if disableTools {
		return nil, "NONE", nil
	}
	if selectedName != "" {
		for _, tool := range converted {
			function := tool["function"].(map[string]any)
			if function["name"] == selectedName {
				return []map[string]any{tool}, "REQUIRED", nil
			}
		}
		return nil, "", core.NewInvalidRequestError("tool_choice references an unknown tool", nil)
	}
	if choice != "" && len(converted) == 0 {
		return nil, "", core.NewInvalidRequestError("tool_choice requires at least one tool", nil)
	}
	return converted, choice, nil
}

func cohereToolChoice(value any) (choice, selectedName string, disableTools bool, err error) {
	switch choiceValue := value.(type) {
	case nil:
		return "", "", false, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(choiceValue)) {
		case "", "auto":
			return "", "", false, nil
		case "required", "any":
			return "REQUIRED", "", false, nil
		case "none":
			return "NONE", "", true, nil
		default:
			return "", "", false, core.NewInvalidRequestError("unsupported tool_choice value", nil)
		}
	case map[string]any:
		choiceType, _ := choiceValue["type"].(string)
		switch strings.ToLower(strings.TrimSpace(choiceType)) {
		case "auto":
			return "", "", false, nil
		case "required", "any":
			return "REQUIRED", "", false, nil
		case "none":
			return "NONE", "", true, nil
		case "function":
			function, _ := choiceValue["function"].(map[string]any)
			name, _ := function["name"].(string)
			if strings.TrimSpace(name) == "" {
				return "", "", false, core.NewInvalidRequestError("tool_choice.function.name is required", nil)
			}
			return "", name, false, nil
		default:
			return "", "", false, core.NewInvalidRequestError("unsupported tool_choice type", nil)
		}
	default:
		return "", "", false, core.NewInvalidRequestError("tool_choice must be a string or object", nil)
	}
}

func copyChatExtraFields(out *chatRequest, fields core.UnknownJSONFields) {
	out.StopSequences = stopSequences(fields.Lookup("stop"))
	if len(out.StopSequences) == 0 {
		out.StopSequences = fields.Lookup("stop_sequences")
	}
	out.Seed = fields.Lookup("seed")
	out.FrequencyPenalty = fields.Lookup("frequency_penalty")
	out.PresencePenalty = fields.Lookup("presence_penalty")
	out.K = fields.Lookup("top_k")
	if len(out.K) == 0 {
		out.K = fields.Lookup("k")
	}
	out.Logprobs = fields.Lookup("logprobs")
	out.ResponseFormat = fields.Lookup("response_format")
	out.SafetyMode = fields.Lookup("safety_mode")
	out.Documents = fields.Lookup("documents")
	out.CitationOptions = fields.Lookup("citation_options")
	out.StrictTools = fields.Lookup("strict_tools")
	out.Thinking = fields.Lookup("thinking")
	out.Priority = fields.Lookup("priority")
}

func stopSequences(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var single string
	if json.Unmarshal(raw, &single) == nil {
		encoded, _ := json.Marshal([]string{single})
		return encoded
	}
	return raw
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func fromCohereChatResponse(resp *chatResponse, requestedModel string) *core.ChatResponse {
	var text, thinking strings.Builder
	for _, part := range resp.Message.Content {
		switch part.Type {
		case "text":
			appendBlock(&text, part.Text)
		case "thinking":
			appendBlock(&thinking, part.Thinking)
		}
	}
	message := core.ResponseMessage{
		Role:      "assistant",
		Content:   text.String(),
		ToolCalls: fromCohereToolCalls(resp.Message.ToolCalls),
	}
	extra := make(map[string]json.RawMessage)
	if thinking.Len() > 0 {
		extra["reasoning_content"], _ = json.Marshal(thinking.String())
	}
	if resp.Message.ToolPlan != "" {
		extra["tool_plan"], _ = json.Marshal(resp.Message.ToolPlan)
	}
	if len(resp.Message.Citations) > 0 && string(resp.Message.Citations) != "null" {
		extra["citations"] = core.CloneRawJSON(resp.Message.Citations)
	}
	if len(extra) > 0 {
		message.ExtraFields = core.UnknownJSONFieldsFromMap(extra)
	}
	return &core.ChatResponse{
		ID:       resp.ID,
		Object:   "chat.completion",
		Model:    requestedModel,
		Provider: "cohere",
		Created:  time.Now().Unix(),
		Choices: []core.Choice{{
			Index:        0,
			Message:      message,
			FinishReason: normalizeFinishReason(resp.FinishReason),
		}},
		Usage: toCoreUsage(resp.Usage),
	}
}

func appendBlock(builder *strings.Builder, value string) {
	if value == "" {
		return
	}
	if builder.Len() > 0 {
		builder.WriteString("\n\n")
	}
	builder.WriteString(value)
}

func fromCohereToolCalls(calls []toolCall) []core.ToolCall {
	if len(calls) == 0 {
		return nil
	}
	out := make([]core.ToolCall, 0, len(calls))
	for _, call := range calls {
		arguments := call.Function.Arguments
		if strings.TrimSpace(arguments) == "" {
			arguments = "{}"
		}
		out = append(out, core.ToolCall{
			ID:   call.ID,
			Type: "function",
			Function: core.FunctionCall{
				Name:      call.Function.Name,
				Arguments: arguments,
			},
		})
	}
	return out
}

func normalizeFinishReason(reason string) string {
	switch strings.ToUpper(strings.TrimSpace(reason)) {
	case "MAX_TOKENS":
		return "length"
	case "TOOL_CALL":
		return "tool_calls"
	case "COMPLETE", "STOP_SEQUENCE", "ERROR", "TIMEOUT", "":
		return "stop"
	default:
		return "stop"
	}
}

func toCoreUsage(value usage) core.Usage {
	input := int(value.Tokens.InputTokens)
	output := int(value.Tokens.OutputTokens)
	if input == 0 && output == 0 {
		input = int(value.BilledUnits.InputTokens)
		output = int(value.BilledUnits.OutputTokens)
	}
	raw := map[string]any{}
	if value.BilledUnits.InputTokens != 0 ||
		value.BilledUnits.OutputTokens != 0 ||
		value.BilledUnits.SearchUnits != 0 ||
		value.BilledUnits.Classifications != 0 {
		raw["billed_units"] = value.BilledUnits
	}
	if value.CachedTokens != 0 {
		raw["cached_tokens"] = value.CachedTokens
	}
	result := core.Usage{
		PromptTokens:     input,
		CompletionTokens: output,
		TotalTokens:      input + output,
	}
	if value.CachedTokens != 0 {
		result.PromptTokensDetails = &core.PromptTokensDetails{CachedTokens: int(value.CachedTokens)}
	}
	if len(raw) > 0 {
		result.RawUsage = raw
	}
	return result
}
