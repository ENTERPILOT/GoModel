package anthropicapi

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
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
// runs through the standard chat-completions pipeline. Content the canonical
// request cannot represent is rejected with a 400 rather than dropped.
func ToChatRequest(req *MessagesRequest) (*core.ChatRequest, error) {
	return toChatRequest(req, false)
}

// ToChatRequestLenient is ToChatRequest for routing decisions only: blocks and
// tools that have no canonical equivalent (server tools, server-tool results,
// …) are dropped instead of rejected. The result carries enough of the request
// (model, stream, messages) to resolve a workflow and decide whether the
// original body can be forwarded natively; it must not be dispatched through
// the translated pipeline.
func ToChatRequestLenient(req *MessagesRequest) (*core.ChatRequest, error) {
	return toChatRequest(req, true)
}

func toChatRequest(req *MessagesRequest, lenient bool) (*core.ChatRequest, error) {
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
	if _, err := anthropicCacheControlExtra(req.CacheControl); err != nil {
		return nil, core.NewInvalidRequestError(err.Error(), err).WithParam("cache_control")
	}

	messages, err := convertMessages(req, lenient)
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

	tools, err := convertTools(req.Tools, lenient)
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
// Messages with role "system" stay at their position in the conversation:
// Claude 4.8+/5-family models accept them natively, and clients (notably
// Claude Code) rely on their placement and block-level cache_control
// breakpoints for prompt caching. The Anthropic egress translator hoists them
// into the top-level system field only for models that reject the role.
func convertMessages(req *MessagesRequest, lenient bool) ([]core.Message, error) {
	out := make([]core.Message, 0, len(req.Messages)+1)

	// Build the system prompt while retaining block-level cache_control metadata.
	system, err := systemContent(req.System, lenient)
	if err != nil {
		return nil, core.NewInvalidRequestError(err.Error(), err)
	}
	if system != nil && core.ExtractTextContent(system) != "" {
		out = append(out, core.Message{Role: "system", Content: system})
	}

	for i, msg := range req.Messages {
		if msg.Role == "system" {
			content, err := systemContent(msg.Content, lenient)
			if err != nil {
				return nil, core.NewInvalidRequestError(fmt.Sprintf("messages[%d]: %v", i, err), err)
			}
			if content == nil || core.ExtractTextContent(content) == "" {
				continue
			}
			out = append(out, core.Message{Role: "system", Content: content})
			continue
		}
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
		converted, err := convertBlockMessage(msg.Role, blocks, lenient)
		if err != nil {
			return nil, core.NewInvalidRequestError(fmt.Sprintf("messages[%d]: %v", i, err), err)
		}
		out = append(out, converted...)
	}
	return out, nil
}

// convertBlockMessage converts one Anthropic block-content message. tool_result
// blocks become standalone role:"tool" messages that precede the remaining
// content, matching the OpenAI ordering where tool responses follow the
// assistant tool call and any user follow-up comes after. Assistant thinking
// blocks are preserved verbatim on the message so the Anthropic provider can
// replay them; they have no meaning for other providers and are stripped
// before the request reaches one.
func convertBlockMessage(role string, blocks []ContentBlock, lenient bool) ([]core.Message, error) {
	var (
		toolMessages []core.Message
		parts        []core.ContentPart
		toolCalls    []core.ToolCall
		thinking     []json.RawMessage
	)
	for _, block := range blocks {
		extra, err := anthropicCacheControlExtra(block.CacheControl)
		if err != nil {
			return nil, err
		}
		switch block.Type {
		case "text", "image", "document", "search_result":
			part, ok, err := payloadPart(block, extra)
			if err != nil {
				return nil, err
			}
			if ok {
				parts = append(parts, part)
			}
		case "tool_use":
			if strings.TrimSpace(block.Name) == "" {
				return nil, fmt.Errorf("tool_use block is missing name")
			}
			toolCalls = append(toolCalls, core.ToolCall{
				ID:          block.ID,
				Type:        "function",
				Function:    core.FunctionCall{Name: block.Name, Arguments: rawToArguments(block.Input)},
				ExtraFields: extra,
			})
		case "tool_result":
			id := strings.TrimSpace(block.ToolUseID)
			if id == "" {
				return nil, fmt.Errorf("tool_result block is missing tool_use_id")
			}
			content, err := toolResultContent(block.Content, lenient)
			if err != nil {
				return nil, err
			}
			toolMessages = append(toolMessages, core.Message{
				Role:        "tool",
				ToolCallID:  id,
				Content:     content,
				ExtraFields: toolResultExtra(extra, block.IsError),
			})
		case "thinking", "redacted_thinking":
			// Only assistant turns carry thinking; anywhere else it is an
			// artifact with nothing to replay.
			if role == "assistant" {
				thinking = append(thinking, thinkingBlockJSON(block))
			}
		default:
			if lenient {
				continue
			}
			// Block types that carry caller payload (server-tool results,
			// container uploads, …) have no canonical chat equivalent. Reject
			// them rather than silently dropping the data, which would make
			// the model answer as if the content were never sent.
			return nil, fmt.Errorf("unsupported content block type %q; use the /p/anthropic/v1/messages passthrough for provider-native features", block.Type)
		}
	}

	messages := toolMessages
	content := collapseParts(parts)
	if content != nil || len(toolCalls) > 0 || len(thinking) > 0 {
		msg := core.Message{
			Role:      role,
			Content:   content,
			ToolCalls: toolCalls,
		}
		if len(thinking) > 0 {
			raw, err := json.Marshal(thinking)
			if err != nil {
				return nil, fmt.Errorf("thinking blocks: %v", err)
			}
			msg.ExtraFields = core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				core.ThinkingBlocksField: raw,
			})
		}
		messages = append(messages, msg)
	}
	return messages, nil
}

// payloadPart converts a payload-carrying block (text, image, document,
// search_result) into a canonical content part. ok is false when the block is
// empty and contributes nothing.
func payloadPart(block ContentBlock, extra core.UnknownJSONFields) (core.ContentPart, bool, error) {
	switch block.Type {
	case "text":
		if block.Text == "" {
			return core.ContentPart{}, false, nil
		}
		return core.ContentPart{Type: "text", Text: block.Text, ExtraFields: extra}, true, nil
	case "image":
		source, err := decodeSource(block.Source)
		if err != nil {
			return core.ContentPart{}, false, fmt.Errorf("image block: %v", err)
		}
		url, err := imageURLFromSource(source)
		if err != nil {
			return core.ContentPart{}, false, err
		}
		return core.ContentPart{
			Type:        "image_url",
			ImageURL:    &core.ImageURLContent{URL: url},
			ExtraFields: extra,
		}, true, nil
	case "document":
		return documentPart(block, extra)
	case "search_result":
		text, err := searchResultText(block)
		if err != nil {
			return core.ContentPart{}, false, err
		}
		if text == "" {
			return core.ContentPart{}, false, nil
		}
		return core.ContentPart{Type: "text", Text: text, ExtraFields: extra}, true, nil
	default:
		return core.ContentPart{}, false, fmt.Errorf("unsupported content block type %q", block.Type)
	}
}

// documentPart maps an Anthropic document block onto the canonical file part.
// PDF and plain-text sources become data: URLs, URL and file_id sources are
// carried as-is, and the custom-content variant (an array of text blocks)
// degrades to a text part. The document title becomes the filename; citation
// settings and context have no canonical equivalent and are dropped.
func documentPart(block ContentBlock, extra core.UnknownJSONFields) (core.ContentPart, bool, error) {
	source, err := decodeSource(block.Source)
	if err != nil {
		return core.ContentPart{}, false, fmt.Errorf("document block: %v", err)
	}
	if source == nil {
		return core.ContentPart{}, false, fmt.Errorf("document block is missing source")
	}
	file := &core.FileContent{Filename: strings.TrimSpace(block.Title)}
	switch source.Type {
	case "base64":
		if source.MediaType == "" || source.Data == "" {
			return core.ContentPart{}, false, fmt.Errorf("base64 document source requires media_type and data")
		}
		file.FileData = "data:" + source.MediaType + ";base64," + source.Data
	case "text":
		if source.Data == "" {
			return core.ContentPart{}, false, fmt.Errorf("text document source requires data")
		}
		mediaType := source.MediaType
		if mediaType == "" {
			mediaType = "text/plain"
		}
		file.FileData = "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString([]byte(source.Data))
	case "url":
		if source.URL == "" {
			return core.ContentPart{}, false, fmt.Errorf("url document source requires url")
		}
		file.FileURL = source.URL
	case "file":
		if strings.TrimSpace(source.FileID) == "" {
			return core.ContentPart{}, false, fmt.Errorf("file document source requires file_id")
		}
		file.FileID = strings.TrimSpace(source.FileID)
	case "content":
		text, err := textBlocksText(source.Content)
		if err != nil {
			return core.ContentPart{}, false, fmt.Errorf("document content: %v", err)
		}
		if title := strings.TrimSpace(block.Title); title != "" && text != "" {
			text = title + "\n\n" + text
		}
		if text == "" {
			return core.ContentPart{}, false, nil
		}
		return core.ContentPart{Type: "text", Text: text, ExtraFields: extra}, true, nil
	default:
		return core.ContentPart{}, false, fmt.Errorf("unsupported document source type %q", source.Type)
	}
	return core.ContentPart{Type: "file", File: file, ExtraFields: extra}, true, nil
}

// searchResultText flattens a search_result block (source URL, title, text
// content) into text. Citation metadata has no canonical equivalent.
func searchResultText(block ContentBlock) (string, error) {
	var source string
	if trimmed := bytes.TrimSpace(block.Source); len(trimmed) > 0 && trimmed[0] == '"' {
		if err := json.Unmarshal(trimmed, &source); err != nil {
			return "", fmt.Errorf("search_result source: %v", err)
		}
	}
	body, err := textBlocksText(block.Content)
	if err != nil {
		return "", fmt.Errorf("search_result content: %v", err)
	}
	var lines []string
	if title := strings.TrimSpace(block.Title); title != "" {
		lines = append(lines, "Title: "+title)
	}
	if source = strings.TrimSpace(source); source != "" {
		lines = append(lines, "Source: "+source)
	}
	if body != "" {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, body)
	}
	return strings.Join(lines, "\n"), nil
}

// textBlocksText joins the text of a string-or-text-block-array content value.
func textBlocksText(raw json.RawMessage) (string, error) {
	text, blocks, err := parseContent(raw)
	if err != nil {
		return "", err
	}
	if blocks == nil {
		return text, nil
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type != "text" {
			return "", fmt.Errorf("block type %q is not supported; only text is allowed", block.Type)
		}
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n"), nil
}

// thinkingBlockJSON re-encodes a thinking block exactly as Anthropic expects
// it back: thinking blocks always carry their text (possibly empty) and
// signature, redacted blocks carry their opaque data.
func thinkingBlockJSON(block ContentBlock) json.RawMessage {
	var raw []byte
	if block.Type == "redacted_thinking" {
		raw, _ = json.Marshal(struct {
			Type string `json:"type"`
			Data string `json:"data"`
		}{Type: block.Type, Data: block.Data})
	} else {
		raw, _ = json.Marshal(struct {
			Type      string `json:"type"`
			Thinking  string `json:"thinking"`
			Signature string `json:"signature,omitempty"`
		}{Type: block.Type, Thinking: block.Thinking, Signature: block.Signature})
	}
	return raw
}

// toolResultExtra adds the is_error marker to a tool message's extras.
func toolResultExtra(extra core.UnknownJSONFields, isError bool) core.UnknownJSONFields {
	if !isError {
		return extra
	}
	fields := map[string]json.RawMessage{core.ToolResultIsErrorField: json.RawMessage("true")}
	if raw := extra.Lookup("cache_control"); len(raw) > 0 {
		fields["cache_control"] = raw
	}
	return core.UnknownJSONFieldsFromMap(fields)
}

// decodeSource decodes an image/document source object. A nil result means
// the block had no source.
func decodeSource(raw json.RawMessage) (*Source, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || core.IsJSONNull(trimmed) {
		return nil, nil
	}
	if trimmed[0] != '{' {
		return nil, fmt.Errorf("source must be an object")
	}
	var source Source
	if err := json.Unmarshal(trimmed, &source); err != nil {
		return nil, fmt.Errorf("source: %v", err)
	}
	return &source, nil
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
		if part.Type != "text" || !part.ExtraFields.IsEmpty() {
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
			continue
		}
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n")), nil
}

// systemContent is systemText's metadata-preserving counterpart. It keeps a
// structured representation only when at least one block carries cache_control;
// ordinary system prompts retain the historical compact string representation.
func systemContent(raw json.RawMessage, lenient bool) (core.MessageContent, error) {
	text, blocks, err := parseContent(raw)
	if err != nil {
		return nil, fmt.Errorf("system: %v", err)
	}
	if blocks == nil {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil, nil
		}
		return text, nil
	}
	parts := make([]core.ContentPart, 0, len(blocks))
	hasCacheControl := false
	for _, block := range blocks {
		if block.Type != "text" {
			if lenient {
				continue
			}
			return nil, fmt.Errorf("system block type %q is not supported; only text is allowed", block.Type)
		}
		if block.Text == "" {
			continue
		}
		extra, err := anthropicCacheControlExtra(block.CacheControl)
		if err != nil {
			return nil, err
		}
		if !extra.IsEmpty() {
			hasCacheControl = true
		}
		parts = append(parts, core.ContentPart{Type: "text", Text: block.Text, ExtraFields: extra})
	}
	if len(parts) == 0 {
		return nil, nil
	}
	if !hasCacheControl {
		return strings.TrimSpace(core.ExtractTextContent(parts)), nil
	}
	return parts, nil
}

func anthropicCacheControlExtra(raw json.RawMessage) (core.UnknownJSONFields, error) {
	validated, err := validatedCacheControlJSON(raw)
	if err != nil {
		return core.UnknownJSONFields{}, err
	}
	if len(validated) == 0 {
		return core.UnknownJSONFields{}, nil
	}
	return core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
		"cache_control": validated,
	}), nil
}

func validatedCacheControlJSON(raw json.RawMessage) (json.RawMessage, error) {
	validated, err := core.CloneOptionalJSONObject(raw)
	if err != nil {
		return nil, fmt.Errorf("cache_control must be an object")
	}
	return validated, nil
}

// toolResultContent converts a tool_result block content, which itself may be
// a string or an array of text, image, document, and search_result blocks,
// into canonical message content. Text-only results collapse to a plain
// string; results carrying attachments (Claude Code returns screenshots and
// read image/PDF files this way) keep the structured part list so the egress
// translator can forward them. A present but malformed or otherwise
// unsupported tool_result content is an error rather than silently dropped:
// the downstream provider must not receive an empty or truncated tool
// response.
func toolResultContent(raw json.RawMessage, lenient bool) (core.MessageContent, error) {
	text, blocks, err := parseContent(raw)
	if err != nil {
		return nil, fmt.Errorf("tool_result content: %v", err)
	}
	if blocks == nil {
		return text, nil
	}
	parts := make([]core.ContentPart, 0, len(blocks))
	for _, block := range blocks {
		extra, err := anthropicCacheControlExtra(block.CacheControl)
		if err != nil {
			return nil, fmt.Errorf("tool_result content: %v", err)
		}
		switch block.Type {
		case "text", "image", "document", "search_result":
			part, ok, err := payloadPart(block, extra)
			if err != nil {
				return nil, fmt.Errorf("tool_result content: %v", err)
			}
			if ok {
				parts = append(parts, part)
			}
		default:
			if lenient {
				continue
			}
			return nil, fmt.Errorf("tool_result content block type %q is not supported; only text, image, document, and search_result are allowed", block.Type)
		}
	}
	content := collapseParts(parts)
	if content == nil {
		return "", nil
	}
	return content, nil
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

func convertTools(tools []Tool, lenient bool) ([]map[string]any, error) {
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
			if lenient {
				continue
			}
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
		converted := map[string]any{"type": "function", "function": function}
		raw, err := validatedCacheControlJSON(tool.CacheControl)
		if err != nil {
			return nil, core.NewInvalidRequestError(fmt.Sprintf("tools[%d].%s", i, err), err)
		}
		if len(raw) > 0 {
			var cacheControl any
			if err := json.Unmarshal(raw, &cacheControl); err != nil {
				return nil, core.NewInvalidRequestError(fmt.Sprintf("tools[%d].cache_control: %v", i, err), err)
			}
			converted["cache_control"] = cacheControl
		}
		out = append(out, converted)
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
	if raw := bytes.TrimSpace(req.CacheControl); len(raw) > 0 && !core.IsJSONNull(raw) {
		fields["cache_control"] = core.CloneRawJSON(raw)
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
			result, _ := toolResultContent(block.Content, true)
			chars += len(core.ExtractTextContent(result))
		}
	}
	for _, tool := range req.Tools {
		chars += len(tool.Name) + len(tool.Description) + len(bytes.TrimSpace(tool.InputSchema))
	}
	return tokensFromChars(chars)
}

// EstimateChatInputTokens returns the same chars/4 heuristic for a canonical
// chat request. It seeds the stream converter's message_start usage, where the
// Anthropic contract expects input tokens before the upstream has reported any.
func EstimateChatInputTokens(req *core.ChatRequest) int {
	if req == nil {
		return 0
	}
	chars := 0
	for _, msg := range req.Messages {
		chars += len(core.ExtractTextContent(msg.Content))
		for _, call := range msg.ToolCalls {
			chars += len(call.Function.Name) + len(call.Function.Arguments)
		}
	}
	for _, tool := range req.Tools {
		if raw, err := json.Marshal(tool); err == nil {
			chars += len(raw)
		}
	}
	return tokensFromChars(chars)
}

// tokensFromChars converts a character count to the heuristic token estimate
// (roughly characters / 4, at least 1 for non-empty input).
func tokensFromChars(chars int) int {
	tokens := (chars + 3) / 4
	if tokens == 0 && chars > 0 {
		return 1
	}
	return tokens
}
