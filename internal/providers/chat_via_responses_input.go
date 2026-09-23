package providers

import (
	"fmt"
	"strings"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
)

// ConvertMessagesToResponsesInput converts Chat Completions messages into a
// Responses API input array plus a top-level instructions string. It is the
// inverse of ConvertResponsesInputToMessages: system and developer messages
// become instructions (joined with a blank line), user and assistant messages
// become message items, assistant tool calls become function_call items, and
// tool results become function_call_output items.
//
// Message content parts are emitted with Responses spellings as generic
// blocks rather than core.ContentPart values: ContentPart marshals to the
// Chat Completions shape, rewriting "input_text" to "text", which the
// Responses API rejects (see chatgpt/request.go).
func ConvertMessagesToResponsesInput(messages []core.Message) (input any, instructions string, err error) {
	items := make([]core.ResponsesInputElement, 0, len(messages))
	instructionParts := make([]string, 0, 1)
	for i := range messages {
		msg := messages[i]
		switch msg.Role {
		case "system", "developer":
			if text := core.ExtractTextContent(msg.Content); strings.TrimSpace(text) != "" {
				instructionParts = append(instructionParts, text)
			}
		case "assistant":
			assistantItems, convErr := chatAssistantMessageToResponsesItems(msg)
			if convErr != nil {
				return nil, "", convErr
			}
			items = append(items, assistantItems...)
		case "tool":
			item, convErr := chatToolMessageToResponsesItem(msg)
			if convErr != nil {
				return nil, "", convErr
			}
			items = append(items, item)
		default:
			// "user" and any other role travel as a plain message item.
			item, convErr := chatMessageToResponsesItem(msg, "input_text")
			if convErr != nil {
				return nil, "", convErr
			}
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		return nil, strings.Join(instructionParts, "\n\n"), nil
	}
	return items, strings.Join(instructionParts, "\n\n"), nil
}

// chatMessageToResponsesItem converts a plain (non-tool) chat message into a
// Responses message input item. textType is "input_text" for user content and
// "output_text" for assistant content, matching the Responses vocabulary for
// each role.
func chatMessageToResponsesItem(msg core.Message, textType string) (core.ResponsesInputElement, error) {
	blocks, err := chatContentToResponsesBlocks(msg.Content, textType)
	if err != nil {
		return core.ResponsesInputElement{}, err
	}
	item := core.ResponsesInputElement{
		Type:        "message",
		Role:        msg.Role,
		ExtraFields: chatMessageExtraFieldsForResponses(msg.ExtraFields),
	}
	// An empty message carries an empty string, not an empty part: OpenAI
	// never emits an empty text block.
	if len(blocks) == 0 {
		item.Content = ""
	} else {
		item.Content = blocks
	}
	return item, nil
}

// chatAssistantMessageToResponsesItems converts an assistant message into a
// reasoning item (when replay state is present), a message item, and one
// function_call item per tool call, in that order.
func chatAssistantMessageToResponsesItems(msg core.Message) ([]core.ResponsesInputElement, error) {
	items := make([]core.ResponsesInputElement, 0, len(msg.ToolCalls)+2)
	if reasoning, ok := chatReasoningReplayItem(msg); ok {
		items = append(items, reasoning)
	}
	// A tool-call-only turn must not gain a blank message item, mirroring
	// buildResponsesMessageContent.
	if core.ExtractTextContent(msg.Content) != "" || core.HasStructuredContent(msg.Content) || len(msg.ToolCalls) == 0 {
		item, err := chatMessageToResponsesItem(msg, "output_text")
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	for _, call := range msg.ToolCalls {
		callID := ResponsesFunctionCallCallID(call.ID)
		items = append(items, core.ResponsesInputElement{
			Type:        "function_call",
			CallID:      callID,
			Name:        call.Function.Name,
			Arguments:   call.Function.Arguments,
			ExtraFields: toolCallExtraContent(call.ExtraFields),
		})
	}
	return items, nil
}

// chatToolMessageToResponsesItem converts a tool-role message into a
// function_call_output item. Non-string content is stringified via JSON,
// mirroring stringifyResponsesInputValueWithError on the inbound path.
func chatToolMessageToResponsesItem(msg core.Message) (core.ResponsesInputElement, error) {
	output, err := stringifyResponsesInputValueWithError(msg.Content)
	if err != nil {
		return core.ResponsesInputElement{}, core.NewInvalidRequestError(
			"chat tool message content must be JSON-serializable for a Responses function_call_output item",
			err,
		)
	}
	return core.ResponsesInputElement{
		Type:        "function_call_output",
		CallID:      msg.ToolCallID,
		Output:      output,
		ExtraFields: core.CloneUnknownJSONFields(msg.ExtraFields),
	}, nil
}

// chatReasoningReplayItem rebuilds the Responses reasoning item of an
// assistant turn when the message carries replay state in extra_content
// (attached by ConvertResponsesInputToMessages on the way in). Reasoning text
// alone is not replayable, so without replay state the turn emits no
// reasoning item. The item is emitted even when there is no readable
// reasoning text, because a redacted or encrypted reasoning block has none
// and still has to reach the upstream.
func chatReasoningReplayItem(msg core.Message) (core.ResponsesInputElement, bool) {
	replayState := msg.ExtraFields.Lookup(core.ExtraContentField)
	if core.IsJSONNull(replayState) {
		return core.ResponsesInputElement{}, false
	}
	extra := map[string]json.RawMessage{
		"summary":              json.RawMessage(`[]`),
		core.ExtraContentField: replayState,
	}
	if text := chatMessageReasoningText(msg); text != "" {
		content, err := json.Marshal([]map[string]string{{"type": "reasoning_text", "text": text}})
		if err == nil {
			extra["content"] = content
		}
	}
	return core.ResponsesInputElement{
		Type:        "reasoning",
		ExtraFields: core.UnknownJSONFieldsFromMap(extra),
	}, true
}

// chatMessageReasoningText returns the reasoning text recorded on a request
// message, accepting the same member spellings as the response side
// ("reasoning_content", then the vendor "reasoning" member).
func chatMessageReasoningText(msg core.Message) string {
	for _, member := range []string{"reasoning_content", "reasoning"} {
		raw := msg.ExtraFields.Lookup(member)
		if len(raw) == 0 {
			continue
		}
		var content string
		if err := json.Unmarshal(raw, &content); err == nil && content != "" {
			return content
		}
	}
	return ""
}

// chatMessageExtraFieldsForResponses strips the chat-only reasoning members
// consumed by the reasoning replay from a message's unknown fields; every
// other extension travels onto the Responses item unchanged.
func chatMessageExtraFieldsForResponses(fields core.UnknownJSONFields) core.UnknownJSONFields {
	return fields.Without("reasoning_content", "reasoning", core.ExtraContentField)
}

// chatContentToResponsesBlocks converts chat message content into Responses
// content blocks. A plain string becomes a single text block; structured
// parts keep their order. Unknown part types are rejected rather than coerced
// to text.
func chatContentToResponsesBlocks(content any, textType string) ([]any, error) {
	switch c := content.(type) {
	case nil:
		return nil, nil
	case string:
		if c == "" {
			return nil, nil
		}
		return []any{map[string]any{"type": textType, "text": c}}, nil
	case []core.ContentPart:
		return chatPartsToResponsesBlocks(c, textType)
	case []any:
		parts, ok := core.NormalizeContentParts(c)
		if !ok {
			return nil, nil
		}
		return chatPartsToResponsesBlocks(parts, textType)
	default:
		text := core.ExtractTextContent(content)
		if text == "" {
			return nil, nil
		}
		return []any{map[string]any{"type": textType, "text": text}}, nil
	}
}

// chatPartsToResponsesBlocks maps chat content parts onto their Responses
// spellings: "text" becomes the role-appropriate text type, "image_url"
// becomes "input_image", and "file" flattens its nested payload into the
// "input_file" members. Malformed parts of a known type are skipped, matching
// buildResponsesContentItemsFromParts; unknown part types are an error.
func chatPartsToResponsesBlocks(parts []core.ContentPart, textType string) ([]any, error) {
	blocks := make([]any, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "text", "input_text", "output_text":
			if part.Text == "" {
				continue
			}
			blocks = append(blocks, map[string]any{"type": textType, "text": part.Text})
		case "image_url", "input_image":
			if part.ImageURL == nil {
				continue
			}
			url := strings.TrimSpace(part.ImageURL.URL)
			if url == "" {
				continue
			}
			block := map[string]any{"type": "input_image", "image_url": url}
			if detail := strings.TrimSpace(part.ImageURL.Detail); detail != "" {
				block["detail"] = detail
			}
			blocks = append(blocks, block)
		case "input_audio":
			if part.InputAudio == nil {
				continue
			}
			data := strings.TrimSpace(part.InputAudio.Data)
			format := strings.TrimSpace(part.InputAudio.Format)
			if data == "" || format == "" {
				continue
			}
			blocks = append(blocks, map[string]any{
				"type":        "input_audio",
				"input_audio": map[string]any{"data": data, "format": format},
			})
		case "file", "input_file":
			if !core.ValidFilePayload(part.File) {
				continue
			}
			block := map[string]any{"type": "input_file"}
			if fileData := strings.TrimSpace(part.File.FileData); fileData != "" {
				block["file_data"] = fileData
			}
			if fileURL := strings.TrimSpace(part.File.FileURL); fileURL != "" {
				block["file_url"] = fileURL
			}
			if fileID := strings.TrimSpace(part.File.FileID); fileID != "" {
				block["file_id"] = fileID
			}
			if filename := strings.TrimSpace(part.File.Filename); filename != "" {
				block["filename"] = filename
			}
			blocks = append(blocks, block)
		default:
			return nil, core.NewInvalidRequestError(
				fmt.Sprintf("chat message content part type %q is only supported by native chat providers; it has no Responses input equivalent", part.Type),
				nil,
			)
		}
	}
	return blocks, nil
}
