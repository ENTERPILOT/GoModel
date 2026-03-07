package providers

import (
	"context"
	"encoding/json"
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
func ConvertResponsesRequestToChat(req *core.ResponsesRequest) *core.ChatRequest {
	chatReq := &core.ChatRequest{
		Model:             req.Model,
		Provider:          req.Provider,
		Messages:          make([]core.Message, 0),
		Tools:             req.Tools,
		ToolChoice:        req.ToolChoice,
		ParallelToolCalls: req.ParallelToolCalls,
		Temperature:       req.Temperature,
		StreamOptions:     req.StreamOptions,
		Reasoning:         req.Reasoning,
		Stream:            req.Stream,
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

	chatReq.Messages = append(chatReq.Messages, ConvertResponsesInputToMessages(req.Input)...)

	return chatReq
}

// ConvertResponsesInputToMessages converts a Responses API input payload into Chat API messages.
func ConvertResponsesInputToMessages(input interface{}) []core.Message {
	switch in := input.(type) {
	case string:
		return []core.Message{{Role: "user", Content: in}}
	case []map[string]any:
		items := make([]interface{}, 0, len(in))
		for _, item := range in {
			items = append(items, item)
		}
		return convertResponsesInputItems(items)
	case []interface{}:
		return convertResponsesInputItems(in)
	case []core.ResponsesInputItem:
		items := make([]interface{}, 0, len(in))
		for _, item := range in {
			items = append(items, item)
		}
		return convertResponsesInputItems(items)
	default:
		return nil
	}
}

func convertResponsesInputItems(items []interface{}) []core.Message {
	messages := make([]core.Message, 0, len(items))
	var pendingAssistant *core.Message

	flushPendingAssistant := func() {
		if pendingAssistant == nil {
			return
		}
		messages = append(messages, *pendingAssistant)
		pendingAssistant = nil
	}

	for _, item := range items {
		if msg, ok := convertResponsesInputItem(item); ok {
			if msg.Role == "assistant" {
				if pendingAssistant == nil {
					assistant := core.Message{
						Role:       "assistant",
						Content:    msg.Content,
						ToolCallID: msg.ToolCallID,
					}
					if len(msg.ToolCalls) > 0 {
						assistant.ToolCalls = append([]core.ToolCall(nil), msg.ToolCalls...)
					}
					pendingAssistant = &assistant
				} else {
					if msg.Content != "" {
						pendingAssistant.Content += msg.Content
					}
					if len(msg.ToolCalls) > 0 {
						pendingAssistant.ToolCalls = append(pendingAssistant.ToolCalls, msg.ToolCalls...)
					}
				}
				continue
			}

			flushPendingAssistant()
			messages = append(messages, msg)
		}
	}
	flushPendingAssistant()
	return messages
}

func convertResponsesInputItem(item interface{}) (core.Message, bool) {
	switch typed := item.(type) {
	case core.ResponsesInputItem:
		content := ExtractContentFromInput(typed.Content)
		if typed.Role == "" || content == "" {
			return core.Message{}, false
		}
		return core.Message{Role: typed.Role, Content: content}, true
	case map[string]interface{}:
		return convertResponsesInputMap(typed)
	default:
		return core.Message{}, false
	}
}

func convertResponsesInputMap(item map[string]interface{}) (core.Message, bool) {
	itemType, _ := item["type"].(string)
	switch itemType {
	case "function_call":
		name, _ := item["name"].(string)
		callID := firstNonEmptyString(item, "call_id", "id")
		if name == "" {
			return core.Message{}, false
		}
		callID = ResponsesFunctionCallCallID(callID)
		return core.Message{
			Role: "assistant",
			ToolCalls: []core.ToolCall{
				{
					ID:   callID,
					Type: "function",
					Function: core.FunctionCall{
						Name:      name,
						Arguments: stringifyResponsesInputValue(item["arguments"]),
					},
				},
			},
		}, true
	case "function_call_output":
		callID := firstNonEmptyString(item, "call_id")
		if callID == "" {
			return core.Message{}, false
		}
		return core.Message{
			Role:       "tool",
			ToolCallID: callID,
			Content:    stringifyResponsesInputValue(item["output"]),
		}, true
	}

	role, _ := item["role"].(string)
	content := ExtractContentFromInput(item["content"])
	if role == "" || content == "" {
		return core.Message{}, false
	}
	return core.Message{
		Role:    role,
		Content: content,
	}, true
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
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
}

// ExtractContentFromInput extracts text content from responses input.
func ExtractContentFromInput(content interface{}) string {
	switch c := content.(type) {
	case string:
		return c
	case []core.ResponsesContentPart:
		var texts []string
		for _, part := range c {
			if part.Text != "" {
				texts = append(texts, part.Text)
			}
		}
		return strings.Join(texts, " ")
	case []map[string]any:
		return extractTextFromMapSlice(c)
	case []interface{}:
		// Array of content parts - extract text
		var texts []string
		for _, part := range c {
			if partMap, ok := part.(map[string]interface{}); ok {
				if text := extractTextFromInputMap(partMap); text != "" {
					texts = append(texts, text)
				}
			}
		}
		return strings.Join(texts, " ")
	}
	return ""
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
	var texts []string
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

func buildResponsesOutputItems(msg core.Message) []core.ResponsesOutputItem {
	output := make([]core.ResponsesOutputItem, 0, len(msg.ToolCalls)+1)
	if msg.Content != "" || len(msg.ToolCalls) == 0 {
		output = append(output, core.ResponsesOutputItem{
			ID:     "msg_" + uuid.New().String(),
			Type:   "message",
			Role:   "assistant",
			Status: "completed",
			Content: []core.ResponsesContentItem{
				{
					Type:        "output_text",
					Text:        msg.Content,
					Annotations: []string{},
				},
			},
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
					Annotations: []string{},
				},
			},
		},
	}
	if len(resp.Choices) > 0 {
		output = buildResponsesOutputItems(resp.Choices[0].Message)
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
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
			TotalTokens:  resp.Usage.TotalTokens,
		},
	}
}

// ResponsesViaChat implements the Responses API by converting to/from Chat format.
func ResponsesViaChat(ctx context.Context, p ChatProvider, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	chatReq := ConvertResponsesRequestToChat(req)

	chatResp, err := p.ChatCompletion(ctx, chatReq)
	if err != nil {
		return nil, err
	}

	return ConvertChatResponseToResponses(chatResp), nil
}

// StreamResponsesViaChat implements streaming Responses API by converting to/from Chat format.
func StreamResponsesViaChat(ctx context.Context, p ChatProvider, req *core.ResponsesRequest, providerName string) (io.ReadCloser, error) {
	chatReq := ConvertResponsesRequestToChat(req)

	stream, err := p.StreamChatCompletion(ctx, chatReq)
	if err != nil {
		return nil, err
	}

	return NewOpenAIResponsesStreamConverter(stream, req.Model, providerName), nil
}
