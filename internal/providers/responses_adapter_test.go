package providers

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"strings"
	"testing"

	"gomodel/internal/core"
)

type capturingChatProvider struct {
	capturedReq *core.ChatRequest
	streamData  string
	streamErr   error
}

func (p *capturingChatProvider) ChatCompletion(_ context.Context, _ *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, nil
}

func (p *capturingChatProvider) StreamChatCompletion(_ context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	p.capturedReq = req
	if p.streamErr != nil {
		return nil, p.streamErr
	}
	return io.NopCloser(strings.NewReader(p.streamData)), nil
}

func TestResponsesFunctionCallIDs(t *testing.T) {
	t.Run("preserve explicit call id", func(t *testing.T) {
		const callID = "call_123"
		if got := ResponsesFunctionCallCallID(callID); got != callID {
			t.Fatalf("ResponsesFunctionCallCallID(%q) = %q, want %q", callID, got, callID)
		}
		if got := ResponsesFunctionCallItemID(callID); got != "fc_"+callID {
			t.Fatalf("ResponsesFunctionCallItemID(%q) = %q, want %q", callID, got, "fc_"+callID)
		}
	})

	t.Run("generate ids when empty", func(t *testing.T) {
		callID := ResponsesFunctionCallCallID("  ")
		if !strings.HasPrefix(callID, "call_") {
			t.Fatalf("generated call id = %q, want prefix call_", callID)
		}

		itemID := ResponsesFunctionCallItemID("")
		if !strings.HasPrefix(itemID, "fc_call_") {
			t.Fatalf("generated item id = %q, want prefix fc_call_", itemID)
		}
	})
}

func TestConvertResponsesRequestToChat(t *testing.T) {
	temp := 0.7
	maxTokens := 1024
	includeUsage := true
	mustResponsesRequest := func(data string) *core.ResponsesRequest {
		t.Helper()
		var req core.ResponsesRequest
		if err := json.Unmarshal([]byte(data), &req); err != nil {
			t.Fatalf("unmarshal responses request: %v", err)
		}
		return &req
	}

	tests := []struct {
		name      string
		input     *core.ResponsesRequest
		expectErr bool
		checkFn   func(*testing.T, *core.ChatRequest)
	}{
		{
			name: "string input",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: "Hello",
			},
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				if req.Model != "test-model" {
					t.Errorf("Model = %q, want test-model", req.Model)
				}
				if len(req.Messages) != 1 {
					t.Fatalf("len(Messages) = %d, want 1", len(req.Messages))
				}
				if req.Messages[0].Role != "user" {
					t.Errorf("Messages[0].Role = %q, want user", req.Messages[0].Role)
				}
				if got := core.ExtractTextContent(req.Messages[0].Content); got != "Hello" {
					t.Errorf("Messages[0].Content = %q, want Hello", got)
				}
			},
		},
		{
			name: "with instructions and options",
			input: &core.ResponsesRequest{
				Model:             "test-model",
				Input:             "Hello",
				Instructions:      "Be helpful",
				Temperature:       &temp,
				MaxOutputTokens:   &maxTokens,
				Reasoning:         &core.Reasoning{Effort: "high"},
				StreamOptions:     &core.StreamOptions{IncludeUsage: includeUsage},
				Tools:             []map[string]any{{"type": "function", "function": map[string]any{"name": "lookup_weather"}}},
				ToolChoice:        map[string]any{"type": "function", "function": map[string]any{"name": "lookup_weather"}},
				ParallelToolCalls: boolPtr(false),
			},
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				if len(req.Messages) != 2 || req.Messages[0].Role != "system" {
					t.Fatalf("unexpected messages: %+v", req.Messages)
				}
				if req.MaxTokens == nil || *req.MaxTokens != 1024 {
					t.Fatalf("MaxTokens = %#v, want 1024", req.MaxTokens)
				}
				if req.Reasoning == nil || req.Reasoning.Effort != "high" {
					t.Fatalf("Reasoning = %+v, want high", req.Reasoning)
				}
				if req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
					t.Fatalf("StreamOptions = %+v, want include_usage=true", req.StreamOptions)
				}
				if len(req.Tools) != 1 || req.ToolChoice == nil {
					t.Fatalf("tool configuration not preserved: %+v %+v", req.Tools, req.ToolChoice)
				}
				if req.ParallelToolCalls == nil || *req.ParallelToolCalls {
					t.Fatalf("ParallelToolCalls = %#v, want false", req.ParallelToolCalls)
				}
			},
		},
		{
			name: "normalizes native responses tool format",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: "Hello",
				Tools: []map[string]any{
					{
						"type":        "function",
						"name":        "lookup_weather",
						"description": "Get weather by city.",
						"parameters": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"city": map[string]any{"type": "string"},
							},
						},
					},
				},
				ToolChoice: map[string]any{
					"type": "function",
					"name": "lookup_weather",
				},
			},
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				if len(req.Tools) != 1 {
					t.Fatalf("len(Tools) = %d, want 1", len(req.Tools))
				}

				function, ok := req.Tools[0]["function"].(map[string]any)
				if !ok {
					t.Fatalf("Tools[0].function = %#v, want object", req.Tools[0]["function"])
				}
				if function["name"] != "lookup_weather" {
					t.Fatalf("Tools[0].function.name = %#v, want lookup_weather", function["name"])
				}
				if _, ok := req.Tools[0]["name"]; ok {
					t.Fatalf("Tools[0].name should be wrapped into function, got %+v", req.Tools[0])
				}

				toolChoice, ok := req.ToolChoice.(map[string]any)
				if !ok {
					t.Fatalf("ToolChoice = %#v, want object", req.ToolChoice)
				}
				selected, ok := toolChoice["function"].(map[string]any)
				if !ok {
					t.Fatalf("ToolChoice.function = %#v, want object", toolChoice["function"])
				}
				if selected["name"] != "lookup_weather" {
					t.Fatalf("ToolChoice.function.name = %#v, want lookup_weather", selected["name"])
				}
				if _, ok := toolChoice["name"]; ok {
					t.Fatalf("ToolChoice.name should be wrapped into function, got %+v", toolChoice)
				}
			},
		},
		{
			name: "typed multimodal input",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: []core.ResponsesInputElement{
					{
						Role: " user ",
						Content: []core.ContentPart{
							{Type: "input_text", Text: "Describe the image."},
							{
								Type: "input_image",
								ImageURL: &core.ImageURLContent{
									URL:    "https://example.com/image.png",
									Detail: "high",
								},
							},
						},
					},
				},
			},
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				if len(req.Messages) != 1 {
					t.Fatalf("len(Messages) = %d, want 1", len(req.Messages))
				}
				parts, ok := req.Messages[0].Content.([]core.ContentPart)
				if !ok {
					t.Fatalf("Messages[0].Content type = %T, want []core.ContentPart", req.Messages[0].Content)
				}
				if len(parts) != 2 || parts[1].ImageURL == nil || parts[1].ImageURL.URL != "https://example.com/image.png" {
					t.Fatalf("unexpected multimodal content: %+v", parts)
				}
			},
		},
		{
			name: "function call loop items",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: []interface{}{
					map[string]interface{}{
						"type":      "function_call",
						"call_id":   "call_123",
						"name":      "lookup_weather",
						"arguments": `{"city":"Warsaw"}`,
					},
					map[string]interface{}{
						"type":    "function_call_output",
						"call_id": "call_123",
						"output":  map[string]interface{}{"temperature_c": 21},
					},
				},
			},
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				if len(req.Messages) != 2 {
					t.Fatalf("len(Messages) = %d, want 2", len(req.Messages))
				}
				if len(req.Messages[0].ToolCalls) != 1 || req.Messages[0].ToolCalls[0].ID != "call_123" {
					t.Fatalf("unexpected assistant tool_calls: %+v", req.Messages[0].ToolCalls)
				}
				if !req.Messages[0].ContentNull {
					t.Fatal("assistant function_call history should preserve null content")
				}
				if req.Messages[1].Role != "tool" || req.Messages[1].ToolCallID != "call_123" {
					t.Fatalf("unexpected tool result message: %+v", req.Messages[1])
				}
			},
		},
		{
			name: "typed function call output stringifies structured output",
			input: mustResponsesRequest(`{
				"model":"test-model",
				"input":[{
					"type":"function_call_output",
					"call_id":"call_456",
					"output":{"temperature_c":21},
					"x_meta":true
				}]
			}`),
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				if len(req.Messages) != 1 {
					t.Fatalf("len(Messages) = %d, want 1", len(req.Messages))
				}
				if req.Messages[0].Role != "tool" || req.Messages[0].ToolCallID != "call_456" {
					t.Fatalf("unexpected tool result message: %+v", req.Messages[0])
				}
				if got := req.Messages[0].Content; got != `{"temperature_c":21}` {
					t.Fatalf("Content = %#v, want serialized object", got)
				}
				if req.Messages[0].ExtraFields.Lookup("x_meta") == nil {
					t.Fatal("tool result extra missing")
				}
			},
		},
		{
			name: "assistant text merges with later function call item",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: []interface{}{
					map[string]interface{}{
						"type":   "message",
						"role":   "assistant",
						"status": "completed",
						"content": []map[string]interface{}{
							{"type": "output_text", "text": "I'll check that for you."},
						},
					},
					map[string]interface{}{
						"type":      "function_call",
						"call_id":   "call_123",
						"name":      "lookup_weather",
						"arguments": `{"city":"Warsaw"}`,
					},
				},
			},
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				if len(req.Messages) != 1 {
					t.Fatalf("len(Messages) = %d, want 1", len(req.Messages))
				}
				if got := core.ExtractTextContent(req.Messages[0].Content); got != "I'll check that for you." {
					t.Fatalf("Messages[0].Content = %q, want assistant preamble", got)
				}
				if len(req.Messages[0].ToolCalls) != 1 {
					t.Fatalf("len(Messages[0].ToolCalls) = %d, want 1", len(req.Messages[0].ToolCalls))
				}
			},
		},
		{
			name: "assistant structured content merges with later function call item",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: []interface{}{
					map[string]interface{}{
						"type":   "message",
						"role":   "assistant",
						"status": "completed",
						"content": []map[string]interface{}{
							{"type": "output_text", "text": "I'll check that for you."},
							{"type": "input_image", "image_url": map[string]interface{}{"url": "https://example.com/image.png"}},
						},
					},
					map[string]interface{}{
						"type":      "function_call",
						"call_id":   "call_123",
						"name":      "lookup_weather",
						"arguments": `{"city":"Warsaw"}`,
					},
				},
			},
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				if len(req.Messages) != 1 {
					t.Fatalf("len(Messages) = %d, want 1", len(req.Messages))
				}
				parts, ok := req.Messages[0].Content.([]core.ContentPart)
				if !ok {
					t.Fatalf("Messages[0].Content type = %T, want []core.ContentPart", req.Messages[0].Content)
				}
				if len(parts) != 2 || parts[0].Text != "I'll check that for you." || parts[1].ImageURL == nil || parts[1].ImageURL.URL != "https://example.com/image.png" {
					t.Fatalf("unexpected structured assistant content: %+v", parts)
				}
				if len(req.Messages[0].ToolCalls) != 1 || req.Messages[0].ToolCalls[0].ID != "call_123" {
					t.Fatalf("unexpected assistant tool_calls: %+v", req.Messages[0].ToolCalls)
				}
			},
		},
		{
			name: "invalid content fails",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: []interface{}{
					map[string]interface{}{
						"role": "user",
						"content": []interface{}{
							map[string]interface{}{"type": "unknown"},
						},
					},
				},
			},
			expectErr: true,
		},
		{
			name: "nil input fails",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: nil,
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ConvertResponsesRequestToChat(tt.input)
			if tt.expectErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("ConvertResponsesRequestToChat() error = %v", err)
			}
			tt.checkFn(t, result)
		})
	}
}

func TestConvertResponsesRequestToChat_DoesNotMergeAssistantMessagesWithExtraFields(t *testing.T) {
	req := &core.ResponsesRequest{
		Model: "test-model",
		Input: []core.ResponsesInputElement{
			{
				Type:        "message",
				Role:        "assistant",
				Content:     "first",
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"x_first": json.RawMessage(`true`)}),
			},
			{
				Type:        "message",
				Role:        "assistant",
				Content:     "second",
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"x_second": json.RawMessage(`true`)}),
			},
		},
	}

	chatReq, err := ConvertResponsesRequestToChat(req)
	if err != nil {
		t.Fatalf("ConvertResponsesRequestToChat() error = %v", err)
	}
	if len(chatReq.Messages) != 2 {
		t.Fatalf("len(Messages) = %d, want 2", len(chatReq.Messages))
	}
	if chatReq.Messages[0].ExtraFields.Lookup("x_first") == nil {
		t.Fatal("first assistant extra missing")
	}
	if chatReq.Messages[1].ExtraFields.Lookup("x_second") == nil {
		t.Fatal("second assistant extra missing")
	}
}

func TestConvertResponsesRequestToChat_RejectsWhitespaceOnlyMediaFields(t *testing.T) {
	tests := []struct {
		name  string
		input any
	}{
		{
			name: "typed image url",
			input: []core.ResponsesInputElement{
				{
					Type: "message",
					Role: "user",
					Content: []core.ContentPart{
						{
							Type:     "image_url",
							ImageURL: &core.ImageURLContent{URL: "   "},
						},
					},
				},
			},
		},
		{
			name: "map input audio",
			input: []interface{}{
				map[string]interface{}{
					"type": "message",
					"role": "user",
					"content": []map[string]interface{}{
						{
							"type":        "input_audio",
							"input_audio": map[string]interface{}{"data": "  ", "format": "wav"},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ConvertResponsesRequestToChat(&core.ResponsesRequest{
				Model: "test-model",
				Input: tt.input,
			})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "unsupported content") {
				t.Fatalf("error = %v, want unsupported content", err)
			}
		})
	}
}

func TestConvertResponsesRequestToChat_PreservesOpaqueExtras(t *testing.T) {
	req := &core.ResponsesRequest{
		Model: "test-model",
		Input: []core.ResponsesInputElement{
			{
				Role: "user",
				Content: []core.ContentPart{
					{
						Type: "input_text",
						Text: "Describe this",
						ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
							"cache_control": json.RawMessage(`{"type":"ephemeral"}`),
						}),
					},
				},
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					"x_message_hint": json.RawMessage(`true`),
				}),
			},
		},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"response_format": json.RawMessage(`{"type":"json_schema"}`),
		}),
	}

	chatReq, err := ConvertResponsesRequestToChat(req)
	if err != nil {
		t.Fatalf("ConvertResponsesRequestToChat() error = %v", err)
	}

	if chatReq.ExtraFields.Lookup("response_format") == nil {
		t.Fatal("response_format missing from chat request extras")
	}
	if len(chatReq.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(chatReq.Messages))
	}
	if chatReq.Messages[0].ExtraFields.Lookup("x_message_hint") == nil {
		t.Fatal("message extra missing after conversion")
	}

	parts, ok := chatReq.Messages[0].Content.([]core.ContentPart)
	if !ok {
		t.Fatalf("Messages[0].Content type = %T, want []core.ContentPart to preserve part extras", chatReq.Messages[0].Content)
	}
	if parts[0].ExtraFields.Lookup("cache_control") == nil {
		t.Fatal("content part extra missing after conversion")
	}
}

func TestConvertResponsesRequestToChat_PreservesUnknownMapFields(t *testing.T) {
	req := &core.ResponsesRequest{
		Model: "test-model",
		Input: []interface{}{
			map[string]interface{}{
				"type":      "function_call",
				"call_id":   "call_123",
				"name":      "lookup_weather",
				"arguments": `{"city":"Warsaw"}`,
				"x_trace":   map[string]interface{}{"attempt": 2},
			},
			map[string]interface{}{
				"type":    "message",
				"role":    "user",
				"content": []map[string]interface{}{{"type": "output_text", "text": "hello", "cache_control": map[string]interface{}{"type": "ephemeral"}}},
				"x_meta":  "keep-me",
			},
			map[string]interface{}{
				"type": "message",
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{
						"type": "input_image",
						"image_url": map[string]string{
							"url":        "https://example.com/image.png",
							"detail":     "high",
							"media_type": "image/png",
							"x_nested":   "keep-image",
						},
					},
					map[string]interface{}{
						"type": "input_audio",
						"input_audio": map[string]string{
							"data":     "aGVsbG8=",
							"format":   "wav",
							"x_nested": "keep-audio",
						},
					},
				},
			},
		},
	}

	chatReq, err := ConvertResponsesRequestToChat(req)
	if err != nil {
		t.Fatalf("ConvertResponsesRequestToChat() error = %v", err)
	}
	if len(chatReq.Messages) != 3 {
		t.Fatalf("len(Messages) = %d, want 3", len(chatReq.Messages))
	}
	if len(chatReq.Messages[0].ToolCalls) != 1 {
		t.Fatalf("len(Messages[0].ToolCalls) = %d, want 1", len(chatReq.Messages[0].ToolCalls))
	}
	if chatReq.Messages[0].ToolCalls[0].ExtraFields.Lookup("x_trace") == nil {
		t.Fatal("tool_call extra missing after conversion")
	}
	if chatReq.Messages[1].ExtraFields.Lookup("x_meta") == nil {
		t.Fatal("message extra missing after map conversion")
	}

	parts, ok := chatReq.Messages[1].Content.([]core.ContentPart)
	if !ok {
		t.Fatalf("Messages[1].Content type = %T, want []core.ContentPart to preserve mapped text-part extras", chatReq.Messages[1].Content)
	}
	if parts[0].ExtraFields.Lookup("cache_control") == nil {
		t.Fatal("mapped content part extra missing after conversion")
	}

	multimodalParts, ok := chatReq.Messages[2].Content.([]core.ContentPart)
	if !ok || len(multimodalParts) != 2 {
		t.Fatalf("Messages[2].Content = %#v, want []core.ContentPart len=2", chatReq.Messages[2].Content)
	}
	if multimodalParts[0].ImageURL == nil || multimodalParts[0].ImageURL.ExtraFields.Lookup("x_nested") == nil {
		t.Fatalf("image_url extra missing after map[string]string conversion: %+v", multimodalParts[0].ImageURL)
	}
	if multimodalParts[1].InputAudio == nil || multimodalParts[1].InputAudio.ExtraFields.Lookup("x_nested") == nil {
		t.Fatalf("input_audio extra missing after map[string]string conversion: %+v", multimodalParts[1].InputAudio)
	}
}

func TestConvertChatResponseToResponses(t *testing.T) {
	resp := &core.ChatResponse{
		ID:      "chatcmpl-123",
		Object:  "chat.completion",
		Model:   "test-model",
		Created: 1677652288,
		Choices: []core.Choice{
			{
				Index: 0,
				Message: core.ResponseMessage{
					Role:    "assistant",
					Content: "Hello! How can I help you today?",
					ToolCalls: []core.ToolCall{
						{
							ID:   "call_123",
							Type: "function",
							Function: core.FunctionCall{
								Name:      "lookup_weather",
								Arguments: `{"city":"Warsaw"}`,
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
		},
		Usage: core.Usage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
			PromptTokensDetails: &core.PromptTokensDetails{
				CachedTokens: 1,
			},
			CompletionTokensDetails: &core.CompletionTokensDetails{
				ReasoningTokens: 3,
			},
			RawUsage: map[string]any{"provider": "test"},
		},
	}

	result := ConvertChatResponseToResponses(resp)

	if len(result.Output) != 2 {
		t.Fatalf("len(Output) = %d, want 2", len(result.Output))
	}
	if result.Output[0].Type != "message" || result.Output[1].Type != "function_call" {
		t.Fatalf("unexpected output items: %+v", result.Output)
	}
	if result.Output[1].CallID != "call_123" {
		t.Fatalf("Output[1].CallID = %q, want call_123", result.Output[1].CallID)
	}
	if result.Usage == nil || result.Usage.PromptTokensDetails == nil || result.Usage.CompletionTokensDetails == nil {
		t.Fatalf("usage details not preserved: %+v", result.Usage)
	}
	if result.Usage.RawUsage["provider"] != "test" {
		t.Fatalf("RawUsage = %+v, want provider=test", result.Usage.RawUsage)
	}
}

func TestConvertChatResponseToResponses_PreservesStructuredAssistantContent(t *testing.T) {
	resp := &core.ChatResponse{
		ID:      "chatcmpl-structured",
		Object:  "chat.completion",
		Model:   "test-model",
		Created: 1677652288,
		Choices: []core.Choice{
			{
				Index: 0,
				Message: core.ResponseMessage{
					Role: "assistant",
					Content: []core.ContentPart{
						{Type: "text", Text: "Here is the result."},
						{
							Type: "image_url",
							ImageURL: &core.ImageURLContent{
								URL:         "https://example.com/result.png",
								ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"x_image": json.RawMessage(`true`)}),
							},
						},
						{
							Type: "input_audio",
							InputAudio: &core.InputAudioContent{
								Data:        "YWJj",
								Format:      "wav",
								ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"x_audio": json.RawMessage(`true`)}),
							},
						},
					},
				},
				FinishReason: "stop",
			},
		},
	}

	result := ConvertChatResponseToResponses(resp)

	if len(result.Output) != 1 {
		t.Fatalf("len(Output) = %d, want 1", len(result.Output))
	}
	if result.Output[0].Type != "message" {
		t.Fatalf("Output[0].Type = %q, want message", result.Output[0].Type)
	}
	if len(result.Output[0].Content) != 3 {
		t.Fatalf("len(Output[0].Content) = %d, want 3 structured content items", len(result.Output[0].Content))
	}
	if result.Output[0].Content[0].Type != "output_text" || result.Output[0].Content[0].Text != "Here is the result." {
		t.Fatalf("unexpected text content item: %+v", result.Output[0].Content[0])
	}
	if result.Output[0].Content[1].Type != "input_image" {
		t.Fatalf("expected preserved non-text content item, got %+v", result.Output[0].Content[1])
	}
	if result.Output[0].Content[1].ImageURL == nil || result.Output[0].Content[1].ImageURL.URL != "https://example.com/result.png" {
		t.Fatalf("unexpected preserved image content item: %+v", result.Output[0].Content[1])
	}
	if result.Output[0].Content[1].ImageURL.ExtraFields.Lookup("x_image") == nil {
		t.Fatalf("image extra missing after conversion: %+v", result.Output[0].Content[1].ImageURL)
	}
	if result.Output[0].Content[2].Type != "input_audio" {
		t.Fatalf("expected preserved audio content item, got %+v", result.Output[0].Content[2])
	}
	if result.Output[0].Content[2].InputAudio == nil || result.Output[0].Content[2].InputAudio.Format != "wav" {
		t.Fatalf("unexpected preserved audio content item: %+v", result.Output[0].Content[2])
	}
	if result.Output[0].Content[2].InputAudio.ExtraFields.Lookup("x_audio") == nil {
		t.Fatalf("audio extra missing after conversion: %+v", result.Output[0].Content[2].InputAudio)
	}
}

func TestConvertResponsesRequestToChat_RejectsNonSerializableFunctionCallOutputMap(t *testing.T) {
	_, err := ConvertResponsesRequestToChat(&core.ResponsesRequest{
		Model: "test-model",
		Input: []interface{}{
			map[string]interface{}{
				"type":    "function_call_output",
				"call_id": "call_123",
				"output":  math.Inf(1),
			},
		},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "function_call_output.output must be JSON-serializable") {
		t.Fatalf("error = %v, want JSON-serializable validation error", err)
	}
}

func TestExtractContentFromInput(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected string
	}{
		{name: "string input", input: "Hello world", expected: "Hello world"},
		{
			name: "nested content",
			input: []map[string]any{
				{
					"type": "message",
					"content": []map[string]any{
						{"type": "output_text", "text": "Hello"},
						{"type": "wrapper", "content": []interface{}{map[string]any{"type": "output_text", "text": "world"}}},
					},
				},
			},
			expected: "Hello world",
		},
		{name: "unsupported type", input: 12345, expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractContentFromInput(tt.input); got != tt.expected {
				t.Fatalf("ExtractContentFromInput(%v) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestConvertResponsesRequestToChat_ClonesStreamOptions(t *testing.T) {
	req := &core.ResponsesRequest{
		Model:         "test-model",
		Input:         "hello",
		Stream:        true,
		StreamOptions: &core.StreamOptions{IncludeUsage: false},
	}

	chatReq, err := ConvertResponsesRequestToChat(req)
	if err != nil {
		t.Fatalf("ConvertResponsesRequestToChat() error = %v", err)
	}
	if chatReq.StreamOptions == nil {
		t.Fatal("StreamOptions = nil, want cloned value")
	}
	if chatReq.StreamOptions == req.StreamOptions {
		t.Fatal("StreamOptions pointer was reused")
	}
	if chatReq.StreamOptions.IncludeUsage {
		t.Fatalf("IncludeUsage = %v, want false", chatReq.StreamOptions.IncludeUsage)
	}
}

func TestStreamResponsesViaChat_InjectsUsageWhenPolicyEnabled(t *testing.T) {
	provider := &capturingChatProvider{
		streamData: "data: [DONE]\n\n",
	}
	req := &core.ResponsesRequest{
		Model:         "gemini-2.0-flash",
		Input:         "hello",
		Stream:        true,
		StreamOptions: &core.StreamOptions{IncludeUsage: false},
	}
	ctx := core.WithEnforceReturningUsageData(context.Background(), true)

	stream, err := StreamResponsesViaChat(ctx, provider, req, "gemini")
	if err != nil {
		t.Fatalf("StreamResponsesViaChat() error = %v", err)
	}
	defer func() {
		_ = stream.Close()
	}()

	if provider.capturedReq == nil {
		t.Fatal("capturedReq = nil")
	}
	if provider.capturedReq.StreamOptions == nil {
		t.Fatal("captured StreamOptions = nil")
	}
	if !provider.capturedReq.StreamOptions.IncludeUsage {
		t.Fatal("captured IncludeUsage = false, want true")
	}
	if req.StreamOptions == nil {
		t.Fatal("original StreamOptions unexpectedly nil")
	}
	if req.StreamOptions.IncludeUsage {
		t.Fatal("original request was mutated")
	}
}

func TestStreamResponsesViaChat_DoesNotInjectUsageWhenPolicyDisabled(t *testing.T) {
	provider := &capturingChatProvider{
		streamData: "data: [DONE]\n\n",
	}
	req := &core.ResponsesRequest{
		Model:  "gemini-2.0-flash",
		Input:  "hello",
		Stream: true,
	}

	stream, err := StreamResponsesViaChat(context.Background(), provider, req, "gemini")
	if err != nil {
		t.Fatalf("StreamResponsesViaChat() error = %v", err)
	}
	defer func() {
		_ = stream.Close()
	}()

	if provider.capturedReq == nil {
		t.Fatal("capturedReq = nil")
	}
	if provider.capturedReq.StreamOptions != nil {
		t.Fatalf("captured StreamOptions = %+v, want nil", provider.capturedReq.StreamOptions)
	}
}

func boolPtr(v bool) *bool {
	return &v
}
