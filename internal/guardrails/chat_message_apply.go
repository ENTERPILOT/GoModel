package guardrails

import (
	"reflect"
	"strings"

	"github.com/enterpilot/gomodel/internal/core"
)

// chatToMessages extracts the normalized message list from a ChatRequest.
func chatToMessages(req *core.ChatRequest) ([]Message, error) {
	msgs := make([]Message, len(req.Messages))
	for i, m := range req.Messages {
		text, err := normalizeGuardrailMessageText(m.Content)
		if err != nil {
			return nil, core.NewInvalidRequestError("invalid chat message content", err)
		}
		msgs[i] = Message{
			Role:        m.Role,
			Content:     text,
			ToolCalls:   cloneToolCalls(m.ToolCalls),
			ToolCallID:  m.ToolCallID,
			ContentNull: m.ContentNull || m.Content == nil,
		}
	}
	return msgs, nil
}

// applyMessagesToChatPreservingEnvelope applies guardrail message updates while
// preserving the original chat message envelopes and structured content shapes.
func applyMessagesToChatPreservingEnvelope(req *core.ChatRequest, msgs []Message) (*core.ChatRequest, error) {
	systemOriginal := make([]core.Message, 0, len(req.Messages))
	nonSystemOriginal := make([]core.Message, 0, len(req.Messages))
	for _, original := range req.Messages {
		if original.Role == "system" {
			systemOriginal = append(systemOriginal, original)
			continue
		}
		nonSystemOriginal = append(nonSystemOriginal, original)
	}

	coreMessages := make([]core.Message, 0, len(msgs))
	modifiedSystemCount := 0
	for _, modified := range msgs {
		if modified.Role == "system" {
			modifiedSystemCount++
		}
	}
	systemMatchStart, originalSystemStart := tailMatchedSystemOffsets(len(systemOriginal), modifiedSystemCount)
	nextSystem := 0
	nextNonSystem := 0
	for _, modified := range msgs {
		if modified.Role == "system" {
			if nextSystem >= systemMatchStart {
				preserved, err := applyGuardedMessageToOriginal(systemOriginal[originalSystemStart+(nextSystem-systemMatchStart)], modified)
				if err != nil {
					return nil, err
				}
				coreMessages = append(coreMessages, preserved)
			} else {
				coreMessages = append(coreMessages, newChatMessageFromGuardrail(modified))
			}
			nextSystem++
			continue
		}

		if nextNonSystem >= len(nonSystemOriginal) {
			return nil, core.NewInvalidRequestError("guardrails cannot insert non-system chat messages", nil)
		}
		original := nonSystemOriginal[nextNonSystem]
		if modified.Role != original.Role {
			return nil, core.NewInvalidRequestError("guardrails cannot reorder non-system chat messages", nil)
		}
		preserved, err := applyGuardedMessageToOriginal(original, modified)
		if err != nil {
			return nil, err
		}
		coreMessages = append(coreMessages, preserved)
		nextNonSystem++
	}

	if nextNonSystem != len(nonSystemOriginal) {
		return nil, core.NewInvalidRequestError("guardrails cannot add or remove non-system chat messages", nil)
	}

	result := *req
	result.Messages = coreMessages
	return &result, nil
}

func tailMatchedSystemOffsets(originalSystemCount, modifiedSystemCount int) (matchStart, originalStart int) {
	matched := min(modifiedSystemCount, originalSystemCount)
	return modifiedSystemCount - matched, originalSystemCount - matched
}

func applyGuardedMessageToOriginal(original core.Message, modified Message) (core.Message, error) {
	preserved := cloneChatMessageEnvelope(original)
	preserved.Role = modified.Role
	preserved.ToolCalls = cloneToolCalls(modified.ToolCalls)
	preserved.ToolCallID = modified.ToolCallID

	content, contentNull, err := applyGuardedContentToOriginal(original.Content, modified.Content, modified.ContentNull)
	if err != nil {
		return core.Message{}, err
	}
	preserved.Content = content
	preserved.ContentNull = contentNull
	return preserved, nil
}

func newChatMessageFromGuardrail(m Message) core.Message {
	contentNull := m.ContentNull
	if m.Content != "" {
		contentNull = false
	}

	content := any(m.Content)
	if contentNull {
		content = nil
	}

	return core.Message{
		Role:        m.Role,
		Content:     content,
		ToolCalls:   cloneToolCalls(m.ToolCalls),
		ToolCallID:  m.ToolCallID,
		ContentNull: contentNull,
	}
}

func applyGuardedContentToOriginal(originalContent any, rewrittenText string, contentNull bool) (any, bool, error) {
	if core.HasStructuredContent(originalContent) {
		mergedContent, err := rewriteStructuredContentWithTextRewrite(originalContent, rewrittenText)
		if err != nil {
			return nil, false, err
		}
		return mergedContent, false, nil
	}

	if rewrittenText != "" {
		contentNull = false
	}
	if contentNull {
		return nil, true, nil
	}
	return rewrittenText, false, nil
}

func rewriteStructuredContentWithTextRewrite(originalContent any, rewrittenText string) (any, error) {
	parts, ok := core.NormalizeContentParts(originalContent)
	if !ok {
		return nil, core.NewInvalidRequestError("guardrails cannot merge rewritten text into structured message", nil)
	}

	// Guard against pathological numbers of content parts that could cause size
	// computations for allocations to overflow on some platforms.
	const maxContentParts = 1_000_000
	if len(parts) >= maxContentParts {
		return nil, core.NewInvalidRequestError("guardrails cannot merge structured message with too many content parts", nil)
	}

	originalTexts := make([]string, 0, len(parts))
	textPartIndexes := make([]int, 0, len(parts))
	for i, part := range parts {
		if part.Type == "text" {
			textPartIndexes = append(textPartIndexes, i)
			originalTexts = append(originalTexts, part.Text)
		}
	}

	if len(textPartIndexes) == 0 {
		merged := cloneContentParts(parts)
		if rewrittenText != "" {
			merged = append([]core.ContentPart{{Type: "text", Text: rewrittenText}}, merged...)
		}
		if len(merged) == 0 {
			return nil, core.NewInvalidRequestError("guardrails produced empty structured message after rewrite", nil)
		}
		return merged, nil
	}

	if len(textPartIndexes) == 1 {
		merged := cloneContentParts(parts)
		textIndex := textPartIndexes[0]
		if rewrittenText == "" {
			merged = append(merged[:textIndex], merged[textIndex+1:]...)
		} else {
			merged[textIndex].Text = rewrittenText
		}
		if len(merged) == 0 {
			return nil, core.NewInvalidRequestError("guardrails produced empty structured message after rewrite", nil)
		}
		return merged, nil
	}

	if rewrittenText == strings.Join(originalTexts, " ") {
		return cloneContentParts(parts), nil
	}

	merged := make([]core.ContentPart, 0, len(parts))
	insertedRewrittenText := false
	for _, part := range parts {
		if part.Type == "text" {
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
		return nil, core.NewInvalidRequestError("guardrails produced empty structured message after rewrite", nil)
	}
	return merged, nil
}

func normalizeGuardrailMessageText(content any) (string, error) {
	normalizedContent := content
	switch typed := content.(type) {
	case []map[string]any:
		parts := make([]any, len(typed))
		for i, part := range typed {
			parts[i] = part
		}
		normalizedContent = parts
	default:
		value := reflect.ValueOf(content)
		if value.IsValid() && (value.Kind() == reflect.Slice || value.Kind() == reflect.Array) {
			switch content.(type) {
			case []any, []core.ContentPart:
			default:
				parts := make([]any, value.Len())
				for i := 0; i < value.Len(); i++ {
					parts[i] = value.Index(i).Interface()
				}
				normalizedContent = parts
			}
		}
	}

	normalized, err := core.NormalizeMessageContent(normalizedContent)
	if err != nil {
		return "", err
	}
	return core.ExtractTextContent(normalized), nil
}
