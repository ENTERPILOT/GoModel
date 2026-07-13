package guardrails

import (
	"maps"
	"reflect"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// responsesToMessages extracts the normalized message list from a ResponsesRequest.
// The Instructions field maps to a system message and the input items are kept in
// one-to-one order so content-rewriting guardrails can be applied back safely.
func responsesToMessages(req *core.ResponsesRequest) ([]Message, error) {
	var msgs []Message
	if req.Instructions != "" {
		msgs = append(msgs, Message{Role: "system", Content: req.Instructions})
	}

	inputMsgs, err := responsesInputToMessages(req.Input)
	if err != nil {
		return nil, err
	}
	msgs = append(msgs, inputMsgs...)
	return msgs, nil
}

func responsesInputToMessages(input any) ([]Message, error) {
	switch typed := input.(type) {
	case nil:
		return nil, nil
	case string:
		return []Message{{Role: "user", Content: typed}}, nil
	}

	elements, err := coerceResponsesInputElements(input)
	if err != nil {
		return nil, err
	}
	msgs := make([]Message, len(elements))
	for i, element := range elements {
		msg, err := responsesInputElementToGuardrailMessage(element, i)
		if err != nil {
			return nil, err
		}
		msgs[i] = msg
	}
	return msgs, nil
}

func coerceResponsesInputElements(input any) ([]core.ResponsesInputElement, error) {
	switch typed := input.(type) {
	case []core.ResponsesInputElement:
		elements := make([]core.ResponsesInputElement, len(typed))
		copy(elements, typed)
		return elements, nil
	case []map[string]any:
		elements := make([]core.ResponsesInputElement, len(typed))
		for i, item := range typed {
			raw, err := json.Marshal(item)
			if err != nil {
				return nil, core.NewInvalidRequestError("invalid responses input item", err)
			}
			if err := json.Unmarshal(raw, &elements[i]); err != nil {
				return nil, core.NewInvalidRequestError("invalid responses input item", err)
			}
		}
		return elements, nil
	case []any:
		elements := make([]core.ResponsesInputElement, len(typed))
		for i, item := range typed {
			raw, err := json.Marshal(item)
			if err != nil {
				return nil, core.NewInvalidRequestError("invalid responses input item", err)
			}
			if err := json.Unmarshal(raw, &elements[i]); err != nil {
				return nil, core.NewInvalidRequestError("invalid responses input item", err)
			}
		}
		return elements, nil
	default:
		return nil, core.NewInvalidRequestError("invalid responses input: unsupported type", nil)
	}
}

func responsesInputElementToGuardrailMessage(item core.ResponsesInputElement, index int) (Message, error) {
	switch item.Type {
	case "function_call":
		if strings.TrimSpace(item.Name) == "" {
			return Message{}, core.NewInvalidRequestError("invalid responses input item: function_call name is required", nil)
		}
		return Message{
			Role:        "assistant",
			Content:     "",
			ContentNull: true,
			ToolCalls: []core.ToolCall{
				{
					ID:   item.CallID,
					Type: "function",
					Function: core.FunctionCall{
						Name:      item.Name,
						Arguments: item.Arguments,
					},
				},
			},
		}, nil
	case "function_call_output":
		content, err := stringifyResponsesValue(item.Output)
		if err != nil {
			return Message{}, core.NewInvalidRequestError("invalid responses input item: function_call_output.output must be JSON-serializable", err)
		}
		return Message{
			Role:       "tool",
			ToolCallID: item.CallID,
			Content:    content,
		}, nil
	default:
		role := strings.TrimSpace(item.Role)
		if role == "" {
			return Message{}, core.NewInvalidRequestError("invalid responses input item: role is required", nil)
		}
		text, err := normalizeGuardrailMessageText(item.Content)
		if err != nil {
			return Message{}, core.NewInvalidRequestError("invalid responses input item: unsupported content", err)
		}
		return Message{
			Role:        role,
			Content:     text,
			ContentNull: item.Content == nil,
		}, nil
	}
}

func stringifyResponsesValue(value any) (string, error) {
	switch typed := value.(type) {
	case nil:
		return "", nil
	case string:
		return typed, nil
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
}

// applyMessagesToResponses returns a shallow copy of req with system and input
// messages applied back to the original Responses envelope.
func applyMessagesToResponses(req *core.ResponsesRequest, msgs []Message) (*core.ResponsesRequest, error) {
	result := *req
	originalInputMsgs, err := responsesInputToMessages(req.Input)
	if err != nil {
		return nil, err
	}

	inputMsgs := msgs
	switch {
	case len(msgs) == len(originalInputMsgs):
		result.Instructions = ""
	case len(msgs) == len(originalInputMsgs)+1 && len(msgs) > 0 && msgs[0].Role == "system":
		result.Instructions = msgs[0].Content
		inputMsgs = msgs[1:]
	default:
		return nil, core.NewInvalidRequestError("guardrails cannot add or remove responses input items", nil)
	}

	input, err := applyMessagesToResponsesInput(req.Input, inputMsgs)
	if err != nil {
		return nil, err
	}
	result.Input = input
	return &result, nil
}

func applyMessagesToResponsesInput(original any, msgs []Message) (any, error) {
	switch original.(type) {
	case nil:
		if len(msgs) != 0 {
			return nil, core.NewInvalidRequestError("guardrails cannot add or remove responses input items", nil)
		}
		return nil, nil
	case string:
		if len(msgs) != 1 {
			return nil, core.NewInvalidRequestError("guardrails cannot add or remove responses input items", nil)
		}
		if msgs[0].Role != "user" {
			return nil, core.NewInvalidRequestError("guardrails cannot change the role of a string responses input", nil)
		}
		if msgs[0].ContentNull {
			return "", nil
		}
		return msgs[0].Content, nil
	}

	elements, err := coerceResponsesInputElements(original)
	if err != nil {
		return nil, err
	}
	patched, err := applyMessagesToResponsesElements(elements, msgs)
	if err != nil {
		return nil, err
	}
	return patchResponsesInputEnvelope(original, patched)
}

func applyMessagesToResponsesElements(elements []core.ResponsesInputElement, msgs []Message) ([]core.ResponsesInputElement, error) {
	if len(msgs) != len(elements) {
		return nil, core.NewInvalidRequestError("guardrails cannot add or remove responses input items", nil)
	}

	result := make([]core.ResponsesInputElement, len(elements))
	for i, original := range elements {
		patched, err := applyGuardedResponsesElementToOriginal(original, msgs[i], i)
		if err != nil {
			return nil, err
		}
		result[i] = patched
	}
	return result, nil
}

func applyGuardedResponsesElementToOriginal(original core.ResponsesInputElement, modified Message, _ int) (core.ResponsesInputElement, error) {
	preserved := original

	switch original.Type {
	case "function_call":
		if modified.Role != "assistant" {
			return core.ResponsesInputElement{}, core.NewInvalidRequestError("guardrails cannot reorder or retag responses input items", nil)
		}
		return preserved, nil
	case "function_call_output":
		if modified.Role != "tool" {
			return core.ResponsesInputElement{}, core.NewInvalidRequestError("guardrails cannot reorder or retag responses input items", nil)
		}
		preserved.Output = modified.Content
		return preserved, nil
	default:
		role := strings.TrimSpace(original.Role)
		if role == "" {
			return core.ResponsesInputElement{}, core.NewInvalidRequestError("invalid responses input item: role is required", nil)
		}
		if modified.Role != role {
			return core.ResponsesInputElement{}, core.NewInvalidRequestError("guardrails cannot reorder or retag responses input items", nil)
		}
		content, err := applyGuardedResponsesContentToOriginal(original.Content, modified.Content, modified.ContentNull)
		if err != nil {
			return core.ResponsesInputElement{}, core.NewInvalidRequestError("guardrails cannot merge rewritten text into responses input item", err)
		}
		preserved.Content = content
		return preserved, nil
	}
}

func applyGuardedResponsesContentToOriginal(originalContent any, rewrittenText string, contentNull bool) (any, error) {
	if isResponsesStructuredContent(originalContent) {
		return rewriteStructuredResponsesContentWithTextRewrite(originalContent, rewrittenText)
	}
	if rewrittenText != "" {
		contentNull = false
	}
	if contentNull {
		return nil, nil
	}
	return rewrittenText, nil
}

func patchResponsesInputEnvelope(original any, patched []core.ResponsesInputElement) (any, error) {
	switch typed := original.(type) {
	case []core.ResponsesInputElement:
		result := make([]core.ResponsesInputElement, len(patched))
		copy(result, patched)
		return result, nil
	case []map[string]any:
		return patchResponsesInputMapSlice(typed, patched)
	case []any:
		return patchResponsesInputInterfaceSlice(typed, patched)
	default:
		return patched, nil
	}
}

func patchResponsesInputMapSlice(original []map[string]any, patched []core.ResponsesInputElement) ([]map[string]any, error) {
	if len(original) != len(patched) {
		return nil, core.NewInvalidRequestError("guardrails cannot add or remove responses input items", nil)
	}
	result := make([]map[string]any, len(original))
	for i := range original {
		item, err := patchResponsesInputMap(original[i], patched[i])
		if err != nil {
			return nil, err
		}
		result[i] = item
	}
	return result, nil
}

func patchResponsesInputInterfaceSlice(original []any, patched []core.ResponsesInputElement) ([]any, error) {
	if len(original) != len(patched) {
		return nil, core.NewInvalidRequestError("guardrails cannot add or remove responses input items", nil)
	}
	result := make([]any, len(original))
	for i := range original {
		item, err := patchResponsesInputInterfaceElement(original[i], patched[i])
		if err != nil {
			return nil, err
		}
		result[i] = item
	}
	return result, nil
}

func patchResponsesInputInterfaceElement(original any, patched core.ResponsesInputElement) (any, error) {
	if originalMap, ok := original.(map[string]any); ok {
		return patchResponsesInputMap(originalMap, patched)
	}
	return responsesInputElementAsAny(patched)
}

func patchResponsesInputMap(original map[string]any, patched core.ResponsesInputElement) (map[string]any, error) {
	cloned := cloneStringAnyMap(original)
	updated, err := responsesInputElementAsMap(patched)
	if err != nil {
		return nil, err
	}

	for _, key := range []string{"type", "role", "status", "content", "call_id", "id", "name", "arguments", "output"} {
		delete(cloned, key)
	}
	maps.Copy(cloned, updated)
	if patched.Type == "function_call_output" {
		cloned["output"] = restoreResponsesInputOutputValue(original["output"], patched.Output)
	}
	return cloned, nil
}

func restoreResponsesInputOutputValue(original any, rewritten string) any {
	if _, ok := original.(string); ok {
		return rewritten
	}
	if strings.TrimSpace(rewritten) == "" {
		if original == nil {
			return nil
		}
		return original
	}

	var decoded any
	if err := json.Unmarshal([]byte(rewritten), &decoded); err == nil {
		return decoded
	}
	if original == nil {
		return nil
	}
	return original
}

func responsesInputElementAsMap(element core.ResponsesInputElement) (map[string]any, error) {
	value, err := responsesInputElementAsAny(element)
	if err != nil {
		return nil, err
	}
	itemMap, ok := value.(map[string]any)
	if !ok {
		return nil, core.NewInvalidRequestError("invalid responses input item", nil)
	}
	return itemMap, nil
}

func responsesInputElementAsAny(element core.ResponsesInputElement) (any, error) {
	raw, err := json.Marshal(element)
	if err != nil {
		return nil, core.NewInvalidRequestError("invalid responses input item", err)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, core.NewInvalidRequestError("invalid responses input item", err)
	}
	return value, nil
}

func isResponsesStructuredContent(content any) bool {
	if content == nil {
		return false
	}
	switch typed := content.(type) {
	case []any:
		return true
	case []core.ContentPart:
		return true
	case []map[string]any:
		return true
	default:
		_ = typed
	}
	contentType := reflect.TypeOf(content)
	return contentType.Kind() == reflect.Slice || contentType.Kind() == reflect.Array
}

func rewriteStructuredResponsesContentWithTextRewrite(originalContent any, rewrittenText string) (any, error) {
	switch typed := originalContent.(type) {
	case []core.ContentPart:
		return rewriteStructuredResponsesTypedContentParts(typed, rewrittenText)
	case []any:
		return rewriteStructuredResponsesInterfaceContentParts(typed, rewrittenText)
	case []map[string]any:
		return rewriteStructuredResponsesMapContentParts(typed, rewrittenText)
	default:
		value := reflect.ValueOf(originalContent)
		if !value.IsValid() || (value.Kind() != reflect.Slice && value.Kind() != reflect.Array) {
			return nil, core.NewInvalidRequestError("unsupported structured responses content", nil)
		}
		parts := make([]any, value.Len())
		for i := 0; i < value.Len(); i++ {
			parts[i] = value.Index(i).Interface()
		}
		return rewriteStructuredResponsesInterfaceContentParts(parts, rewrittenText)
	}
}

func rewriteStructuredResponsesTypedContentParts(parts []core.ContentPart, rewrittenText string) (any, error) {
	textIndexes := make([]int, 0, len(parts))
	originalTexts := make([]string, 0, len(parts))
	for i, part := range parts {
		if !isResponsesTextPartType(part.Type) || part.Text == "" {
			continue
		}
		textIndexes = append(textIndexes, i)
		originalTexts = append(originalTexts, part.Text)
	}

	if len(textIndexes) == 0 {
		if rewrittenText == "" {
			if len(parts) == 0 {
				return []core.ContentPart{}, nil
			}
			return cloneContentParts(parts), nil
		}
		prepended := []core.ContentPart{{Type: "input_text", Text: rewrittenText}}
		prepended = append(prepended, cloneContentParts(parts)...)
		return prepended, nil
	}

	if len(textIndexes) == 1 {
		merged := cloneContentParts(parts)
		textIndex := textIndexes[0]
		if rewrittenText == "" {
			merged = append(merged[:textIndex], merged[textIndex+1:]...)
		} else {
			merged[textIndex].Text = rewrittenText
		}
		if len(merged) == 0 {
			return nil, core.NewInvalidRequestError("guardrails produced empty structured responses content after rewrite", nil)
		}
		return merged, nil
	}

	if rewrittenText == strings.Join(originalTexts, " ") {
		return cloneContentParts(parts), nil
	}

	merged := make([]core.ContentPart, 0, len(parts))
	insertedRewrittenText := false
	for _, part := range parts {
		if isResponsesTextPartType(part.Type) {
			if !insertedRewrittenText && rewrittenText != "" {
				rewrittenPart := cloneContentPart(part)
				rewrittenPart.Text = rewrittenText
				merged = append(merged, rewrittenPart)
				insertedRewrittenText = true
			}
			continue
		}
		merged = append(merged, cloneContentPart(part))
	}

	if len(merged) == 0 {
		return nil, core.NewInvalidRequestError("guardrails produced empty structured responses content after rewrite", nil)
	}
	return merged, nil
}

func rewriteStructuredResponsesInterfaceContentParts(parts []any, rewrittenText string) (any, error) {
	textIndexes := make([]int, 0, len(parts))
	originalTexts := make([]string, 0, len(parts))
	for i, part := range parts {
		partMap, ok := part.(map[string]any)
		if !ok {
			continue
		}
		partType, _ := partMap["type"].(string)
		if !isResponsesTextPartType(partType) {
			continue
		}
		text, _ := partMap["text"].(string)
		if text == "" {
			continue
		}
		textIndexes = append(textIndexes, i)
		originalTexts = append(originalTexts, text)
	}

	if len(textIndexes) == 0 {
		if rewrittenText == "" {
			if len(parts) == 0 {
				return []any{}, nil
			}
			return cloneResponsesInterfaceParts(parts), nil
		}
		prepended := []any{map[string]any{"type": "input_text", "text": rewrittenText}}
		prepended = append(prepended, cloneResponsesInterfaceParts(parts)...)
		return prepended, nil
	}

	if len(textIndexes) == 1 {
		merged := make([]any, 0, len(parts))
		textIndex := textIndexes[0]
		for i, part := range parts {
			if i == textIndex {
				if rewrittenText == "" {
					continue
				}
				partMap, ok := part.(map[string]any)
				if !ok {
					return nil, core.NewInvalidRequestError("guardrails cannot rewrite non-object responses content part", nil)
				}
				cloned := cloneStringAnyMap(partMap)
				cloned["text"] = rewrittenText
				merged = append(merged, cloned)
				continue
			}
			merged = append(merged, cloneResponsesInterfacePart(part))
		}
		if len(merged) == 0 {
			return nil, core.NewInvalidRequestError("guardrails produced empty structured responses content after rewrite", nil)
		}
		return merged, nil
	}

	if rewrittenText == strings.Join(originalTexts, " ") {
		return cloneResponsesInterfaceParts(parts), nil
	}

	merged := make([]any, 0, len(parts))
	insertedRewrittenText := false
	for _, part := range parts {
		partMap, ok := part.(map[string]any)
		if ok {
			partType, _ := partMap["type"].(string)
			if isResponsesTextPartType(partType) {
				if !insertedRewrittenText && rewrittenText != "" {
					cloned := cloneStringAnyMap(partMap)
					cloned["text"] = rewrittenText
					merged = append(merged, cloned)
					insertedRewrittenText = true
				}
				continue
			}
		}
		merged = append(merged, cloneResponsesInterfacePart(part))
	}

	if len(merged) == 0 {
		return nil, core.NewInvalidRequestError("guardrails produced empty structured responses content after rewrite", nil)
	}
	return merged, nil
}

func rewriteStructuredResponsesMapContentParts(parts []map[string]any, rewrittenText string) (any, error) {
	if len(parts) == 0 && rewrittenText == "" {
		return []map[string]any{}, nil
	}

	interfaceParts := make([]any, len(parts))
	for i, part := range parts {
		interfaceParts[i] = part
	}
	rewritten, err := rewriteStructuredResponsesInterfaceContentParts(interfaceParts, rewrittenText)
	if err != nil {
		return nil, err
	}
	rewrittenParts, ok := rewritten.([]any)
	if !ok {
		return nil, core.NewInvalidRequestError("unsupported structured responses content", nil)
	}

	result := make([]map[string]any, len(rewrittenParts))
	for i, part := range rewrittenParts {
		partMap, ok := part.(map[string]any)
		if !ok {
			return nil, core.NewInvalidRequestError("guardrails cannot rewrite non-object responses content part", nil)
		}
		result[i] = cloneStringAnyMap(partMap)
	}
	return result, nil
}

func isResponsesTextPartType(partType string) bool {
	switch partType {
	case "text", "input_text", "output_text":
		return true
	default:
		return false
	}
}

func cloneResponsesInterfaceParts(parts []any) []any {
	if len(parts) == 0 {
		return nil
	}
	cloned := make([]any, len(parts))
	for i, part := range parts {
		cloned[i] = cloneResponsesInterfacePart(part)
	}
	return cloned
}

func cloneResponsesInterfacePart(part any) any {
	partMap, ok := part.(map[string]any)
	if !ok {
		return part
	}
	return cloneStringAnyMap(partMap)
}

// cloneStringAnyMap performs a shallow copy of the map. Nested maps/slices are
// intentionally shared; callers are expected to either preserve them as-is or
// replace whole top-level values instead of mutating nested structures in place.
func cloneStringAnyMap(src map[string]any) map[string]any {
	return maps.Clone(src)
}
