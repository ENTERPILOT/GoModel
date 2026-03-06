package providers

import (
	"context"
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
func ConvertResponsesRequestToChat(req *core.ResponsesRequest) (*core.ChatRequest, error) {
	chatReq := &core.ChatRequest{
		Model:       req.Model,
		Provider:    req.Provider,
		Messages:    make([]core.Message, 0),
		Temperature: req.Temperature,
		Stream:      req.Stream,
	}

	if req.MaxOutputTokens != nil {
		chatReq.MaxTokens = req.MaxOutputTokens
	}

	// Add system instruction if provided
	if req.Instructions != "" {
		chatReq.Messages = append(chatReq.Messages, core.Message{
			Role:    "system",
			Content: req.Instructions,
		})
	}

	// Convert input to messages
	switch input := req.Input.(type) {
	case string:
		chatReq.Messages = append(chatReq.Messages, core.Message{
			Role:    "user",
			Content: input,
		})
	case []interface{}:
		for i, item := range input {
			msgMap, ok := item.(map[string]interface{})
			if !ok {
				return nil, core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: expected object", i), nil)
			}

			role, _ := msgMap["role"].(string)
			if strings.TrimSpace(role) == "" {
				return nil, core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: role is required", i), nil)
			}

			content, ok := ConvertResponsesContentToChatContent(msgMap["content"])
			if !ok {
				return nil, core.NewInvalidRequestError(fmt.Sprintf("invalid responses input item at index %d: unsupported content", i), nil)
			}
			chatReq.Messages = append(chatReq.Messages, core.Message{
				Role:    role,
				Content: content,
			})
		}
	case nil:
		return nil, core.NewInvalidRequestError("invalid responses input: unsupported type", nil)
	default:
		return nil, core.NewInvalidRequestError("invalid responses input: unsupported type", nil)
	}

	return chatReq, nil
}

// ConvertResponsesContentToChatContent maps Responses input content to Chat content.
// Text-only arrays are flattened to strings for broader provider compatibility.
// Any non-text part preserves the array form so multimodal payloads survive routing.
func ConvertResponsesContentToChatContent(content interface{}) (any, bool) {
	switch c := content.(type) {
	case string:
		return c, true
	case []interface{}:
		parts := make([]core.ContentPart, 0, len(c))
		texts := make([]string, 0, len(c))
		textOnly := true
		for _, part := range c {
			partMap, ok := part.(map[string]interface{})
			if !ok {
				return nil, false
			}

			partType, _ := partMap["type"].(string)
			switch partType {
			case "text", "input_text":
				text, ok := partMap["text"].(string)
				if !ok || text == "" {
					return nil, false
				}
				parts = append(parts, core.ContentPart{
					Type: "text",
					Text: text,
				})
				texts = append(texts, text)
			case "image_url", "input_image":
				imageURL, detail, mediaType, ok := extractResponsesImageURL(partMap["image_url"])
				if !ok {
					return nil, false
				}
				textOnly = false
				parts = append(parts, core.ContentPart{
					Type: "image_url",
					ImageURL: &core.ImageURLContent{
						URL:       imageURL,
						Detail:    detail,
						MediaType: mediaType,
					},
				})
			case "input_audio":
				data, format, ok := extractResponsesInputAudio(partMap["input_audio"])
				if !ok {
					return nil, false
				}
				textOnly = false
				parts = append(parts, core.ContentPart{
					Type: "input_audio",
					InputAudio: &core.InputAudioContent{
						Data:   data,
						Format: format,
					},
				})
			default:
				return nil, false
			}
		}
		if len(parts) == 0 {
			return nil, false
		}
		if textOnly {
			return strings.Join(texts, " "), true
		}
		return parts, true
	}
	return nil, false
}

func extractResponsesImageURL(value interface{}) (url string, detail string, mediaType string, ok bool) {
	switch v := value.(type) {
	case string:
		if v == "" {
			return "", "", "", false
		}
		return v, "", "", true
	case map[string]string:
		url = v["url"]
		detail = v["detail"]
		mediaType = v["media_type"]
		return url, detail, mediaType, url != ""
	case map[string]interface{}:
		url, _ = v["url"].(string)
		detail, _ = v["detail"].(string)
		mediaType, _ = v["media_type"].(string)
		return url, detail, mediaType, url != ""
	default:
		return "", "", "", false
	}
}

func extractResponsesInputAudio(value interface{}) (data string, format string, ok bool) {
	switch v := value.(type) {
	case map[string]string:
		data = v["data"]
		format = v["format"]
		return data, format, data != "" && format != ""
	case map[string]interface{}:
		data, _ = v["data"].(string)
		format, _ = v["format"].(string)
		return data, format, data != "" && format != ""
	default:
		return "", "", false
	}
}

// ConvertChatResponseToResponses converts a ChatResponse to a ResponsesResponse.
func ConvertChatResponseToResponses(resp *core.ChatResponse) *core.ResponsesResponse {
	content := ""
	if len(resp.Choices) > 0 {
		content = core.ExtractTextContent(resp.Choices[0].Message.Content)
	}

	return &core.ResponsesResponse{
		ID:        resp.ID,
		Object:    "response",
		CreatedAt: resp.Created,
		Model:     resp.Model,
		Provider:  resp.Provider,
		Status:    "completed",
		Output: []core.ResponsesOutputItem{
			{
				ID:     "msg_" + uuid.New().String(),
				Type:   "message",
				Role:   "assistant",
				Status: "completed",
				Content: []core.ResponsesContentItem{
					{
						Type:        "output_text",
						Text:        content,
						Annotations: []string{},
					},
				},
			},
		},
		Usage: &core.ResponsesUsage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
			TotalTokens:  resp.Usage.TotalTokens,
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

	stream, err := p.StreamChatCompletion(ctx, chatReq)
	if err != nil {
		return nil, err
	}

	return NewOpenAIResponsesStreamConverter(stream, req.Model, providerName), nil
}
