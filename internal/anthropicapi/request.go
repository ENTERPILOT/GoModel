package anthropicapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"gomodel/internal/core"
)

// DecodeMessagesRequest parses an Anthropic Messages API request body.
func DecodeMessagesRequest(body []byte) (*MessagesRequest, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("request body is empty")
	}
	var req MessagesRequest
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	if err := dec.Decode(&req); err != nil {
		return nil, err
	}
	return &req, nil
}

// ToChatRequest translates an Anthropic Messages request into the canonical
// chat request. The translation is provider-agnostic: the resulting request
// runs through the standard chat-completions pipeline.
func ToChatRequest(req *MessagesRequest) (*core.ChatRequest, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("messages request is required", nil)
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, core.NewInvalidRequestError("model is required", nil).WithParam("model")
	}
	if req.MaxTokens <= 0 {
		return nil, core.NewInvalidRequestError("max_tokens must be a positive integer", nil).WithParam("max_tokens")
	}
	if len(req.Messages) == 0 {
		return nil, core.NewInvalidRequestError("messages must not be empty", nil).WithParam("messages")
	}

	messages, err := convertMessages(req)
	if err != nil {
		return nil, err
	}

	maxTokens := req.MaxTokens
	chat := &core.ChatRequest{
		Model:       req.Model,
		Messages:    messages,
		MaxTokens:   &maxTokens,
		Temperature: req.Temperature,
		Stream:      req.Stream,
		Reasoning:   thinkingToReasoning(req.Thinking),
	}
	if req.Stream {
		chat.StreamOptions = &core.StreamOptions{IncludeUsage: true}
	}

	tools, err := convertTools(req.Tools)
	if err != nil {
		return nil, err
	}
	chat.Tools = tools
	toolChoice, parallel := convertToolChoice(req.ToolChoice)
	chat.ToolChoice = toolChoice
	chat.ParallelToolCalls = parallel

	if extra := buildExtraFields(req); !extra.IsEmpty() {
		chat.ExtraFields = extra
	}
	return chat, nil
}

// convertMessages flattens the Anthropic system prompt and messages into the
// canonical message list. A single Anthropic message may expand into multiple
// canonical messages: tool_result blocks become standalone role:"tool" messages.
func convertMessages(req *MessagesRequest) ([]core.Message, error) {
	out := make([]core.Message, 0, len(req.Messages)+1)

	if system := systemText(req.System); system != "" {
		out = append(out, core.Message{Role: "system", Content: system})
	}

	for i, msg := range req.Messages {
		text, blocks, err := parseContent(msg.Content)
		if err != nil {
			return nil, core.NewInvalidRequestError(fmt.Sprintf("messages[%d].content: %v", i, err), err)
		}
		if blocks == nil {
			out = append(out, core.Message{Role: normalizeRole(msg.Role), Content: text})
			continue
		}
		converted, err := convertBlockMessage(msg.Role, blocks)
		if err != nil {
			return nil, core.NewInvalidRequestError(fmt.Sprintf("messages[%d]: %v", i, err), err)
		}
		out = append(out, converted...)
	}
	return out, nil
}

// convertBlockMessage converts one Anthropic block-content message. tool_result
// blocks are emitted as separate role:"tool" messages (OpenAI representation);
// text/image blocks and tool_use blocks collapse into a single user/assistant
// message.
func convertBlockMessage(role string, blocks []ContentBlock) ([]core.Message, error) {
	var (
		toolMessages []core.Message
		parts        []core.ContentPart
		toolCalls    []core.ToolCall
	)
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				parts = append(parts, core.ContentPart{Type: "text", Text: block.Text})
			}
		case "image":
			url, err := imageURLFromSource(block.Source)
			if err != nil {
				return nil, err
			}
			parts = append(parts, core.ContentPart{
				Type:     "image_url",
				ImageURL: &core.ImageURLContent{URL: url},
			})
		case "tool_use":
			if strings.TrimSpace(block.Name) == "" {
				return nil, fmt.Errorf("tool_use block is missing name")
			}
			toolCalls = append(toolCalls, core.ToolCall{
				ID:       block.ID,
				Type:     "function",
				Function: core.FunctionCall{Name: block.Name, Arguments: rawToArguments(block.Input)},
			})
		case "tool_result":
			id := strings.TrimSpace(block.ToolUseID)
			if id == "" {
				return nil, fmt.Errorf("tool_result block is missing tool_use_id")
			}
			toolMessages = append(toolMessages, core.Message{
				Role:       "tool",
				ToolCallID: id,
				Content:    toolResultText(block.Content),
			})
		case "thinking", "redacted_thinking":
			// Extended-thinking history has no canonical chat equivalent; drop
			// it. It is an assistant-side artifact, so dropping it does not lose
			// caller intent.
		default:
			// Block types that carry caller payload (e.g. document) have no
			// canonical chat equivalent. Reject them rather than silently
			// dropping the data, which would make the model answer as if the
			// attachment were never sent.
			return nil, fmt.Errorf("unsupported content block type %q; use the /p/anthropic passthrough for provider-native features", block.Type)
		}
	}

	messages := toolMessages
	if content := collapseParts(parts); content != nil || len(toolCalls) > 0 {
		messages = append(messages, core.Message{
			Role:      normalizeRole(role),
			Content:   content,
			ToolCalls: toolCalls,
		})
	}
	return messages, nil
}

// collapseParts reduces content parts to a plain string when they are all text,
// and otherwise returns the typed part slice. It returns nil when there is no
// content at all.
func collapseParts(parts []core.ContentPart) core.MessageContent {
	if len(parts) == 0 {
		return nil
	}
	onlyText := true
	for _, part := range parts {
		if part.Type != "text" {
			onlyText = false
			break
		}
	}
	if onlyText {
		texts := make([]string, 0, len(parts))
		for _, part := range parts {
			texts = append(texts, part.Text)
		}
		return strings.Join(texts, "\n")
	}
	return parts
}

func normalizeRole(role string) string {
	if role == "assistant" {
		return "assistant"
	}
	return "user"
}

// parseContent decodes a polymorphic Anthropic content value. When the value is
// a string, blocks is nil and text holds the string. When it is an array,
// blocks is non-nil (possibly empty).
func parseContent(raw json.RawMessage) (text string, blocks []ContentBlock, err error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", nil, nil
	}
	switch trimmed[0] {
	case '"':
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return "", nil, err
		}
		return text, nil, nil
	case '[':
		decoded := []ContentBlock{}
		if err := json.Unmarshal(trimmed, &decoded); err != nil {
			return "", nil, err
		}
		return "", decoded, nil
	default:
		return "", nil, fmt.Errorf("must be a string or an array of content blocks")
	}
}

// systemText flattens the Anthropic system field (string or text-block array)
// into a single string.
func systemText(raw json.RawMessage) string {
	text, blocks, err := parseContent(raw)
	if err != nil {
		return ""
	}
	if blocks == nil {
		return strings.TrimSpace(text)
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

// toolResultText extracts the text payload of a tool_result block content,
// which itself may be a string or an array of (text) blocks.
func toolResultText(raw json.RawMessage) string {
	text, blocks, err := parseContent(raw)
	if err != nil {
		return ""
	}
	if blocks == nil {
		return text
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func imageURLFromSource(source *Source) (string, error) {
	if source == nil {
		return "", fmt.Errorf("image block is missing source")
	}
	switch source.Type {
	case "base64":
		if source.MediaType == "" || source.Data == "" {
			return "", fmt.Errorf("base64 image source requires media_type and data")
		}
		return "data:" + source.MediaType + ";base64," + source.Data, nil
	case "url":
		if source.URL == "" {
			return "", fmt.Errorf("url image source requires url")
		}
		return source.URL, nil
	default:
		return "", fmt.Errorf("unsupported image source type %q", source.Type)
	}
}

// rawToArguments renders a tool_use input object as a compact JSON string,
// the form expected by core.FunctionCall.Arguments.
func rawToArguments(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "{}"
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return "{}"
	}
	return compact.String()
}

func convertTools(tools []Tool) ([]map[string]any, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	out := make([]map[string]any, 0, len(tools))
	for i, tool := range tools {
		if strings.TrimSpace(tool.Name) == "" {
			return nil, core.NewInvalidRequestError(fmt.Sprintf("tools[%d].name is required", i), nil)
		}
		function := map[string]any{"name": tool.Name}
		if tool.Description != "" {
			function["description"] = tool.Description
		}
		if len(bytes.TrimSpace(tool.InputSchema)) > 0 {
			var schema any
			if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
				return nil, core.NewInvalidRequestError(fmt.Sprintf("tools[%d].input_schema: %v", i, err), err)
			}
			function["parameters"] = schema
		}
		out = append(out, map[string]any{"type": "function", "function": function})
	}
	return out, nil
}

// convertToolChoice maps an Anthropic tool_choice to its OpenAI equivalent and
// the parallel_tool_calls flag.
func convertToolChoice(choice *ToolChoice) (any, *bool) {
	if choice == nil {
		return nil, nil
	}
	var parallel *bool
	if choice.DisableParallelToolUse != nil && *choice.DisableParallelToolUse {
		disabled := false
		parallel = &disabled
	}
	switch choice.Type {
	case "auto":
		return "auto", parallel
	case "any":
		return "required", parallel
	case "none":
		return "none", parallel
	case "tool":
		if strings.TrimSpace(choice.Name) != "" {
			return map[string]any{
				"type":     "function",
				"function": map[string]any{"name": choice.Name},
			}, parallel
		}
		return "auto", parallel
	default:
		return nil, parallel
	}
}

// thinkingToReasoning maps Anthropic extended-thinking config onto the canonical
// reasoning effort. Budget thresholds mirror the anthropic provider's
// effort-to-budget mapping.
func thinkingToReasoning(thinking *Thinking) *core.Reasoning {
	if thinking == nil || thinking.Type == "" || thinking.Type == "disabled" {
		return nil
	}
	effort := "low"
	switch {
	case thinking.Type == "adaptive":
		effort = "medium"
	case thinking.BudgetTokens >= 20000:
		effort = "high"
	case thinking.BudgetTokens >= 10000:
		effort = "medium"
	}
	return &core.Reasoning{Effort: effort}
}

// buildExtraFields carries Anthropic request fields that have no first-class
// canonical field through as OpenAI-compatible extra fields.
func buildExtraFields(req *MessagesRequest) core.UnknownJSONFields {
	fields := map[string]json.RawMessage{}
	add := func(key string, value any) {
		if raw, err := json.Marshal(value); err == nil {
			fields[key] = raw
		}
	}
	if len(req.StopSequences) > 0 {
		add("stop", req.StopSequences)
	}
	if req.TopP != nil {
		add("top_p", *req.TopP)
	}
	if req.TopK != nil {
		add("top_k", *req.TopK)
	}
	if req.Metadata != nil && strings.TrimSpace(req.Metadata.UserID) != "" {
		add("user", req.Metadata.UserID)
	}
	return core.UnknownJSONFieldsFromMap(fields)
}

// EstimateInputTokens returns a provider-agnostic heuristic estimate of the
// input token count for a Messages request (roughly characters / 4). It is an
// approximation, not a tokenizer-exact count.
func EstimateInputTokens(req *MessagesRequest) int {
	if req == nil {
		return 0
	}
	chars := len(systemText(req.System))
	for _, msg := range req.Messages {
		text, blocks, err := parseContent(msg.Content)
		if err != nil {
			continue
		}
		chars += len(text)
		for _, block := range blocks {
			chars += len(block.Text) + len(block.Thinking)
			chars += len(bytes.TrimSpace(block.Input))
			chars += len(toolResultText(block.Content))
		}
	}
	for _, tool := range req.Tools {
		chars += len(tool.Name) + len(tool.Description) + len(bytes.TrimSpace(tool.InputSchema))
	}
	tokens := (chars + 3) / 4
	if tokens == 0 && chars > 0 {
		return 1
	}
	return tokens
}
