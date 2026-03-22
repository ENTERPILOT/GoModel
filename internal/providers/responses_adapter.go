package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"

	"gomodel/internal/core"
)

// ChatProvider is the minimal interface needed by the shared Responses-to-Chat adapter.
// Any provider that supports ChatCompletion and StreamChatCompletion can use the
// ResponsesViaChat and StreamResponsesViaChat helpers to implement the Responses API.
type ChatProvider interface {
	ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error)
	StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error)
}

// ConvertResponsesRequestToChat converts a ResponsesRequest to a ChatRequest.
// It also validates the supported Responses input shapes and returns an error
// when the request cannot be converted safely.
func ConvertResponsesRequestToChat(req *core.ResponsesRequest) (*core.ChatRequest, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("responses request is required", nil)
	}

	chatReq := &core.ChatRequest{
		Model:             req.Model,
		Provider:          req.Provider,
		Messages:          make([]core.Message, 0),
		Tools:             normalizeResponsesToolsForChat(req.Tools),
		ToolChoice:        normalizeResponsesToolChoiceForChat(req.ToolChoice),
		ParallelToolCalls: req.ParallelToolCalls,
		Temperature:       req.Temperature,
		Stream:            req.Stream,
		StreamOptions:     cloneStreamOptions(req.StreamOptions),
		Reasoning:         req.Reasoning,
		ExtraFields:       core.CloneUnknownJSONFields(req.ExtraFields),
	}

	if req.MaxOutputTokens != nil {
		chatReq.MaxTokens = req.MaxOutputTokens
	}

	if req.Instructions != "" {
		chatReq.Messages = append(chatReq.Messages, core.Message{
			Role:    "system",
			Content: req.Instructions,
		})
	}

	messages, err := ConvertResponsesInputToMessages(req.Input)
	if err != nil {
		return nil, err
	}
	chatReq.Messages = append(chatReq.Messages, messages...)

	return chatReq, nil
}

func cloneStreamOptions(src *core.StreamOptions) *core.StreamOptions {
	if src == nil {
		return nil
	}
	cloned := *src
	return &cloned
}

func normalizeResponsesToolsForChat(tools []map[string]any) []map[string]any {
	if len(tools) == 0 {
		return nil
	}

	normalized := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		normalized = append(normalized, normalizeResponsesToolForChat(tool))
	}
	return normalized
}

func normalizeResponsesToolForChat(tool map[string]any) map[string]any {
	if len(tool) == 0 {
		return tool
	}

	toolType, _ := tool["type"].(string)
	if strings.TrimSpace(toolType) != "function" {
		return cloneStringAnyMap(tool)
	}
	if _, ok := tool["function"].(map[string]any); ok {
		return cloneStringAnyMap(tool)
	}

	normalized := cloneStringAnyMap(tool)
	function := map[string]any{}
	for _, key := range []string{"name", "description", "parameters", "strict"} {
		if value, ok := normalized[key]; ok {
			function[key] = value
			delete(normalized, key)
		}
	}
	if len(function) == 0 {
		return normalized
	}

	normalized["function"] = function
	return normalized
}

func normalizeResponsesToolChoiceForChat(choice any) any {
	choiceMap, ok := choice.(map[string]any)
	if !ok {
		return choice
	}

	choiceType, _ := choiceMap["type"].(string)
	if strings.TrimSpace(choiceType) != "function" {
		return choice
	}
	if _, ok := choiceMap["function"].(map[string]any); ok {
		return cloneStringAnyMap(choiceMap)
	}

	name, hasName := choiceMap["name"]
	if !hasName {
		return cloneStringAnyMap(choiceMap)
	}

	normalized := cloneStringAnyMap(choiceMap)
	delete(normalized, "name")
	normalized["function"] = map[string]any{"name": name}
	return normalized
}

func cloneStringAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

// ConvertResponsesInputToMessages converts a Responses API input payload into Chat API messages.
func ConvertResponsesInputToMessages(input interface{}) ([]core.Message, error) {
	switch in := input.(type) {
	case string:
		return []core.Message{{Role: "user", Content: in}}, nil
	case []map[string]any:
		items := make([]interface{}, 0, len(in))
		for _, item := range in {
			items = append(items, item)
		}
		return convertResponsesInputItems(items)
	case []interface{}:
		return convertResponsesInputItems(in)
	case []core.ResponsesInputElement:
		items := make([]interface{}, 0, len(in))
		for _, item := range in {
			items = append(items, item)
		}
		return convertResponsesInputItems(items)
	case nil:
		return nil, core.NewInvalidRequestError("invalid responses input: unsupported type", nil)
	default:
		return nil, core.NewInvalidRequestError("invalid responses input: unsupported type", nil)
	}
}

func convertResponsesInputItems(items []interface{}) ([]core.Message, error) {
	messages := make([]core.Message, 0, len(items))
	var pendingAssistant *core.Message

	flushPendingAssistant := func() {
		if pendingAssistant == nil {
			return
		}
		messages = append(messages, *pendingAssistant)
		pendingAssistant = nil
	}

	for i, item := range items {
		msg, itemType, err := convertResponsesInputItem(item, i)
		if err != nil {
			return nil, err
		}

		if msg.Role == "assistant" {
			if itemType == "message" {
				flushPendingAssistant()
			}
			if pendingAssistant == nil {
				assistant := cloneResponsesMessage(msg)
				pendingAssistant = &assistant
			} else if canMergeAssistantMessages(*pendingAssistant, msg) {
				mergeAssistantMessage(pendingAssistant, msg)
			} else {
				flushPendingAssistant()
				assistant := cloneResponsesMessage(msg)
				pendingAssistant = &assistant
			}
			continue
		}

		flushPendingAssistant()
		messages = append(messages, msg)
	}

	flushPendingAssistant()
	return messages, nil
}

func convertResponsesInputItem(item interface{}, index int) (core.Message, string, error) {
	switch typed := item.(type) {
	case core.ResponsesInputElement:
		return convertResponsesInputElement(typed, index)
	case map[string]interface{}:
		return convertResponsesInputMap(typed, index)
	default:
		return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: expected object", index), nil)
	}
}

func convertResponsesInputElement(item core.ResponsesInputElement, index int) (core.Message, string, error) {
	switch item.Type {
	case "function_call":
		name := strings.TrimSpace(item.Name)
		if name == "" {
			return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: function_call name is required", index), nil)
		}
		callID := ResponsesFunctionCallCallID(item.CallID)
		return core.Message{
			Role:        "assistant",
			Content:     "",
			ContentNull: true,
			ToolCalls: []core.ToolCall{
				{
					ID:          callID,
					Type:        "function",
					ExtraFields: core.CloneUnknownJSONFields(item.ExtraFields),
					Function: core.FunctionCall{
						Name:      name,
						Arguments: item.Arguments,
					},
				},
			},
		}, "function_call", nil
	case "function_call_output":
		callID := strings.TrimSpace(item.CallID)
		if callID == "" {
			return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: function_call_output call_id is required", index), nil)
		}
		content, err := stringifyResponsesInputValueWithError(item.Output)
		if err != nil {
			return core.Message{}, "", core.NewInvalidRequestError(
				fmt.Sprintf("invalid responses input item at index %d: function_call_output.output must be JSON-serializable", index),
				err,
			)
		}
		return core.Message{
			Role:        "tool",
			ToolCallID:  callID,
			Content:     content,
			ExtraFields: core.CloneUnknownJSONFields(item.ExtraFields),
		}, "function_call_output", nil
	default: // message (type="" or "message")
		role := strings.TrimSpace(item.Role)
		if role == "" {
			return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: role is required", index), nil)
		}
		content, ok := ConvertResponsesContentToChatContent(item.Content)
		if !ok {
			return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: unsupported content", index), nil)
		}
		return core.Message{
			Role:        role,
			Content:     content,
			ExtraFields: core.CloneUnknownJSONFields(item.ExtraFields),
		}, "message", nil
	}
}

func convertResponsesInputMap(item map[string]interface{}, index int) (core.Message, string, error) {
	itemType, _ := item["type"].(string)
	switch itemType {
	case "function_call":
		name, _ := item["name"].(string)
		callID := firstNonEmptyString(item, "call_id", "id")
		if strings.TrimSpace(name) == "" {
			return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: function_call name is required", index), nil)
		}
		callID = ResponsesFunctionCallCallID(callID)
		return core.Message{
			Role:        "assistant",
			Content:     "",
			ContentNull: true,
			ToolCalls: []core.ToolCall{
				{
					ID:          callID,
					Type:        "function",
					ExtraFields: core.UnknownJSONFieldsFromMap(rawJSONMapFromUnknownKeys(item, "type", "call_id", "id", "name", "arguments", "status")),
					Function: core.FunctionCall{
						Name:      name,
						Arguments: stringifyResponsesInputValue(item["arguments"]),
					},
				},
			},
		}, "function_call", nil
	case "function_call_output":
		callID := firstNonEmptyString(item, "call_id")
		if callID == "" {
			return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: function_call_output call_id is required", index), nil)
		}
		content, err := stringifyResponsesInputValueWithError(item["output"])
		if err != nil {
			return core.Message{}, "", core.NewInvalidRequestError(
				fmt.Sprintf("invalid responses input item at index %d: function_call_output.output must be JSON-serializable", index),
				err,
			)
		}
		return core.Message{
			Role:        "tool",
			ToolCallID:  callID,
			Content:     content,
			ExtraFields: core.UnknownJSONFieldsFromMap(rawJSONMapFromUnknownKeys(item, "type", "call_id", "status", "output")),
		}, "function_call_output", nil
	}

	role, _ := item["role"].(string)
	role = strings.TrimSpace(role)
	if role == "" {
		return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: role is required", index), nil)
	}

	content, ok := ConvertResponsesContentToChatContent(item["content"])
	if !ok {
		return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: unsupported content", index), nil)
	}
	return core.Message{
		Role:        role,
		Content:     content,
		ExtraFields: core.UnknownJSONFieldsFromMap(rawJSONMapFromUnknownKeys(item, "type", "role", "status", "content")),
	}, "message", nil
}

func cloneResponsesMessage(msg core.Message) core.Message {
	cloned := msg
	if len(msg.ToolCalls) > 0 {
		cloned.ToolCalls = make([]core.ToolCall, len(msg.ToolCalls))
		for i, call := range msg.ToolCalls {
			cloned.ToolCalls[i] = cloneResponsesToolCall(call)
		}
	}
	if parts, ok := msg.Content.([]core.ContentPart); ok {
		clonedParts := make([]core.ContentPart, len(parts))
		for i, part := range parts {
			clonedParts[i] = cloneResponsesContentPart(part)
		}
		cloned.Content = clonedParts
	}
	cloned.ExtraFields = core.CloneUnknownJSONFields(msg.ExtraFields)
	return cloned
}

func canMergeAssistantMessages(current, next core.Message) bool {
	if !current.ExtraFields.IsEmpty() || !next.ExtraFields.IsEmpty() {
		return false
	}
	if !core.HasStructuredContent(current.Content) && !core.HasStructuredContent(next.Content) {
		return true
	}
	return isAssistantToolCallOnlyMessage(next)
}

func mergeAssistantMessage(dst *core.Message, src core.Message) {
	if text := core.ExtractTextContent(src.Content); text != "" {
		existing := core.ExtractTextContent(dst.Content)
		dst.Content = existing + text
		dst.ContentNull = false
	}
	if len(src.ToolCalls) > 0 {
		dst.ToolCalls = append(dst.ToolCalls, src.ToolCalls...)
		if core.ExtractTextContent(dst.Content) == "" {
			dst.ContentNull = dst.ContentNull || src.ContentNull
		}
	}
}

func isAssistantToolCallOnlyMessage(msg core.Message) bool {
	if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
		return false
	}
	if core.HasStructuredContent(msg.Content) {
		return false
	}
	return core.ExtractTextContent(msg.Content) == ""
}

// ConvertResponsesContentToChatContent maps Responses input content to Chat content.
// Text-only arrays are flattened to strings for broader provider compatibility.
// Any non-text part preserves the array form so multimodal payloads survive routing.
func ConvertResponsesContentToChatContent(content interface{}) (any, bool) {
	switch c := content.(type) {
	case string:
		return c, true
	case []map[string]any:
		items := make([]interface{}, 0, len(c))
		for _, item := range c {
			items = append(items, item)
		}
		return convertResponsesContentParts(items)
	case []interface{}:
		return convertResponsesContentParts(c)
	case []core.ContentPart:
		parts := make([]core.ContentPart, 0, len(c))
		for _, part := range c {
			normalized, ok := normalizeTypedResponsesContentPart(part)
			if !ok {
				return nil, false
			}
			parts = append(parts, normalized)
		}
		return finalizeResponsesChatContent(parts)
	case core.ContentPart:
		normalized, ok := normalizeTypedResponsesContentPart(c)
		if !ok {
			return nil, false
		}
		return finalizeResponsesChatContent([]core.ContentPart{normalized})
	default:
		return nil, false
	}
}

func convertResponsesContentParts(parts []interface{}) (any, bool) {
	typedParts := make([]core.ContentPart, 0, len(parts))

	for _, part := range parts {
		partMap, ok := part.(map[string]interface{})
		if !ok {
			return nil, false
		}

		partType, _ := partMap["type"].(string)
		switch partType {
		case "text", "input_text", "output_text":
			text, ok := partMap["text"].(string)
			if !ok || text == "" {
				return nil, false
			}
			typedParts = append(typedParts, core.ContentPart{
				Type:        "text",
				Text:        text,
				ExtraFields: core.UnknownJSONFieldsFromMap(rawJSONMapFromUnknownKeys(partMap, "type", "text")),
			})
		case "image_url", "input_image":
			imageURL, ok := normalizeResponsesImageURLForChat(partMap["image_url"])
			if !ok {
				return nil, false
			}
			typedParts = append(typedParts, core.ContentPart{
				Type:        "image_url",
				ImageURL:    imageURL,
				ExtraFields: core.UnknownJSONFieldsFromMap(rawJSONMapFromUnknownKeys(partMap, "type", "image_url")),
			})
		case "input_audio":
			inputAudio, ok := normalizeResponsesInputAudioForChat(partMap["input_audio"])
			if !ok {
				return nil, false
			}
			typedParts = append(typedParts, core.ContentPart{
				Type:        "input_audio",
				InputAudio:  inputAudio,
				ExtraFields: core.UnknownJSONFieldsFromMap(rawJSONMapFromUnknownKeys(partMap, "type", "input_audio")),
			})
		default:
			if nested, ok := partMap["content"]; ok {
				text := ExtractContentFromInput(nested)
				if text == "" {
					return nil, false
				}
				typedParts = append(typedParts, core.ContentPart{Type: "text", Text: text})
				continue
			}
			return nil, false
		}
	}

	if len(typedParts) == 0 {
		return nil, false
	}
	return finalizeResponsesChatContent(typedParts)
}

func normalizeTypedResponsesContentPart(part core.ContentPart) (core.ContentPart, bool) {
	switch part.Type {
	case "text", "input_text", "output_text":
		if part.Text == "" {
			return core.ContentPart{}, false
		}
		return core.ContentPart{
			Type:        "text",
			Text:        part.Text,
			ExtraFields: core.CloneUnknownJSONFields(part.ExtraFields),
		}, true
	case "image_url", "input_image":
		if part.ImageURL == nil {
			return core.ContentPart{}, false
		}
		url := strings.TrimSpace(part.ImageURL.URL)
		if url == "" {
			return core.ContentPart{}, false
		}
		return core.ContentPart{
			Type: "image_url",
			ImageURL: &core.ImageURLContent{
				URL:         url,
				Detail:      strings.TrimSpace(part.ImageURL.Detail),
				MediaType:   strings.TrimSpace(part.ImageURL.MediaType),
				ExtraFields: core.CloneUnknownJSONFields(part.ImageURL.ExtraFields),
			},
			ExtraFields: core.CloneUnknownJSONFields(part.ExtraFields),
		}, true
	case "input_audio":
		if part.InputAudio == nil {
			return core.ContentPart{}, false
		}
		data := strings.TrimSpace(part.InputAudio.Data)
		format := strings.TrimSpace(part.InputAudio.Format)
		if data == "" || format == "" {
			return core.ContentPart{}, false
		}
		return core.ContentPart{
			Type: "input_audio",
			InputAudio: &core.InputAudioContent{
				Data:        data,
				Format:      format,
				ExtraFields: core.CloneUnknownJSONFields(part.InputAudio.ExtraFields),
			},
			ExtraFields: core.CloneUnknownJSONFields(part.ExtraFields),
		}, true
	default:
		return core.ContentPart{}, false
	}
}

func finalizeResponsesChatContent(parts []core.ContentPart) (any, bool) {
	if len(parts) == 0 {
		return nil, false
	}

	if !canFlattenResponsesPartsToText(parts) {
		return parts, true
	}

	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		texts = append(texts, part.Text)
	}
	return strings.Join(texts, " "), true
}

func canFlattenResponsesPartsToText(parts []core.ContentPart) bool {
	for _, part := range parts {
		if part.Type != "text" {
			return false
		}
		if !part.ExtraFields.IsEmpty() {
			return false
		}
	}
	return true
}

func normalizeResponsesImageURLForChat(value interface{}) (*core.ImageURLContent, bool) {
	switch v := value.(type) {
	case string:
		url := strings.TrimSpace(v)
		if url == "" {
			return nil, false
		}
		return &core.ImageURLContent{URL: url}, true
	case map[string]string:
		url := strings.TrimSpace(v["url"])
		if url == "" {
			return nil, false
		}
		return &core.ImageURLContent{
			URL:         url,
			Detail:      strings.TrimSpace(v["detail"]),
			MediaType:   strings.TrimSpace(v["media_type"]),
			ExtraFields: core.UnknownJSONFieldsFromMap(rawJSONMapFromUnknownStringKeys(v, "url", "detail", "media_type")),
		}, true
	case map[string]interface{}:
		url, _ := v["url"].(string)
		url = strings.TrimSpace(url)
		if url == "" {
			return nil, false
		}
		detail, _ := v["detail"].(string)
		mediaType, _ := v["media_type"].(string)
		return &core.ImageURLContent{
			URL:         url,
			Detail:      strings.TrimSpace(detail),
			MediaType:   strings.TrimSpace(mediaType),
			ExtraFields: core.UnknownJSONFieldsFromMap(rawJSONMapFromUnknownKeys(v, "url", "detail", "media_type")),
		}, true
	default:
		return nil, false
	}
}

func normalizeResponsesInputAudioForChat(value interface{}) (*core.InputAudioContent, bool) {
	switch v := value.(type) {
	case map[string]string:
		data := strings.TrimSpace(v["data"])
		format := strings.TrimSpace(v["format"])
		if data == "" || format == "" {
			return nil, false
		}
		return &core.InputAudioContent{
			Data:        data,
			Format:      format,
			ExtraFields: core.UnknownJSONFieldsFromMap(rawJSONMapFromUnknownStringKeys(v, "data", "format")),
		}, true
	case map[string]interface{}:
		data, _ := v["data"].(string)
		format, _ := v["format"].(string)
		data = strings.TrimSpace(data)
		format = strings.TrimSpace(format)
		if data == "" || format == "" {
			return nil, false
		}
		return &core.InputAudioContent{
			Data:        data,
			Format:      format,
			ExtraFields: core.UnknownJSONFieldsFromMap(rawJSONMapFromUnknownKeys(v, "data", "format")),
		}, true
	default:
		return nil, false
	}
}

func cloneResponsesToolCall(call core.ToolCall) core.ToolCall {
	cloned := call
	cloned.ExtraFields = core.CloneUnknownJSONFields(call.ExtraFields)
	cloned.Function.ExtraFields = core.CloneUnknownJSONFields(call.Function.ExtraFields)
	return cloned
}

func cloneResponsesContentPart(part core.ContentPart) core.ContentPart {
	cloned := part
	cloned.ExtraFields = core.CloneUnknownJSONFields(part.ExtraFields)
	if part.ImageURL != nil {
		image := *part.ImageURL
		image.ExtraFields = core.CloneUnknownJSONFields(part.ImageURL.ExtraFields)
		cloned.ImageURL = &image
	}
	if part.InputAudio != nil {
		audio := *part.InputAudio
		audio.ExtraFields = core.CloneUnknownJSONFields(part.InputAudio.ExtraFields)
		cloned.InputAudio = &audio
	}
	return cloned
}

func rawJSONMapFromUnknownKeys(src map[string]interface{}, knownKeys ...string) map[string]json.RawMessage {
	if len(src) == 0 {
		return nil
	}
	known := make(map[string]struct{}, len(knownKeys))
	for _, key := range knownKeys {
		known[key] = struct{}{}
	}

	var extras map[string]json.RawMessage
	for key, value := range src {
		if _, ok := known[key]; ok {
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			continue
		}
		if extras == nil {
			extras = make(map[string]json.RawMessage)
		}
		extras[key] = raw
	}
	return extras
}

func rawJSONMapFromUnknownStringKeys(src map[string]string, knownKeys ...string) map[string]json.RawMessage {
	if len(src) == 0 {
		return nil
	}

	converted := make(map[string]interface{}, len(src))
	for key, value := range src {
		converted[key] = value
	}
	return rawJSONMapFromUnknownKeys(converted, knownKeys...)
}

func firstNonEmptyString(item map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		value, _ := item[key].(string)
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stringifyResponsesInputValue(value interface{}) string {
	encoded, err := stringifyResponsesInputValueWithError(value)
	if err != nil {
		return ""
	}
	return encoded
}

func stringifyResponsesInputValueWithError(value interface{}) (string, error) {
	switch v := value.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}

// ExtractContentFromInput extracts text content from responses input.
func ExtractContentFromInput(content interface{}) string {
	switch c := content.(type) {
	case string:
		return c
	case []core.ContentPart:
		texts := make([]string, 0, len(c))
		for _, part := range c {
			if part.Text != "" {
				texts = append(texts, part.Text)
			}
		}
		return strings.Join(texts, " ")
	case []map[string]any:
		return extractTextFromMapSlice(c)
	case []interface{}:
		texts := make([]string, 0, len(c))
		for _, part := range c {
			if partMap, ok := part.(map[string]interface{}); ok {
				if text := extractTextFromInputMap(partMap); text != "" {
					texts = append(texts, text)
				}
			}
		}
		return strings.Join(texts, " ")
	default:
		return ""
	}
}

func extractTextFromMapSlice(parts []map[string]any) string {
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if text := extractTextFromInputMap(part); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, " ")
}

func extractTextFromInputMap(part map[string]any) string {
	texts := make([]string, 0, 2)
	if text, ok := part["text"].(string); ok && text != "" {
		texts = append(texts, text)
	}
	if nested, ok := part["content"]; ok {
		if text := ExtractContentFromInput(nested); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, " ")
}

// ResponsesFunctionCallCallID returns the call id if present or generates one.
func ResponsesFunctionCallCallID(callID string) string {
	if strings.TrimSpace(callID) != "" {
		return callID
	}
	return "call_" + uuid.New().String()
}

// ResponsesFunctionCallItemID returns a stable function-call item id.
func ResponsesFunctionCallItemID(callID string) string {
	normalizedCallID := strings.TrimSpace(callID)
	if normalizedCallID == "" {
		normalizedCallID = "call_" + uuid.New().String()
	}
	return "fc_" + normalizedCallID
}

func buildResponsesMessageContent(content any) []core.ResponsesContentItem {
	switch c := content.(type) {
	case string:
		return []core.ResponsesContentItem{
			{
				Type:        "output_text",
				Text:        c,
				Annotations: []json.RawMessage{},
			},
		}
	case []core.ContentPart:
		return buildResponsesContentItemsFromParts(c)
	case []interface{}:
		parts, ok := core.NormalizeContentParts(c)
		if !ok {
			return nil
		}
		return buildResponsesContentItemsFromParts(parts)
	default:
		text := core.ExtractTextContent(content)
		if text == "" {
			return nil
		}
		return []core.ResponsesContentItem{
			{
				Type:        "output_text",
				Text:        text,
				Annotations: []json.RawMessage{},
			},
		}
	}
}

func buildResponsesContentItemsFromParts(parts []core.ContentPart) []core.ResponsesContentItem {
	items := make([]core.ResponsesContentItem, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "text":
			items = append(items, core.ResponsesContentItem{
				Type:        "output_text",
				Text:        part.Text,
				Annotations: []json.RawMessage{},
			})
		case "image_url":
			if part.ImageURL == nil {
				continue
			}
			url := strings.TrimSpace(part.ImageURL.URL)
			if url == "" {
				continue
			}
			items = append(items, core.ResponsesContentItem{
				Type: "input_image",
				ImageURL: &core.ImageURLContent{
					URL:         url,
					Detail:      strings.TrimSpace(part.ImageURL.Detail),
					MediaType:   strings.TrimSpace(part.ImageURL.MediaType),
					ExtraFields: core.CloneUnknownJSONFields(part.ImageURL.ExtraFields),
				},
			})
		case "input_audio":
			if part.InputAudio == nil {
				continue
			}
			data := strings.TrimSpace(part.InputAudio.Data)
			format := strings.TrimSpace(part.InputAudio.Format)
			if data == "" || format == "" {
				continue
			}
			items = append(items, core.ResponsesContentItem{
				Type: "input_audio",
				InputAudio: &core.InputAudioContent{
					Data:        data,
					Format:      format,
					ExtraFields: core.CloneUnknownJSONFields(part.InputAudio.ExtraFields),
				},
			})
		}
	}
	return items
}

// BuildResponsesOutputItems converts a response message into Responses API output items.
func BuildResponsesOutputItems(msg core.ResponseMessage) []core.ResponsesOutputItem {
	output := make([]core.ResponsesOutputItem, 0, len(msg.ToolCalls)+1)
	contentItems := buildResponsesMessageContent(msg.Content)
	if len(contentItems) > 0 || len(msg.ToolCalls) == 0 {
		if len(contentItems) == 0 {
			contentItems = []core.ResponsesContentItem{
				{
					Type:        "output_text",
					Text:        "",
					Annotations: []json.RawMessage{},
				},
			}
		}
		output = append(output, core.ResponsesOutputItem{
			ID:      "msg_" + uuid.New().String(),
			Type:    "message",
			Role:    "assistant",
			Status:  "completed",
			Content: contentItems,
		})
	}
	for _, toolCall := range msg.ToolCalls {
		callID := ResponsesFunctionCallCallID(toolCall.ID)
		output = append(output, core.ResponsesOutputItem{
			ID:        ResponsesFunctionCallItemID(callID),
			Type:      "function_call",
			Status:    "completed",
			CallID:    callID,
			Name:      toolCall.Function.Name,
			Arguments: toolCall.Function.Arguments,
		})
	}
	return output
}

// ConvertChatResponseToResponses converts a ChatResponse to a ResponsesResponse.
func ConvertChatResponseToResponses(resp *core.ChatResponse) *core.ResponsesResponse {
	output := []core.ResponsesOutputItem{
		{
			ID:     "msg_" + uuid.New().String(),
			Type:   "message",
			Role:   "assistant",
			Status: "completed",
			Content: []core.ResponsesContentItem{
				{
					Type:        "output_text",
					Text:        "",
					Annotations: []json.RawMessage{},
				},
			},
		},
	}
	if len(resp.Choices) > 0 {
		output = BuildResponsesOutputItems(resp.Choices[0].Message)
	}

	return &core.ResponsesResponse{
		ID:        resp.ID,
		Object:    "response",
		CreatedAt: resp.Created,
		Model:     resp.Model,
		Provider:  resp.Provider,
		Status:    "completed",
		Output:    output,
		Usage: &core.ResponsesUsage{
			InputTokens:             resp.Usage.PromptTokens,
			OutputTokens:            resp.Usage.CompletionTokens,
			TotalTokens:             resp.Usage.TotalTokens,
			PromptTokensDetails:     resp.Usage.PromptTokensDetails,
			CompletionTokensDetails: resp.Usage.CompletionTokensDetails,
			RawUsage:                resp.Usage.RawUsage,
		},
	}
}

// ResponsesViaChat implements the Responses API by converting to/from Chat format.
func ResponsesViaChat(ctx context.Context, p ChatProvider, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	chatReq, err := ConvertResponsesRequestToChat(req)
	if err != nil {
		return nil, err
	}

	chatResp, err := p.ChatCompletion(ctx, chatReq)
	if err != nil {
		return nil, err
	}

	return ConvertChatResponseToResponses(chatResp), nil
}

// StreamResponsesViaChat implements streaming Responses API by converting to/from Chat format.
func StreamResponsesViaChat(ctx context.Context, p ChatProvider, req *core.ResponsesRequest, providerName string) (io.ReadCloser, error) {
	chatReq, err := ConvertResponsesRequestToChat(req)
	if err != nil {
		return nil, err
	}
	if core.GetEnforceReturningUsageData(ctx) {
		if chatReq.StreamOptions == nil {
			chatReq.StreamOptions = &core.StreamOptions{}
		}
		chatReq.StreamOptions.IncludeUsage = true
	}

	stream, err := p.StreamChatCompletion(ctx, chatReq)
	if err != nil {
		return nil, err
	}

	return NewOpenAIResponsesStreamConverter(stream, req.Model, providerName), nil
}
