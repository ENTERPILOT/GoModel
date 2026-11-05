package providers

import (
	"fmt"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// ConvertResponsesInputToMessages converts a Responses API input payload into Chat API messages.
func ConvertResponsesInputToMessages(input any) ([]core.Message, error) {
	switch in := input.(type) {
	case string:
		return []core.Message{{Role: "user", Content: in}}, nil
	case []map[string]any:
		items := make([]any, 0, len(in))
		for _, item := range in {
			items = append(items, item)
		}
		return convertResponsesInputItems(items)
	case []any:
		return convertResponsesInputItems(in)
	case []core.ResponsesInputElement:
		items := make([]any, 0, len(in))
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

func convertResponsesInputItems(items []any) ([]core.Message, error) {
	messages := make([]core.Message, 0, len(items))
	var pendingAssistant *core.Message
	var pendingReasoning string
	var pendingReasoningExtra json.RawMessage

	flushPendingAssistant := func() error {
		// A reasoning item describes the assistant turn that follows it. Once
		// the history has moved past that turn the pending state is spent,
		// whether or not there was an assistant turn to attach it to; holding
		// it over would pin one turn's thinking blocks to another.
		reasoning, reasoningExtra := pendingReasoning, pendingReasoningExtra
		pendingReasoning, pendingReasoningExtra = "", nil
		if pendingAssistant == nil {
			return nil
		}
		// Chat reasoning extensions associate reasoning_content with the
		// assistant tool-call message. DeepSeek requires it on the following
		// tool-result request, while reasoning from ordinary assistant turns is
		// intentionally omitted because it is not part of their next-turn
		// context.
		if reasoning != "" && len(pendingAssistant.ToolCalls) > 0 {
			raw, err := json.Marshal(reasoning)
			if err != nil {
				return err
			}
			extra, err := core.MergeUnknownJSONFields(pendingAssistant.ExtraFields, map[string]json.RawMessage{
				"reasoning_content": raw,
			})
			if err != nil {
				return err
			}
			pendingAssistant.ExtraFields = extra
		}
		// Replay state travels with every reasoning item, tool calls or not:
		// an Anthropic thinking block has to be echoed back on a plain
		// assistant turn just as much as on a tool-use one.
		if len(reasoningExtra) > 0 {
			extra, err := core.MergeUnknownJSONFields(pendingAssistant.ExtraFields, map[string]json.RawMessage{
				core.ExtraContentField: reasoningExtra,
			})
			if err != nil {
				return err
			}
			pendingAssistant.ExtraFields = extra
		}
		messages = append(messages, *pendingAssistant)
		pendingAssistant = nil
		return nil
	}

	for i, item := range items {
		if reasoning, extra, ok := responsesInputReasoning(item); ok {
			if err := flushPendingAssistant(); err != nil {
				return nil, err
			}
			pendingReasoning = reasoning
			pendingReasoningExtra = extra
			continue
		}

		if image, ok, err := responsesInputGeneratedImage(item); ok {
			if err != nil {
				return nil, core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: image_generation_call", i), err)
			}
			if image.ImageURL == nil {
				continue
			}
			if pendingAssistant == nil {
				pendingAssistant = &core.Message{Role: "assistant", Content: ""}
			}
			if err := appendChatImage(pendingAssistant, image); err != nil {
				return nil, core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: image_generation_call", i), err)
			}
			continue
		}

		msg, itemType, err := convertResponsesInputItem(item, i)
		if err != nil {
			return nil, err
		}

		if msg.Role == "assistant" {
			if itemType == "message" && pendingAssistant != nil {
				if err := flushPendingAssistant(); err != nil {
					return nil, err
				}
			}
			if pendingAssistant == nil {
				assistant := cloneResponsesMessage(msg)
				pendingAssistant = &assistant
			} else if canMergeAssistantMessages(*pendingAssistant, msg) {
				mergeAssistantMessage(pendingAssistant, msg)
			} else {
				if err := flushPendingAssistant(); err != nil {
					return nil, err
				}
				assistant := cloneResponsesMessage(msg)
				pendingAssistant = &assistant
			}
			continue
		}

		if err := flushPendingAssistant(); err != nil {
			return nil, err
		}
		pendingReasoning = ""
		messages = append(messages, msg)
	}

	if err := flushPendingAssistant(); err != nil {
		return nil, err
	}
	return messages, nil
}

// responsesInputReasoning recognizes a Responses reasoning item and extracts
// its raw reasoning content along with any provider replay state the client
// echoed back. Summary text is accepted as a compatibility fallback for older
// gateways that mislabeled reasoning_content as a summary. Encrypted-only
// reasoning stays opaque and is deliberately omitted.
func responsesInputReasoning(item any) (string, json.RawMessage, bool) {
	var raw json.RawMessage
	switch typed := item.(type) {
	case core.ResponsesInputElement:
		if typed.Type != "reasoning" {
			return "", nil, false
		}
		raw = typed.Raw
		if len(raw) == 0 {
			encoded, err := json.Marshal(typed)
			if err != nil {
				return "", nil, true
			}
			raw = encoded
		}
	case map[string]any:
		itemType, _ := typed["type"].(string)
		if itemType != "reasoning" {
			return "", nil, false
		}
		encoded, err := json.Marshal(typed)
		if err != nil {
			return "", nil, true
		}
		raw = encoded
	default:
		return "", nil, false
	}

	var payload struct {
		Content      []responsesReasoningPart `json:"content"`
		Summary      []responsesReasoningPart `json:"summary"`
		ExtraContent json.RawMessage          `json:"extra_content"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", nil, true
	}
	extra := payload.ExtraContent
	if core.IsJSONNull(extra) {
		extra = nil
	}
	if text := reasoningTextParts(payload.Content, "reasoning_text"); text != "" {
		return text, extra, true
	}
	return reasoningTextParts(payload.Summary, "summary_text"), extra, true
}

// responsesInputGeneratedImage recognizes a replayed image_generation_call item
// and returns it as a chat image_url part with a base64 data URL, keeping its
// extra_content so a provider can match the image to its replay state.
func responsesInputGeneratedImage(item any) (core.ContentPart, bool, error) {
	var raw json.RawMessage
	switch typed := item.(type) {
	case core.ResponsesInputElement:
		if typed.Type != "image_generation_call" {
			return core.ContentPart{}, false, nil
		}
		raw = typed.Raw
		if len(raw) == 0 {
			// Without Raw there is no result to replay; skip the item.
			return core.ContentPart{}, true, nil
		}
	case map[string]any:
		if itemType, _ := typed["type"].(string); itemType != "image_generation_call" {
			return core.ContentPart{}, false, nil
		}
		encoded, err := json.Marshal(typed)
		if err != nil {
			return core.ContentPart{}, true, err
		}
		raw = encoded
	default:
		return core.ContentPart{}, false, nil
	}
	var payload struct {
		Result       json.RawMessage `json:"result"`
		OutputFormat json.RawMessage `json:"output_format"`
		ExtraContent json.RawMessage `json:"extra_content"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return core.ContentPart{}, true, err
	}
	var result, format string
	if !core.IsJSONNull(payload.Result) {
		if err := json.Unmarshal(payload.Result, &result); err != nil {
			return core.ContentPart{}, true, fmt.Errorf("result must be a string: %w", err)
		}
	}
	if !core.IsJSONNull(payload.OutputFormat) {
		if err := json.Unmarshal(payload.OutputFormat, &format); err != nil {
			return core.ContentPart{}, true, fmt.Errorf("output_format must be a string: %w", err)
		}
	}
	if result == "" {
		// Without the image there is nothing to replay; skip the item.
		return core.ContentPart{}, true, nil
	}
	if format == "" {
		format = "png"
	}
	image := &core.ImageURLContent{URL: "data:image/" + format + ";base64," + result}
	if !core.IsJSONNull(payload.ExtraContent) {
		image.ExtraFields = core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{core.ExtraContentField: payload.ExtraContent})
	}
	return core.ContentPart{Type: "image_url", ImageURL: image}, true, nil
}

// appendChatImage adds a generated image to an assistant message's images.
func appendChatImage(msg *core.Message, image core.ContentPart) error {
	if image.ImageURL == nil {
		return nil
	}
	images, err := core.ChatImages(msg.ExtraFields)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(append(images, image))
	if err != nil {
		return err
	}
	msg.ExtraFields, err = core.MergeUnknownJSONFields(msg.ExtraFields, map[string]json.RawMessage{core.ChatImagesField: encoded})
	return err
}

type responsesReasoningPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func reasoningTextParts(parts []responsesReasoningPart, partType string) string {
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if part.Type == partType && part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n\n")
}

// normalizeChatTranslationRole maps "developer" onto "system". "developer" is
// OpenAI's newer alias for "system", understood only by OpenAI's own
// reasoning models; chat-translated providers (DeepSeek, Anthropic, Gemini,
// ...) speak the classic chat-completions roles, so it must be normalized
// the same way the other chat-only providers already do (see
// cohere/gemini/bedrock chat translation).
func normalizeChatTranslationRole(role string) string {
	if role == "developer" {
		return "system"
	}
	return role
}

func convertResponsesInputItem(item any, index int) (core.Message, string, error) {
	switch typed := item.(type) {
	case core.ResponsesInputElement:
		return convertResponsesInputElement(typed, index)
	case map[string]any:
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
					ExtraFields: chatExtraFieldsFromResponsesItem(item.ExtraFields),
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
			ExtraFields: chatExtraFieldsFromResponsesItem(item.ExtraFields),
		}, "function_call_output", nil
	case "", "message":
		role := strings.TrimSpace(item.Role)
		if role == "" {
			return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: role is required", index), nil)
		}
		role = normalizeChatTranslationRole(role)
		content, ok := ConvertResponsesContentToChatContent(item.Content)
		if !ok {
			return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: unsupported content", index), nil)
		}
		return core.Message{
			Role:        role,
			Content:     content,
			ExtraFields: chatExtraFieldsFromResponsesItem(item.ExtraFields),
		}, "message", nil
	case "reasoning":
		// Recognized by responsesInputReasoningText before item conversion.
		return core.Message{}, "reasoning", nil
	default:
		return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: unsupported input item type %q for chat-translated providers", index, item.Type), nil)
	}
}

func convertResponsesInputMap(item map[string]any, index int) (core.Message, string, error) {
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
			ExtraFields: core.UnknownJSONFieldsFromMap(rawJSONMapFromUnknownKeys(item, "type", "call_id", "id", "status", "output")),
		}, "function_call_output", nil
	case "", "message":
	case "reasoning":
		// Recognized by responsesInputReasoningText before item conversion.
		return core.Message{}, "reasoning", nil
	default:
		return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: unsupported input item type %q for chat-translated providers", index, itemType), nil)
	}

	role, _ := item["role"].(string)
	role = strings.TrimSpace(role)
	if role == "" {
		return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: role is required", index), nil)
	}
	role = normalizeChatTranslationRole(role)

	content, ok := ConvertResponsesContentToChatContent(item["content"])
	if !ok {
		return core.Message{}, "", core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: unsupported content", index), nil)
	}
	return core.Message{
		Role:        role,
		Content:     content,
		ExtraFields: core.UnknownJSONFieldsFromMap(rawJSONMapFromUnknownKeys(item, "type", "role", "id", "status", "content")),
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
	// A Responses message followed by function_call items represents one Chat
	// assistant message. Metadata on the message remains on the merged message;
	// function-call metadata is already carried by each ToolCall.
	if isAssistantToolCallOnlyMessage(next) && next.ExtraFields.IsEmpty() {
		return true
	}
	if !current.ExtraFields.IsEmpty() || !next.ExtraFields.IsEmpty() {
		return false
	}
	if !core.HasStructuredContent(current.Content) && !core.HasStructuredContent(next.Content) {
		return true
	}
	return false
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

func firstNonEmptyString(item map[string]any, keys ...string) string {
	for _, key := range keys {
		value, _ := item[key].(string)
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stringifyResponsesInputValue(value any) string {
	encoded, err := stringifyResponsesInputValueWithError(value)
	if err != nil {
		return ""
	}
	return encoded
}

func stringifyResponsesInputValueWithError(value any) (string, error) {
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
