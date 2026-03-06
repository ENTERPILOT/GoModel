package providers

import (
	"reflect"
	"testing"

	"gomodel/internal/core"
)

func TestConvertResponsesRequestToChat(t *testing.T) {
	temp := 0.7
	maxTokens := 1024

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
					t.Errorf("Model = %q, want %q", req.Model, "test-model")
				}
				if len(req.Messages) != 1 {
					t.Errorf("len(Messages) = %d, want 1", len(req.Messages))
				}
				if req.Messages[0].Role != "user" {
					t.Errorf("Messages[0].Role = %q, want %q", req.Messages[0].Role, "user")
				}
				if got := core.ExtractTextContent(req.Messages[0].Content); got != "Hello" {
					t.Errorf("Messages[0].Content = %q, want %q", got, "Hello")
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
				if got := core.ExtractTextContent(req.Messages[0].Content); got != "Be helpful" {
					t.Errorf("Messages[0].Content = %q, want %q", got, "Be helpful")
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
			},
			checkFn: func(t *testing.T, req *core.ChatRequest) {
				if req.Temperature == nil || *req.Temperature != 0.7 {
					t.Errorf("Temperature = %v, want 0.7", req.Temperature)
				}
				if req.MaxTokens == nil || *req.MaxTokens != 1024 {
					t.Errorf("MaxTokens = %v, want 1024", req.MaxTokens)
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
				if got := core.ExtractTextContent(req.Messages[0].Content); got != "Hello" {
					t.Errorf("Messages[0].Content = %q, want %q", got, "Hello")
				}
				if req.Messages[1].Role != "assistant" {
					t.Errorf("Messages[1].Role = %q, want %q", req.Messages[1].Role, "assistant")
				}
			},
		},
		{
			name: "array input with multimodal parts",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: []interface{}{
					map[string]interface{}{
						"role": "user",
						"content": []interface{}{
							map[string]interface{}{
								"type": "input_text",
								"text": "Describe the image.",
							},
							map[string]interface{}{
								"type": "input_image",
								"image_url": map[string]interface{}{
									"url":    "https://example.com/image.png",
									"detail": "high",
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
				if len(parts) != 2 {
					t.Fatalf("len(parts) = %d, want 2", len(parts))
				}
				if parts[0].Type != "text" || parts[0].Text != "Describe the image." {
					t.Fatalf("unexpected text part: %+v", parts[0])
				}
				if parts[1].Type != "image_url" || parts[1].ImageURL == nil || parts[1].ImageURL.URL != "https://example.com/image.png" {
					t.Fatalf("unexpected image part: %+v", parts[1])
				}
			},
		},
		{
			name: "array input with malformed item fails",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: []interface{}{"bad-item"},
			},
			expectErr: true,
		},
		{
			name: "array input with malformed content fails",
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
			name: "unsupported input type fails",
			input: &core.ResponsesRequest{
				Model: "test-model",
				Input: 123,
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
				t.Fatalf("unexpected error: %v", err)
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

func TestConvertResponsesContentToChatContent(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected any
		ok       bool
	}{
		{
			name:     "string input",
			input:    "Hello world",
			expected: "Hello world",
			ok:       true,
		},
		{
			name: "array with text parts",
			input: []interface{}{
				map[string]interface{}{
					"type": "input_text",
					"text": "Hello",
				},
				map[string]interface{}{
					"type": "input_text",
					"text": "world",
				},
			},
			expected: "Hello world",
			ok:       true,
		},
		{
			name:  "nil input",
			input: nil,
			ok:    false,
		},
		{
			name:  "unsupported type",
			input: 12345,
			ok:    false,
		},
		{
			name: "array with unknown part type",
			input: []interface{}{
				map[string]interface{}{
					"type": "unknown_part",
				},
			},
			ok: false,
		},
		{
			name: "array with malformed image part",
			input: []interface{}{
				map[string]interface{}{
					"type":      "input_image",
					"image_url": map[string]interface{}{},
				},
			},
			ok: false,
		},
		{
			name: "array with multimodal parts",
			input: []interface{}{
				map[string]interface{}{
					"type": "input_text",
					"text": "Look",
				},
				map[string]interface{}{
					"type": "input_image",
					"image_url": map[string]interface{}{
						"url": "http://example.com/image.png",
					},
				},
			},
			expected: []core.ContentPart{
				{Type: "text", Text: "Look"},
				{
					Type: "image_url",
					ImageURL: &core.ImageURLContent{
						URL: "http://example.com/image.png",
					},
				},
			},
			ok: true,
		},
		{
			name: "array with input audio part",
			input: []interface{}{
				map[string]interface{}{
					"type": "input_audio",
					"input_audio": map[string]interface{}{
						"data":   "abc",
						"format": "wav",
					},
				},
			},
			expected: []core.ContentPart{
				{
					Type: "input_audio",
					InputAudio: &core.InputAudioContent{
						Data:   "abc",
						Format: "wav",
					},
				},
			},
			ok: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := ConvertResponsesContentToChatContent(tt.input)
			if ok != tt.ok {
				t.Fatalf("ConvertResponsesContentToChatContent(%v) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if !tt.ok {
				return
			}
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("ConvertResponsesContentToChatContent(%v) = %#v, want %#v", tt.input, result, tt.expected)
			}
		})
	}
}
