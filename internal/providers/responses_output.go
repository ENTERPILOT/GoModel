package providers

import (
	"strings"

	"github.com/goccy/go-json"

	"github.com/google/uuid"

	"github.com/enterpilot/gomodel/internal/core"
)

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
	case []any:
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
	var output []core.ResponsesOutputItem
	if len(resp.Choices) > 0 {
		output = BuildResponsesOutputItems(resp.Choices[0].Message)
	} else {
		output = []core.ResponsesOutputItem{
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
