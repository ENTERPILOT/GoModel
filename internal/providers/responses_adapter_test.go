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
			name: "typed multimodal input",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: []core.ResponsesInputItem{
					{
						Role: " user ",
						Content: []core.ResponsesContentPart{
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

func boolPtr(v bool) *bool {
	return &v
}
