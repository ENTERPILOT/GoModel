package providers

import (
	"strings"

	"github.com/goccy/go-json"

	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/core"
)

// ConvertResponsesResponseToChat converts a Responses API response into a
// chat completion, the inverse of ConvertChatResponseToResponses. The
// client-facing ID is minted ("chatcmpl-<uuid>"); the upstream "resp_" ID is
// a Responses resource handle and must not leak onto the chat surface, where
// it would promise chaining the stateless chat API cannot honor.
//
// The whole output array collapses into one choice at index 0: message items
// contribute their output_text parts to content and refusal parts to the
// message's refusal member, function_call items become tool_calls, and
// reasoning items surface as reasoning_content (with their extra_content
// replay state preserved so the next translated request can echo it back).
//
// A response with status "failed" has no honest chat completion shape; the
// caller is expected to turn ResponsesError into an upstream error instead of
// converting, so the finish reason is left empty here.
func ConvertResponsesResponseToChat(resp *core.ResponsesResponse) *core.ChatResponse {
	message := core.ResponseMessage{Role: "assistant"}
	extra := map[string]json.RawMessage{}

	var texts []string
	var refusals []string
	var reasoning []string
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				switch part.Type {
				case "output_text":
					texts = append(texts, part.Text)
				case "refusal":
					refusals = append(refusals, part.Text)
				}
			}
		case "function_call":
			message.ToolCalls = append(message.ToolCalls, core.ToolCall{
				ID:   ResponsesFunctionCallCallID(item.CallID),
				Type: "function",
				Function: core.FunctionCall{
					Name:      item.Name,
					Arguments: item.Arguments,
				},
				// extra_content replay state rides along so the next
				// translated request can echo it back.
				ExtraFields: toolCallExtraContent(item.ExtraFields),
			})
		case "reasoning":
			if text := responsesOutputReasoningText(item); text != "" {
				reasoning = append(reasoning, text)
			}
			if replay := item.ExtraFields.Lookup(core.ExtraContentField); !core.IsJSONNull(replay) {
				extra[core.ExtraContentField] = replay
			}
		}
	}
	message.Content = strings.Join(texts, "")
	if refusal := strings.Join(refusals, ""); refusal != "" {
		extra["refusal"] = chatViaResponsesJSONString(refusal)
	}
	if text := strings.Join(reasoning, "\n\n"); text != "" {
		extra["reasoning_content"] = chatViaResponsesJSONString(text)
	}
	message.ExtraFields = core.UnknownJSONFieldsFromMap(extra)

	converted := &core.ChatResponse{
		ID:       "chatcmpl-" + uuid.New().String(),
		Object:   "chat.completion",
		Created:  resp.CreatedAt,
		Model:    resp.Model,
		Provider: resp.Provider,
		Choices: []core.Choice{{
			Index:        0,
			Message:      message,
			FinishReason: responsesChatFinishReason(resp),
		}},
	}
	if resp.Usage != nil {
		converted.Usage = core.Usage{
			PromptTokens:            resp.Usage.InputTokens,
			CompletionTokens:        resp.Usage.OutputTokens,
			TotalTokens:             resp.Usage.TotalTokens,
			PromptTokensDetails:     resp.Usage.PromptTokensDetails,
			CompletionTokensDetails: resp.Usage.CompletionTokensDetails,
			RawUsage:                resp.Usage.RawUsage,
		}
	}
	return converted
}

// responsesChatFinishReason maps the Responses status onto a chat finish
// reason, the inverse of ApplyResponsesFinishReason. Incomplete reasons
// without a chat equivalent ("max_messages", "steered", anything new) yield
// "" rather than an invented reason; "failed" and non-terminal statuses
// likewise yield "" and are the caller's to handle.
func responsesChatFinishReason(resp *core.ResponsesResponse) string {
	switch resp.Status {
	case "completed":
		for _, item := range resp.Output {
			if item.Type == "function_call" {
				return "tool_calls"
			}
		}
		return "stop"
	case "incomplete":
		if resp.IncompleteDetails == nil {
			return ""
		}
		switch resp.IncompleteDetails.Reason {
		case "max_output_tokens":
			return "length"
		case "content_filter":
			return "content_filter"
		default:
			return ""
		}
	default:
		return ""
	}
}

// responsesOutputReasoningText extracts readable reasoning from an output
// reasoning item. Summary parts are accepted as a fallback, mirroring the
// input-side responsesInputReasoning: some payloads carry only a summary.
// Encrypted-only reasoning stays opaque and is omitted.
func responsesOutputReasoningText(item core.ResponsesOutputItem) string {
	texts := make([]string, 0, len(item.Content))
	for _, part := range item.Content {
		if part.Type == "reasoning_text" && part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	if len(texts) > 0 {
		return strings.Join(texts, "\n\n")
	}
	raw := item.ExtraFields.Lookup("summary")
	if core.IsJSONNull(raw) {
		return ""
	}
	var summary []responsesReasoningPart
	if err := json.Unmarshal(raw, &summary); err != nil {
		return ""
	}
	return reasoningTextParts(summary, "summary_text")
}

func chatViaResponsesJSONString(s string) json.RawMessage {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	return raw
}
