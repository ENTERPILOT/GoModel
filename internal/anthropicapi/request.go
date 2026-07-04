package anthropicapi

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/goccy/go-json"

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
	// Require the body to hold exactly one JSON value: decoding again must
	// reach EOF. This rejects any trailing bytes (a second object, stray
	// brackets, garbage) so a malformed body cannot look valid while
	// audit/cache inputs disagree with the parsed request.
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		return nil, fmt.Errorf("request body must contain a single JSON object")
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
		TopP:        req.TopP,
		Stream:      req.Stream,
		Reasoning:   thinkingToReasoning(req.Thinking),
	}
	if req.Metadata != nil && strings.TrimSpace(req.Metadata.UserID) != "" {
		chat.User = req.Metadata.UserID
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
// Messages with role "system" in the messages array are extracted and prepended
// to the system prompt, as the Anthropic Messages API requires system content
// to be in the top-level system field, not in messages.
func convertMessages(req *MessagesRequest) ([]core.Message, error) {
	out := make([]core.Message, 0, len(req.Messages)+1)

	// Collect system messages from the messages array
	var systemMessages []string
	filteredMessages := make([]Message, 0, len(req.Messages))
	for _, msg := range req.Messages {
		if msg.Role == "system" {
			// Extract system content from message
			text, err := systemText(msg.Content)
			if err != nil {
				return nil, core.NewInvalidRequestError("system message content: "+err.Error(), err)
			}
			if text != "" {
				systemMessages = append(systemMessages, text)
			}
		} else {
			filteredMessages = append(filteredMessages, msg)
		}
	}

	// Build the system prompt by combining existing system field and system messages
	system, err := systemText(req.System)
	if err != nil {
		return nil, core.NewInvalidRequestError(err.Error(), err)
	}
	if len(systemMessages) > 0 {
		if system != "" {
			system = strings.TrimSpace(system + "\n\n" + strings.Join(systemMessages, "\n\n"))
		} else {
			system = strings.TrimSpace(strings.Join(systemMessages, "\n\n"))
		}
	}
	if system != "" {
		out = append(out, core.Message{Role: "system", Content: system})
	}

	for i, msg := range filteredMessages {
		if msg.Role != "user" && msg.Role != "assistant" {
			return nil, core.NewInvalidRequestError(
				fmt.Sprintf("messages[%d].role must be \"user\" or \"assistant\"", i), nil)
		}
		text, blocks, err := parseContent(msg.Content)
		if err != nil {
			return nil, core.NewInvalidRequestError(fmt.Sprintf("messages[%d].content: %v", i, err), err)
		}
		if blocks == nil {
			out = append(out, core.Message{Role: msg.Role, Content: text})
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
			content, err := toolResultText(block.Content)
			if err != nil {
				return nil, err
			}
			toolMessages = append(toolMessages, core.Message{
				Role:       "tool",
				ToolCallID: id,
				Content:    content,
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
			return nil, fmt.Errorf("unsupported content block type %q; use the /p/anthropic/v1/messages passthrough for provider-native features", block.Type)
		}
	}

	messages := toolMessages
	if content := collapseParts(parts); content != nil || len(toolCalls) > 0 {
		messages = append(messages, core.Message{
			Role:      role,
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

// parseContent decodes a polymorphic Anthropic content value. When the value is
// a string, blocks is nil and text holds the string. When it is an array,
// blocks is non-nil (possibly empty).
func parseContent(raw json.RawMessage) (text string, blocks []ContentBlock, err error) {
	trimmed := bytes.TrimSpace(raw)
	if core.IsJSONNull(trimmed) {
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
// into a single string. A present but malformed system value is an error
// rather than silently dropped: the model must not run without the caller's
// instructions.
func systemText(raw json.RawMessage) (string, error) {
	text, blocks, err := parseContent(raw)
	if err != nil {
		return "", fmt.Errorf("system: %v", err)
	}
	if blocks == nil {
		return strings.TrimSpace(text), nil
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type != "text" {
			return "", fmt.Errorf("system block type %q is not supported; only text is allowed", block.Type)
		}
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n")), nil
}

// toolResultText extracts the text payload of a tool_result block content,
// which itself may be a string or an array of text blocks. A present but
// malformed or non-text tool_result content is an error rather than silently
// dropped: the downstream provider must not receive an empty tool response.
func toolResultText(raw json.RawMessage) (string, error) {
	text, blocks, err := parseContent(raw)
	if err != nil {
		return "", fmt.Errorf("tool_result content: %v", err)
	}
	if blocks == nil {
		return text, nil
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type != "text" {
			return "", fmt.Errorf("tool_result content block type %q is not supported; only text is allowed", block.Type)
		}
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n"), nil
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
	if core.IsJSONNull(trimmed) {
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
		// A non-empty type other than "custom" marks an Anthropic server/
		// built-in tool (web search, code execution, …). These have no
		// canonical chat equivalent; reject them rather than mistranslating
		// them into a phantom custom function the gateway cannot execute.
		if t := strings.TrimSpace(tool.Type); t != "" && t != "custom" {
			return nil, core.NewInvalidRequestError(fmt.Sprintf("tools[%d]: server tool type %q is not supported; use the /p/anthropic/v1/messages passthrough for provider-native tools", i, tool.Type), nil)
		}
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

// buildExtraFields carries Anthropic request fields that have a portable
// OpenAI-compatible equivalent but no typed core.ChatRequest field. Fields
// with typed equivalents (top_p, user) are set directly on the ChatRequest in
// ToChatRequest so internal consumers of the typed fields see them too.
//
// top_k is deliberately not carried: it is not a valid OpenAI Chat Completions
// parameter, and the OpenAI-family providers forward request fields verbatim
// and reject unknown ones with a 400. Carrying it would make any request with
// top_k fail when routed to those providers, so it is dropped (see ADR-0007).
func buildExtraFields(req *MessagesRequest) core.UnknownJSONFields {
	fields := map[string]json.RawMessage{}
	if len(req.StopSequences) > 0 {
		if raw, err := json.Marshal(req.StopSequences); err == nil {
			fields["stop"] = raw
		}
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
	// Errors are ignored here: count_tokens is a best-effort heuristic and
	// must not fail on malformed sub-fields that ToChatRequest would reject.
	system, _ := systemText(req.System)
	chars := len(system)
	for _, msg := range req.Messages {
		text, blocks, err := parseContent(msg.Content)
		if err != nil {
			continue
		}
		chars += len(text)
		for _, block := range blocks {
			chars += len(block.Text) + len(block.Thinking)
			chars += len(bytes.TrimSpace(block.Input))
			result, _ := toolResultText(block.Content)
			chars += len(result)
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
