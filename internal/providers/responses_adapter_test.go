package providers

import (
	"strings"
	"testing"

	"gomodel/internal/core"
)

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

	tests := []struct {
		name    string
		input   *core.ResponsesRequest
		checkFn func(*testing.T, *core.ChatRequest)
	}{
		{
			name: "string input",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: "Hello",
			},
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				if req.Model != "test-model" {
					t.Errorf("Model = %q, want %q", req.Model, "test-model")
				}
				if len(req.Messages) != 1 {
					t.Errorf("len(Messages) = %d, want 1", len(req.Messages))
				}
				if req.Messages[0].Role != "user" {
					t.Errorf("Messages[0].Role = %q, want %q", req.Messages[0].Role, "user")
				}
				if req.Messages[0].Content != "Hello" {
					t.Errorf("Messages[0].Content = %q, want %q", req.Messages[0].Content, "Hello")
				}
			},
		},
		{
			name: "with instructions",
			input: &core.ResponsesRequest{
				Model:        "test-model",
				Input:        "Hello",
				Instructions: "Be helpful",
			},
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				if len(req.Messages) < 2 {
					t.Fatalf("len(Messages) = %d, want at least 2", len(req.Messages))
				}
				if req.Messages[0].Role != "system" {
					t.Errorf("Messages[0].Role = %q, want %q", req.Messages[0].Role, "system")
				}
				if req.Messages[0].Content != "Be helpful" {
					t.Errorf("Messages[0].Content = %q, want %q", req.Messages[0].Content, "Be helpful")
				}
			},
		},
		{
			name: "with parameters",
			input: &core.ResponsesRequest{
				Model:           "test-model",
				Input:           "Hello",
				Temperature:     &temp,
				MaxOutputTokens: &maxTokens,
				Reasoning:       &core.Reasoning{Effort: "high"},
				StreamOptions:   &core.StreamOptions{IncludeUsage: includeUsage},
			},
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				if req.Temperature == nil || *req.Temperature != 0.7 {
					t.Errorf("Temperature = %v, want 0.7", req.Temperature)
				}
				if req.MaxTokens == nil || *req.MaxTokens != 1024 {
					t.Errorf("MaxTokens = %v, want 1024", req.MaxTokens)
				}
				if req.Reasoning == nil || req.Reasoning.Effort != "high" {
					t.Errorf("Reasoning = %+v, want high effort", req.Reasoning)
				}
				if req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
					t.Errorf("StreamOptions = %+v, want include_usage=true", req.StreamOptions)
				}
			},
		},
		{
			name: "with tool configuration",
			input: &core.ResponsesRequest{
				Model:      "test-model",
				Input:      "Hello",
				Tools:      []map[string]any{{"type": "function", "function": map[string]any{"name": "lookup_weather"}}},
				ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "lookup_weather"}},
			},
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				if len(req.Tools) != 1 {
					t.Fatalf("len(Tools) = %d, want 1", len(req.Tools))
				}
				if req.ToolChoice == nil {
					t.Fatal("ToolChoice should not be nil")
				}
			},
		},
		{
			name: "with parallel tool calls disabled",
			input: func() *core.ResponsesRequest {
				parallelToolCalls := false
				return &core.ResponsesRequest{
					Model:             "test-model",
					Input:             "Hello",
					ParallelToolCalls: &parallelToolCalls,
				}
			}(),
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				if req.ParallelToolCalls == nil || *req.ParallelToolCalls {
					t.Fatalf("ParallelToolCalls = %#v, want false", req.ParallelToolCalls)
				}
			},
		},
		{
			name: "with streaming enabled",
			input: &core.ResponsesRequest{
				Model:  "test-model",
				Input:  "Hello",
				Stream: true,
			},
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				if !req.Stream {
					t.Error("Stream should be true")
				}
			},
		},
		{
			name: "array input with messages",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: []interface{}{
					map[string]interface{}{
						"role":    "user",
						"content": "Hello",
					},
					map[string]interface{}{
						"role":    "assistant",
						"content": "Hi there!",
					},
				},
			},
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				if len(req.Messages) != 2 {
					t.Fatalf("len(Messages) = %d, want 2", len(req.Messages))
				}
				if req.Messages[0].Role != "user" {
					t.Errorf("Messages[0].Role = %q, want %q", req.Messages[0].Role, "user")
				}
				if req.Messages[0].Content != "Hello" {
					t.Errorf("Messages[0].Content = %q, want %q", req.Messages[0].Content, "Hello")
				}
				if req.Messages[1].Role != "assistant" {
					t.Errorf("Messages[1].Role = %q, want %q", req.Messages[1].Role, "assistant")
				}
			},
		},
		{
			name: "array input with function call loop items",
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
				if req.Messages[0].Role != "assistant" {
					t.Fatalf("Messages[0].Role = %q, want assistant", req.Messages[0].Role)
				}
				if len(req.Messages[0].ToolCalls) != 1 {
					t.Fatalf("len(Messages[0].ToolCalls) = %d, want 1", len(req.Messages[0].ToolCalls))
				}
				if req.Messages[0].ToolCalls[0].ID != "call_123" {
					t.Fatalf("ToolCall ID = %q, want call_123", req.Messages[0].ToolCalls[0].ID)
				}
				if req.Messages[0].ToolCalls[0].Function.Name != "lookup_weather" {
					t.Fatalf("ToolCall name = %q, want lookup_weather", req.Messages[0].ToolCalls[0].Function.Name)
				}
				if req.Messages[1].Role != "tool" {
					t.Fatalf("Messages[1].Role = %q, want tool", req.Messages[1].Role)
				}
				if req.Messages[1].ToolCallID != "call_123" {
					t.Fatalf("Messages[1].ToolCallID = %q, want call_123", req.Messages[1].ToolCallID)
				}
				if req.Messages[1].Content != `{"temperature_c":21}` {
					t.Fatalf("Messages[1].Content = %q, want canonical JSON", req.Messages[1].Content)
				}
			},
		},
		{
			name: "array input merges assistant message and function call items",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: []interface{}{
					map[string]interface{}{
						"type":   "message",
						"role":   "assistant",
						"status": "completed",
						"content": []map[string]interface{}{
							{
								"type": "output_text",
								"text": "I'll check that for you.",
							},
						},
					},
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
				if req.Messages[0].Role != "assistant" {
					t.Fatalf("Messages[0].Role = %q, want assistant", req.Messages[0].Role)
				}
				if req.Messages[0].Content != "I'll check that for you." {
					t.Fatalf("Messages[0].Content = %q, want assistant preamble", req.Messages[0].Content)
				}
				if len(req.Messages[0].ToolCalls) != 1 {
					t.Fatalf("len(Messages[0].ToolCalls) = %d, want 1", len(req.Messages[0].ToolCalls))
				}
				if req.Messages[0].ToolCalls[0].ID != "call_123" {
					t.Fatalf("Messages[0].ToolCalls[0].ID = %q, want call_123", req.Messages[0].ToolCalls[0].ID)
				}
				if req.Messages[1].Role != "tool" || req.Messages[1].ToolCallID != "call_123" {
					t.Fatalf("Messages[1] = %+v, want tool result for call_123", req.Messages[1])
				}
			},
		},
		{
			name: "array input with []map[string]any",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: []map[string]any{
					{
						"role":    "user",
						"content": "Hello",
					},
					{
						"type":      "function_call",
						"call_id":   "call_123",
						"name":      "lookup_weather",
						"arguments": `{"city":"Warsaw"}`,
					},
				},
			},
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				if len(req.Messages) != 2 {
					t.Fatalf("len(Messages) = %d, want 2", len(req.Messages))
				}
				if req.Messages[0].Role != "user" || req.Messages[0].Content != "Hello" {
					t.Fatalf("Messages[0] = %+v, want user/Hello", req.Messages[0])
				}
				if req.Messages[1].Role != "assistant" {
					t.Fatalf("Messages[1].Role = %q, want assistant", req.Messages[1].Role)
				}
				if len(req.Messages[1].ToolCalls) != 1 || req.Messages[1].ToolCalls[0].Function.Name != "lookup_weather" {
					t.Fatalf("Messages[1].ToolCalls = %+v, want lookup_weather", req.Messages[1].ToolCalls)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertResponsesRequestToChat(tt.input)
			tt.checkFn(t, result)
		})
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
				Message: core.Message{
					Role:    "assistant",
					Content: "Hello! How can I help you today?",
				},
				FinishReason: "stop",
			},
		},
		Usage: core.Usage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}

	result := ConvertChatResponseToResponses(resp)

	if result.ID != "chatcmpl-123" {
		t.Errorf("ID = %q, want %q", result.ID, "chatcmpl-123")
	}
	if result.Object != "response" {
		t.Errorf("Object = %q, want %q", result.Object, "response")
	}
	if result.Model != "test-model" {
		t.Errorf("Model = %q, want %q", result.Model, "test-model")
	}
	if result.Status != "completed" {
		t.Errorf("Status = %q, want %q", result.Status, "completed")
	}
	if len(result.Output) != 1 {
		t.Fatalf("len(Output) = %d, want 1", len(result.Output))
	}
	if result.Output[0].Type != "message" {
		t.Errorf("Output[0].Type = %q, want %q", result.Output[0].Type, "message")
	}
	if result.Output[0].Role != "assistant" {
		t.Errorf("Output[0].Role = %q, want %q", result.Output[0].Role, "assistant")
	}
	if result.Output[0].Status != "completed" {
		t.Errorf("Output[0].Status = %q, want %q", result.Output[0].Status, "completed")
	}
	if len(result.Output[0].Content) != 1 {
		t.Fatalf("len(Output[0].Content) = %d, want 1", len(result.Output[0].Content))
	}
	if result.Output[0].Content[0].Type != "output_text" {
		t.Errorf("Content[0].Type = %q, want %q", result.Output[0].Content[0].Type, "output_text")
	}
	if result.Output[0].Content[0].Text != "Hello! How can I help you today?" {
		t.Errorf("Content[0].Text = %q, want %q", result.Output[0].Content[0].Text, "Hello! How can I help you today?")
	}
	if result.Usage == nil {
		t.Fatal("Usage should not be nil")
	}
	if result.Usage.InputTokens != 10 {
		t.Errorf("InputTokens = %d, want 10", result.Usage.InputTokens)
	}
	if result.Usage.OutputTokens != 20 {
		t.Errorf("OutputTokens = %d, want 20", result.Usage.OutputTokens)
	}
	if result.Usage.TotalTokens != 30 {
		t.Errorf("TotalTokens = %d, want 30", result.Usage.TotalTokens)
	}
}

func TestConvertChatResponseToResponses_WithToolCalls(t *testing.T) {
	resp := &core.ChatResponse{
		ID:      "chatcmpl-123",
		Object:  "chat.completion",
		Model:   "test-model",
		Created: 1677652288,
		Choices: []core.Choice{
			{
				Index: 0,
				Message: core.Message{
					Role:    "assistant",
					Content: "I'll call the weather tool.",
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
		},
	}

	result := ConvertChatResponseToResponses(resp)

	if len(result.Output) != 2 {
		t.Fatalf("len(Output) = %d, want 2", len(result.Output))
	}
	if result.Output[1].Type != "function_call" {
		t.Fatalf("Output[1].Type = %q, want function_call", result.Output[1].Type)
	}
	if result.Output[1].CallID != "call_123" {
		t.Fatalf("Output[1].CallID = %q, want call_123", result.Output[1].CallID)
	}
	if result.Output[1].Name != "lookup_weather" {
		t.Fatalf("Output[1].Name = %q, want lookup_weather", result.Output[1].Name)
	}
	if result.Output[1].Arguments != `{"city":"Warsaw"}` {
		t.Fatalf("Output[1].Arguments = %q, want tool arguments", result.Output[1].Arguments)
	}
}

func TestConvertChatResponseToResponses_EmptyChoices(t *testing.T) {
	resp := &core.ChatResponse{
		ID:      "chatcmpl-123",
		Object:  "chat.completion",
		Model:   "test-model",
		Created: 1677652288,
		Choices: []core.Choice{},
		Usage: core.Usage{
			PromptTokens:     10,
			CompletionTokens: 0,
			TotalTokens:      10,
		},
	}

	result := ConvertChatResponseToResponses(resp)

	if len(result.Output) != 1 {
		t.Fatalf("len(Output) = %d, want 1", len(result.Output))
	}
	// Content should be empty string when no choices
	if result.Output[0].Content[0].Text != "" {
		t.Errorf("Content[0].Text = %q, want empty string", result.Output[0].Content[0].Text)
	}
}

func TestExtractContentFromInput(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected string
	}{
		{
			name:     "string input",
			input:    "Hello world",
			expected: "Hello world",
		},
		{
			name: "array with text parts",
			input: []interface{}{
				map[string]interface{}{
					"type": "text",
					"text": "Hello",
				},
				map[string]interface{}{
					"type": "text",
					"text": "world",
				},
			},
			expected: "Hello world",
		},
		{
			name:     "nil input",
			input:    nil,
			expected: "",
		},
		{
			name:     "unsupported type",
			input:    12345,
			expected: "",
		},
		{
			name: "array with non-text parts",
			input: []interface{}{
				map[string]interface{}{
					"type": "image",
					"url":  "http://example.com/image.png",
				},
			},
			expected: "",
		},
		{
			name: "nested []map content",
			input: []map[string]any{
				{
					"type": "message",
					"content": []map[string]any{
						{
							"type": "output_text",
							"text": "Hello",
						},
						{
							"type": "wrapper",
							"content": []interface{}{
								map[string]any{
									"type": "output_text",
									"text": "world",
								},
							},
						},
					},
				},
			},
			expected: "Hello world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractContentFromInput(tt.input)
			if result != tt.expected {
				t.Errorf("ExtractContentFromInput(%v) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
