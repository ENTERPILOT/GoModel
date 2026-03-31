package anthropic

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"

	"gomodel/internal/core"
)

// openAICompatBreakingAnthropicThinkingSignaturesEnv enables preserving Anthropic
// reasoning fingerprints/signatures in OpenAI-compatible chat/responses payloads.
// This is intentionally behind a flag because it adds Anthropic-specific fields
// that can break strict OpenAI-compatible clients.
const openAICompatBreakingAnthropicThinkingSignaturesEnv = "OPENAI_COMPAT_BREAKING_ANTHROPIC_THINKING_SIGNATURES"

type openAIReasoningDetail struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	Signature string `json:"signature,omitempty"`
}

func anthropicThinkingSignaturesCompatEnabled() bool {
	value, ok := os.LookupEnv(openAICompatBreakingAnthropicThinkingSignaturesEnv)
	if !ok {
		return false
	}

	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func hasAnthropicReasoningCompatFields(fields core.UnknownJSONFields) bool {
	for _, key := range []string{"reasoning_details", "reasoning_content", "reasoning_signature"} {
		if len(fields.Lookup(key)) > 0 {
			return true
		}
	}
	return false
}

func hasAnthropicBreakingReasoningCompatFields(fields core.UnknownJSONFields) bool {
	for _, key := range []string{"reasoning_details", "reasoning_signature"} {
		if len(fields.Lookup(key)) > 0 {
			return true
		}
	}
	return false
}

func extractAnthropicReasoningDetails(blocks []anthropicContent) []openAIReasoningDetail {
	details := make([]openAIReasoningDetail, 0, len(blocks))
	for _, block := range blocks {
		if block.Type != "thinking" || block.Thinking == "" {
			continue
		}
		details = append(details, openAIReasoningDetail{
			Type:      "reasoning_text",
			Text:      block.Thinking,
			Signature: block.Signature,
		})
	}
	if len(details) == 0 {
		return nil
	}
	return details
}

func buildAnthropicReasoningExtraFields(details []openAIReasoningDetail) core.UnknownJSONFields {
	if len(details) == 0 {
		return core.UnknownJSONFields{}
	}

	fields := map[string]json.RawMessage{}

	reasoningContent, err := json.Marshal(joinAnthropicReasoningText(details))
	if err == nil {
		fields["reasoning_content"] = reasoningContent
	}

	if anthropicThinkingSignaturesCompatEnabled() {
		reasoningDetails, marshalErr := json.Marshal(details)
		if marshalErr == nil {
			fields["reasoning_details"] = reasoningDetails
		}
		if len(details) == 1 && strings.TrimSpace(details[0].Signature) != "" {
			reasoningSignature, marshalErr := json.Marshal(details[0].Signature)
			if marshalErr == nil {
				fields["reasoning_signature"] = reasoningSignature
			}
		}
	}

	if len(fields) == 0 {
		return core.UnknownJSONFields{}
	}
	return core.UnknownJSONFieldsFromMap(fields)
}

func joinAnthropicReasoningText(details []openAIReasoningDetail) string {
	var builder strings.Builder
	for _, detail := range details {
		if detail.Text == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString(detail.Text)
	}
	return builder.String()
}

func buildAnthropicResponsesReasoningItem(details []openAIReasoningDetail) *core.ResponsesOutputItem {
	if !anthropicThinkingSignaturesCompatEnabled() || len(details) == 0 {
		return nil
	}

	content := make([]core.ResponsesContentItem, 0, len(details))
	for _, detail := range details {
		content = append(content, core.ResponsesContentItem{
			Type:      "reasoning_text",
			Text:      detail.Text,
			Signature: detail.Signature,
		})
	}

	return &core.ResponsesOutputItem{
		ID:      "rs_" + uuid.New().String(),
		Type:    "reasoning",
		Status:  "completed",
		Content: content,
	}
}

func extractAnthropicReasoningBlocksFromExtraFields(fields core.UnknownJSONFields) ([]anthropicContentBlock, error) {
	if !anthropicThinkingSignaturesCompatEnabled() || fields.IsEmpty() {
		return nil, nil
	}

	if raw := fields.Lookup("reasoning_details"); len(raw) > 0 {
		details, err := parseOpenAIReasoningDetails(raw)
		if err != nil {
			return nil, core.NewInvalidRequestError("message.reasoning_details must be an array of reasoning_text objects", err)
		}
		if len(details) == 0 {
			return nil, core.NewInvalidRequestError("message.reasoning_details must contain at least one non-empty reasoning_text object", nil)
		}
		return openAIReasoningDetailsToAnthropicBlocks(details), nil
	}

	reasoningContentRaw := fields.Lookup("reasoning_content")
	reasoningSignatureRaw := fields.Lookup("reasoning_signature")
	if len(reasoningContentRaw) == 0 && len(reasoningSignatureRaw) == 0 {
		return nil, nil
	}
	if len(reasoningContentRaw) == 0 {
		return nil, core.NewInvalidRequestError("message.reasoning_signature requires message.reasoning_content", nil)
	}
	if len(reasoningSignatureRaw) == 0 {
		return nil, core.NewInvalidRequestError("message.reasoning_content requires message.reasoning_signature", nil)
	}

	var reasoningContent string
	if err := json.Unmarshal(reasoningContentRaw, &reasoningContent); err != nil {
		return nil, core.NewInvalidRequestError("message.reasoning_content must be a string", err)
	}

	var reasoningSignature string
	if err := json.Unmarshal(reasoningSignatureRaw, &reasoningSignature); err != nil {
		return nil, core.NewInvalidRequestError("message.reasoning_signature must be a string", err)
	}

	if strings.TrimSpace(reasoningContent) == "" {
		return nil, core.NewInvalidRequestError("message.reasoning_content must be a non-empty string", nil)
	}
	if strings.TrimSpace(reasoningSignature) == "" {
		return nil, core.NewInvalidRequestError("message.reasoning_signature must be a non-empty string", nil)
	}

	return []anthropicContentBlock{{
		Type:      "thinking",
		Thinking:  reasoningContent,
		Signature: reasoningSignature,
	}}, nil
}

func parseOpenAIReasoningDetails(raw json.RawMessage) ([]openAIReasoningDetail, error) {
	var details []openAIReasoningDetail
	if err := json.Unmarshal(raw, &details); err != nil {
		return nil, err
	}

	normalized := make([]openAIReasoningDetail, 0, len(details))
	for _, detail := range details {
		if strings.TrimSpace(detail.Text) == "" {
			continue
		}

		detailType := strings.TrimSpace(detail.Type)
		if detailType == "" {
			detailType = "reasoning_text"
		}
		if detailType != "reasoning_text" && detailType != "thinking" {
			return nil, fmt.Errorf("unsupported reasoning_details type %q", detailType)
		}

		normalized = append(normalized, openAIReasoningDetail{
			Type:      "reasoning_text",
			Text:      detail.Text,
			Signature: strings.TrimSpace(detail.Signature),
		})
	}

	if len(normalized) == 0 {
		return nil, nil
	}
	return normalized, nil
}

func openAIReasoningDetailsToAnthropicBlocks(details []openAIReasoningDetail) []anthropicContentBlock {
	blocks := make([]anthropicContentBlock, 0, len(details))
	for _, detail := range details {
		if strings.TrimSpace(detail.Text) == "" {
			continue
		}
		blocks = append(blocks, anthropicContentBlock{
			Type:      "thinking",
			Thinking:  detail.Text,
			Signature: detail.Signature,
		})
	}
	if len(blocks) == 0 {
		return nil
	}
	return blocks
}
