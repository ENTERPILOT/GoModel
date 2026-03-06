package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
)

func TestNew(t *testing.T) {
	apiKey := "test-api-key"
	// Use NewWithHTTPClient to get concrete type for internal testing
	provider := NewWithHTTPClient(apiKey, nil, llmclient.Hooks{})

	if provider.apiKey != apiKey {
		t.Errorf("apiKey = %q, want %q", provider.apiKey, apiKey)
	}
	if provider.client == nil {
		t.Error("client should not be nil")
	}
}

func TestNew_ReturnsProvider(t *testing.T) {
	apiKey := "test-api-key"
	provider := New(apiKey, providers.ProviderOptions{})

	if provider == nil {
		t.Error("provider should not be nil")
	}
}

func TestChatCompletion(t *testing.T) {
	tests := []struct {
		name          string
		statusCode    int
		responseBody  string
		expectedError bool
		checkResponse func(*testing.T, *core.ChatResponse)
	}{
		{
			name:       "successful request",
			statusCode: http.StatusOK,
			responseBody: `{
				"id": "msg_123",
				"type": "message",
				"role": "assistant",
				"model": "claude-sonnet-4-5-20250929",
				"content": [{
					"type": "text",
					"text": "Hello! How can I help you today?"
				}],
				"stop_reason": "end_turn",
				"usage": {
					"input_tokens": 10,
					"output_tokens": 20
				}
			}`,
			expectedError: false,
			checkResponse: func(t *testing.T, resp *core.ChatResponse) {
				if resp.ID != "msg_123" {
					t.Errorf("ID = %q, want %q", resp.ID, "msg_123")
				}
				if resp.Model != "claude-sonnet-4-5-20250929" {
					t.Errorf("Model = %q, want %q", resp.Model, "claude-sonnet-4-5-20250929")
				}
				if len(resp.Choices) != 1 {
					t.Fatalf("len(Choices) = %d, want 1", len(resp.Choices))
				}
				if resp.Choices[0].Message.Content != "Hello! How can I help you today?" {
					t.Errorf("Message content = %q, want %q", resp.Choices[0].Message.Content, "Hello! How can I help you today?")
				}
				if resp.Usage.PromptTokens != 10 {
					t.Errorf("PromptTokens = %d, want 10", resp.Usage.PromptTokens)
				}
				if resp.Usage.CompletionTokens != 20 {
					t.Errorf("CompletionTokens = %d, want 20", resp.Usage.CompletionTokens)
				}
				if resp.Usage.TotalTokens != 30 {
					t.Errorf("TotalTokens = %d, want 30", resp.Usage.TotalTokens)
				}
			},
		},
		{
			name:          "API error - unauthorized",
			statusCode:    http.StatusUnauthorized,
			responseBody:  `{"type": "error", "error": {"type": "authentication_error", "message": "Invalid API key"}}`,
			expectedError: true,
		},
		{
			name:          "rate limit error",
			statusCode:    http.StatusTooManyRequests,
			responseBody:  `{"type": "error", "error": {"type": "rate_limit_error", "message": "Rate limit exceeded"}}`,
			expectedError: true,
		},
		{
			name:          "server error",
			statusCode:    http.StatusInternalServerError,
			responseBody:  `{"type": "error", "error": {"type": "api_error", "message": "Internal server error"}}`,
			expectedError: true,
		},
		{
			name:          "bad request error",
			statusCode:    http.StatusBadRequest,
			responseBody:  `{"type": "error", "error": {"type": "invalid_request_error", "message": "Invalid request"}}`,
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify request headers
				if r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("Content-Type = %q, want %q", r.Header.Get("Content-Type"), "application/json")
				}
				apiKey := r.Header.Get("x-api-key")
				if apiKey == "" {
					t.Error("x-api-key header should not be empty")
				}
				if r.Header.Get("anthropic-version") != anthropicAPIVersion {
					t.Errorf("anthropic-version = %q, want %q", r.Header.Get("anthropic-version"), anthropicAPIVersion)
				}

				// Verify request path
				if r.URL.Path != "/messages" {
					t.Errorf("Path = %q, want %q", r.URL.Path, "/messages")
				}

				// Verify request body
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("failed to read request body: %v", err)
				}
				var req anthropicRequest
				if err := json.Unmarshal(body, &req); err != nil {
					t.Fatalf("failed to unmarshal request: %v", err)
				}

				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			req := &core.ChatRequest{
				Model: "claude-sonnet-4-5-20250929",
				Messages: []core.Message{
					{Role: "user", Content: "Hello"},
				},
			}

			resp, err := provider.ChatCompletion(context.Background(), req)

			if tt.expectedError {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tt.checkResponse != nil {
					tt.checkResponse(t, resp)
				}
			}
		})
	}
}

func TestStreamChatCompletion(t *testing.T) {
	tests := []struct {
		name          string
		statusCode    int
		responseBody  string
		expectedError bool
		checkStream   func(*testing.T, io.ReadCloser)
	}{
		{
			name:       "successful streaming request",
			statusCode: http.StatusOK,
			responseBody: `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"!"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}
`,
			expectedError: false,
			checkStream: func(t *testing.T, body io.ReadCloser) {
				if body == nil {
					t.Fatal("body should not be nil")
				}
				defer func() { _ = body.Close() }()

				// Read and verify the streaming response
				respBody, err := io.ReadAll(body)
				if err != nil {
					t.Fatalf("failed to read response body: %v", err)
				}

				// The response should be converted to OpenAI format
				responseStr := string(respBody)
				if !strings.Contains(responseStr, "data:") {
					t.Error("response should contain SSE data")
				}
				if !strings.Contains(responseStr, "[DONE]") {
					t.Error("response should end with [DONE]")
				}
			},
		},
		{
			name:          "API error - unauthorized",
			statusCode:    http.StatusUnauthorized,
			responseBody:  `{"type": "error", "error": {"type": "authentication_error", "message": "Invalid API key"}}`,
			expectedError: true,
		},
		{
			name:          "rate limit error",
			statusCode:    http.StatusTooManyRequests,
			responseBody:  `{"type": "error", "error": {"type": "rate_limit_error", "message": "Rate limit exceeded"}}`,
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify request headers
				if r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("Content-Type = %q, want %q", r.Header.Get("Content-Type"), "application/json")
				}
				apiKey := r.Header.Get("x-api-key")
				if apiKey == "" {
					t.Error("x-api-key header should not be empty")
				}

				// Verify stream is set in request body
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("failed to read request body: %v", err)
				}
				var req anthropicRequest
				if err := json.Unmarshal(body, &req); err != nil {
					t.Fatalf("failed to unmarshal request: %v", err)
				}
				if !req.Stream {
					t.Error("Stream should be true in request")
				}

				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			req := &core.ChatRequest{
				Model: "claude-sonnet-4-5-20250929",
				Messages: []core.Message{
					{Role: "user", Content: "Hello"},
				},
			}

			body, err := provider.StreamChatCompletion(context.Background(), req)

			if tt.expectedError {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tt.checkStream != nil {
					tt.checkStream(t, body)
				}
			}
		})
	}
}

func TestListModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request path and method
		if r.URL.Path != "/models" {
			t.Errorf("Path = %q, want %q", r.URL.Path, "/models")
		}
		if r.Method != http.MethodGet {
			t.Errorf("Method = %q, want %q", r.Method, http.MethodGet)
		}

		// Verify required headers
		apiKey := r.Header.Get("x-api-key")
		if apiKey == "" {
			t.Error("x-api-key header should not be empty")
		}
		if r.Header.Get("anthropic-version") != anthropicAPIVersion {
			t.Errorf("anthropic-version = %q, want %q", r.Header.Get("anthropic-version"), anthropicAPIVersion)
		}

		// Verify limit query param (passed in URL)
		if limit := r.URL.Query().Get("limit"); limit != "1000" {
			t.Errorf("limit query param = %q, want %q", limit, "1000")
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": [
				{"id": "claude-sonnet-4-5-20250929", "type": "model", "created_at": "2025-09-29T00:00:00Z", "display_name": "Claude Sonnet 4.5"},
				{"id": "claude-opus-4-5-20251101", "type": "model", "created_at": "2025-11-01T00:00:00Z", "display_name": "Claude Opus 4.5"},
				{"id": "claude-3-haiku-20240307", "type": "model", "created_at": "2024-03-07T00:00:00Z", "display_name": "Claude 3 Haiku"}
			],
			"has_more": false
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.ListModels(context.Background())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Object != "list" {
		t.Errorf("Object = %q, want %q", resp.Object, "list")
	}

	if len(resp.Data) != 3 {
		t.Errorf("len(Data) = %d, want 3", len(resp.Data))
	}

	// Verify that all models have the correct fields
	for _, model := range resp.Data {
		if model.ID == "" {
			t.Error("Model ID should not be empty")
		}
		if !strings.HasPrefix(model.ID, "claude-") {
			t.Errorf("Model ID %q should start with 'claude-'", model.ID)
		}
		if model.Object != "model" {
			t.Errorf("Model.Object = %q, want %q", model.Object, "model")
		}
		if model.OwnedBy != "anthropic" {
			t.Errorf("Model.OwnedBy = %q, want %q", model.OwnedBy, "anthropic")
		}
		if model.Created == 0 {
			t.Error("Model.Created should not be zero")
		}
	}

	// Verify expected models are present
	expectedModels := map[string]bool{
		"claude-sonnet-4-5-20250929": false,
		"claude-opus-4-5-20251101":   false,
		"claude-3-haiku-20240307":    false,
	}

	for _, model := range resp.Data {
		if _, ok := expectedModels[model.ID]; ok {
			expectedModels[model.ID] = true
		}
	}

	for model, found := range expectedModels {
		if !found {
			t.Errorf("Expected model %q not found in response", model)
		}
	}

	// Verify created timestamps are parsed correctly
	for _, model := range resp.Data {
		if model.ID == "claude-sonnet-4-5-20250929" {
			// 2025-09-29T00:00:00Z in Unix
			expected := int64(1759104000)
			if model.Created != expected {
				t.Errorf("Created for claude-sonnet-4-5-20250929 = %d, want %d", model.Created, expected)
			}
		}
	}
}

func TestListModels_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type": "error", "error": {"type": "authentication_error", "message": "Invalid API key"}}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("invalid-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	_, err := provider.ListModels(context.Background())
	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestParseCreatedAt(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantTime int64
	}{
		{
			name:     "valid RFC3339 timestamp",
			input:    "2025-09-29T00:00:00Z",
			wantTime: 1759104000,
		},
		{
			name:     "valid RFC3339 timestamp with different time",
			input:    "2024-03-07T12:30:00Z",
			wantTime: 1709814600,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseCreatedAt(tt.input)
			if got != tt.wantTime {
				t.Errorf("parseCreatedAt(%q) = %d, want %d", tt.input, got, tt.wantTime)
			}
		})
	}
}

func TestParseCreatedAt_InvalidFormat(t *testing.T) {
	// For invalid format, it should return current time (non-zero)
	got := parseCreatedAt("invalid-date")
	if got == 0 {
		t.Error("parseCreatedAt with invalid format should return non-zero (current time)")
	}
}

func TestChatCompletionWithContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a slow response
		<-r.Context().Done()
		w.WriteHeader(http.StatusRequestTimeout)
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	req := &core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	_, err := provider.ChatCompletion(ctx, req)
	if err == nil {
		t.Error("expected error when context is cancelled, got nil")
	}
}

func TestConvertToAnthropicRequest(t *testing.T) {
	temp := 0.7
	maxTokens := 1024

	tests := []struct {
		name    string
		input   *core.ChatRequest
		checkFn func(*testing.T, *anthropicRequest)
	}{
		{
			name: "basic request",
			input: &core.ChatRequest{
				Model: "claude-sonnet-4-5-20250929",
				Messages: []core.Message{
					{Role: "user", Content: "Hello"},
				},
			},
			checkFn: func(t *testing.T, req *anthropicRequest) {
				if req.Model != "claude-sonnet-4-5-20250929" {
					t.Errorf("Model = %q, want %q", req.Model, "claude-sonnet-4-5-20250929")
				}
				if len(req.Messages) != 1 {
					t.Errorf("len(Messages) = %d, want 1", len(req.Messages))
				}
				if req.Messages[0].Content != "Hello" {
					t.Errorf("Message content = %q, want %q", req.Messages[0].Content, "Hello")
				}
				if req.MaxTokens != 4096 {
					t.Errorf("MaxTokens = %d, want 4096", req.MaxTokens)
				}
			},
		},
		{
			name: "request with system message",
			input: &core.ChatRequest{
				Model: "claude-opus-4-5-20251101",
				Messages: []core.Message{
					{Role: "system", Content: "You are a helpful assistant"},
					{Role: "user", Content: "Hello"},
				},
			},
			checkFn: func(t *testing.T, req *anthropicRequest) {
				if req.System != "You are a helpful assistant" {
					t.Errorf("System = %q, want %q", req.System, "You are a helpful assistant")
				}
				if len(req.Messages) != 1 {
					t.Errorf("len(Messages) = %d, want 1 (system should be extracted)", len(req.Messages))
				}
			},
		},
		{
			name: "request with parameters",
			input: &core.ChatRequest{
				Model:       "claude-sonnet-4-5-20250929",
				Temperature: &temp,
				MaxTokens:   &maxTokens,
				Messages: []core.Message{
					{Role: "user", Content: "Hello"},
				},
			},
			checkFn: func(t *testing.T, req *anthropicRequest) {
				if req.Temperature == nil || *req.Temperature != 0.7 {
					t.Errorf("Temperature = %v, want 0.7", req.Temperature)
				}
				if req.MaxTokens != 1024 {
					t.Errorf("MaxTokens = %d, want 1024", req.MaxTokens)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := convertToAnthropicRequest(tt.input)
			if err != nil {
				t.Fatalf("convertToAnthropicRequest() error = %v", err)
			}
			tt.checkFn(t, result)
		})
	}
}

func TestConvertFromAnthropicResponse(t *testing.T) {
	resp := &anthropicResponse{
		ID:    "msg_123",
		Type:  "message",
		Role:  "assistant",
		Model: "claude-sonnet-4-5-20250929",
		Content: []anthropicContent{
			{Type: "text", Text: "Hello! How can I help you today?"},
		},
		StopReason: "end_turn",
		Usage: anthropicUsage{
			InputTokens:  10,
			OutputTokens: 20,
		},
	}

	result := convertFromAnthropicResponse(resp)

	if result.ID != "msg_123" {
		t.Errorf("ID = %q, want %q", result.ID, "msg_123")
	}
	if result.Object != "chat.completion" {
		t.Errorf("Object = %q, want %q", result.Object, "chat.completion")
	}
	if result.Model != "claude-sonnet-4-5-20250929" {
		t.Errorf("Model = %q, want %q", result.Model, "claude-sonnet-4-5-20250929")
	}
	if len(result.Choices) != 1 {
		t.Fatalf("len(Choices) = %d, want 1", len(result.Choices))
	}
	if result.Choices[0].Message.Content != "Hello! How can I help you today?" {
		t.Errorf("Message content = %q, want %q", result.Choices[0].Message.Content, "Hello! How can I help you today?")
	}
	if result.Choices[0].Message.Role != "assistant" {
		t.Errorf("Message role = %q, want %q", result.Choices[0].Message.Role, "assistant")
	}
	if result.Choices[0].FinishReason != "end_turn" {
		t.Errorf("FinishReason = %q, want %q", result.Choices[0].FinishReason, "end_turn")
	}
	if result.Usage.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d, want 10", result.Usage.PromptTokens)
	}
	if result.Usage.CompletionTokens != 20 {
		t.Errorf("CompletionTokens = %d, want 20", result.Usage.CompletionTokens)
	}
	if result.Usage.TotalTokens != 30 {
		t.Errorf("TotalTokens = %d, want 30", result.Usage.TotalTokens)
	}
}

func TestConvertFromAnthropicResponse_WithCacheFields(t *testing.T) {
	resp := &anthropicResponse{
		ID:    "msg_cache",
		Type:  "message",
		Role:  "assistant",
		Model: "claude-sonnet-4-5-20250929",
		Content: []anthropicContent{
			{Type: "text", Text: "Hello!"},
		},
		StopReason: "end_turn",
		Usage: anthropicUsage{
			InputTokens:              100,
			OutputTokens:             20,
			CacheCreationInputTokens: 50,
			CacheReadInputTokens:     30,
		},
	}

	result := convertFromAnthropicResponse(resp)

	if result.Usage.RawUsage == nil {
		t.Fatal("expected RawUsage to be set")
	}
	if result.Usage.RawUsage["cache_creation_input_tokens"] != 50 {
		t.Errorf("RawUsage[cache_creation_input_tokens] = %v, want 50", result.Usage.RawUsage["cache_creation_input_tokens"])
	}
	if result.Usage.RawUsage["cache_read_input_tokens"] != 30 {
		t.Errorf("RawUsage[cache_read_input_tokens] = %v, want 30", result.Usage.RawUsage["cache_read_input_tokens"])
	}
}

func TestConvertFromAnthropicResponse_NoCacheFields(t *testing.T) {
	resp := &anthropicResponse{
		ID:    "msg_nocache",
		Type:  "message",
		Role:  "assistant",
		Model: "claude-sonnet-4-5-20250929",
		Content: []anthropicContent{
			{Type: "text", Text: "Hello!"},
		},
		StopReason: "end_turn",
		Usage: anthropicUsage{
			InputTokens:  100,
			OutputTokens: 20,
		},
	}

	result := convertFromAnthropicResponse(resp)

	if result.Usage.RawUsage != nil {
		t.Errorf("expected RawUsage to be nil when no cache fields, got %v", result.Usage.RawUsage)
	}
}

func TestConvertAnthropicResponseToResponses_WithCacheFields(t *testing.T) {
	resp := &anthropicResponse{
		ID:    "msg_cache_resp",
		Type:  "message",
		Role:  "assistant",
		Model: "claude-sonnet-4-5-20250929",
		Content: []anthropicContent{
			{Type: "text", Text: "Hello!"},
		},
		StopReason: "end_turn",
		Usage: anthropicUsage{
			InputTokens:              100,
			OutputTokens:             20,
			CacheCreationInputTokens: 40,
			CacheReadInputTokens:     60,
		},
	}

	result := convertAnthropicResponseToResponses(resp, "claude-sonnet-4-5-20250929")

	if result.Usage == nil {
		t.Fatal("Usage should not be nil")
	}
	if result.Usage.RawUsage == nil {
		t.Fatal("expected RawUsage to be set")
	}
	if result.Usage.RawUsage["cache_creation_input_tokens"] != 40 {
		t.Errorf("RawUsage[cache_creation_input_tokens] = %v, want 40", result.Usage.RawUsage["cache_creation_input_tokens"])
	}
	if result.Usage.RawUsage["cache_read_input_tokens"] != 60 {
		t.Errorf("RawUsage[cache_read_input_tokens] = %v, want 60", result.Usage.RawUsage["cache_read_input_tokens"])
	}
}

func TestConvertFromAnthropicResponse_WithThinkingBlocks(t *testing.T) {
	tests := []struct {
		name         string
		content      []anthropicContent
		expectedText string
	}{
		{
			name: "thinking then text",
			content: []anthropicContent{
				{Type: "thinking", Text: "Let me think about this..."},
				{Type: "text", Text: "The capital of France is Paris."},
			},
			expectedText: "The capital of France is Paris.",
		},
		{
			name: "preamble text then thinking then answer",
			content: []anthropicContent{
				{Type: "text", Text: "\n\n"},
				{Type: "thinking", Text: ""},
				{Type: "text", Text: "The capital of France is Paris."},
			},
			expectedText: "The capital of France is Paris.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &anthropicResponse{
				ID:         "msg_456",
				Type:       "message",
				Role:       "assistant",
				Model:      "claude-opus-4-6",
				Content:    tt.content,
				StopReason: "end_turn",
				Usage:      anthropicUsage{InputTokens: 15, OutputTokens: 40},
			}

			result := convertFromAnthropicResponse(resp)

			if len(result.Choices) == 0 {
				t.Fatalf("expected at least 1 choice, got 0")
			}
			if result.Choices[0].Message.Content != tt.expectedText {
				t.Errorf("expected %q, got %q", tt.expectedText, result.Choices[0].Message.Content)
			}
			if result.Usage.CompletionTokens != 40 {
				t.Errorf("CompletionTokens = %d, want 40", result.Usage.CompletionTokens)
			}
		})
	}
}

func TestExtractTextContent(t *testing.T) {
	tests := []struct {
		name     string
		blocks   []anthropicContent
		expected string
	}{
		{
			name:     "single text block",
			blocks:   []anthropicContent{{Type: "text", Text: "hello"}},
			expected: "hello",
		},
		{
			name: "thinking then text",
			blocks: []anthropicContent{
				{Type: "thinking", Text: "reasoning..."},
				{Type: "text", Text: "answer"},
			},
			expected: "answer",
		},
		{
			name: "multiple thinking blocks then text",
			blocks: []anthropicContent{
				{Type: "thinking", Text: "step 1"},
				{Type: "thinking", Text: "step 2"},
				{Type: "text", Text: "final answer"},
			},
			expected: "final answer",
		},
		{
			name: "preamble text then thinking then answer text",
			blocks: []anthropicContent{
				{Type: "text", Text: "\n\n"},
				{Type: "thinking", Text: ""},
				{Type: "text", Text: "The capital of France is **Paris**."},
			},
			expected: "The capital of France is **Paris**.",
		},
		{
			name: "preamble text then thinking then answer - picks last text",
			blocks: []anthropicContent{
				{Type: "text", Text: "preamble"},
				{Type: "thinking", Text: "let me think..."},
				{Type: "text", Text: "real answer"},
			},
			expected: "real answer",
		},
		{
			name:     "empty blocks",
			blocks:   []anthropicContent{},
			expected: "",
		},
		{
			name:     "nil blocks",
			blocks:   nil,
			expected: "",
		},
		{
			name:     "only thinking blocks - returns empty",
			blocks:   []anthropicContent{{Type: "thinking", Text: "some reasoning"}},
			expected: "",
		},
		{
			name:     "only thinking blocks with empty text - returns empty",
			blocks:   []anthropicContent{{Type: "thinking", Text: ""}},
			expected: "",
		},
		{
			name:     "no type field - returns empty",
			blocks:   []anthropicContent{{Text: "legacy response"}},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractTextContent(tt.blocks)
			if result != tt.expected {
				t.Errorf("extractTextContent() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestResponses(t *testing.T) {
	tests := []struct {
		name          string
		statusCode    int
		responseBody  string
		expectedError bool
		checkResponse func(*testing.T, *core.ResponsesResponse)
	}{
		{
			name:       "successful request with string input",
			statusCode: http.StatusOK,
			responseBody: `{
				"id": "msg_123",
				"type": "message",
				"role": "assistant",
				"model": "claude-sonnet-4-5-20250929",
				"content": [{
					"type": "text",
					"text": "Hello! How can I help you today?"
				}],
				"stop_reason": "end_turn",
				"usage": {
					"input_tokens": 10,
					"output_tokens": 20
				}
			}`,
			expectedError: false,
			checkResponse: func(t *testing.T, resp *core.ResponsesResponse) {
				if resp.ID != "msg_123" {
					t.Errorf("ID = %q, want %q", resp.ID, "msg_123")
				}
				if resp.Object != "response" {
					t.Errorf("Object = %q, want %q", resp.Object, "response")
				}
				if resp.Model != "claude-sonnet-4-5-20250929" {
					t.Errorf("Model = %q, want %q", resp.Model, "claude-sonnet-4-5-20250929")
				}
				if resp.Status != "completed" {
					t.Errorf("Status = %q, want %q", resp.Status, "completed")
				}
				if len(resp.Output) != 1 {
					t.Fatalf("len(Output) = %d, want 1", len(resp.Output))
				}
				if len(resp.Output[0].Content) != 1 {
					t.Fatalf("len(Output[0].Content) = %d, want 1", len(resp.Output[0].Content))
				}
				if resp.Output[0].Content[0].Text != "Hello! How can I help you today?" {
					t.Errorf("Output text = %q, want %q", resp.Output[0].Content[0].Text, "Hello! How can I help you today?")
				}
				if resp.Usage == nil {
					t.Fatal("Usage should not be nil")
				}
				if resp.Usage.InputTokens != 10 {
					t.Errorf("InputTokens = %d, want 10", resp.Usage.InputTokens)
				}
				if resp.Usage.OutputTokens != 20 {
					t.Errorf("OutputTokens = %d, want 20", resp.Usage.OutputTokens)
				}
				if resp.Usage.TotalTokens != 30 {
					t.Errorf("TotalTokens = %d, want 30", resp.Usage.TotalTokens)
				}
			},
		},
		{
			name:          "API error - unauthorized",
			statusCode:    http.StatusUnauthorized,
			responseBody:  `{"type": "error", "error": {"type": "authentication_error", "message": "Invalid API key"}}`,
			expectedError: true,
		},
		{
			name:          "rate limit error",
			statusCode:    http.StatusTooManyRequests,
			responseBody:  `{"type": "error", "error": {"type": "rate_limit_error", "message": "Rate limit exceeded"}}`,
			expectedError: true,
		},
		{
			name:          "server error",
			statusCode:    http.StatusInternalServerError,
			responseBody:  `{"type": "error", "error": {"type": "api_error", "message": "Internal server error"}}`,
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify request headers
				if r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("Content-Type = %q, want %q", r.Header.Get("Content-Type"), "application/json")
				}
				apiKey := r.Header.Get("x-api-key")
				if apiKey == "" {
					t.Error("x-api-key header should not be empty")
				}
				if r.Header.Get("anthropic-version") != anthropicAPIVersion {
					t.Errorf("anthropic-version = %q, want %q", r.Header.Get("anthropic-version"), anthropicAPIVersion)
				}

				// Verify request path (Anthropic uses /messages)
				if r.URL.Path != "/messages" {
					t.Errorf("Path = %q, want %q", r.URL.Path, "/messages")
				}

				// Verify request body is converted to Anthropic format
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("failed to read request body: %v", err)
				}
				var req anthropicRequest
				if err := json.Unmarshal(body, &req); err != nil {
					t.Fatalf("failed to unmarshal request: %v", err)
				}

				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			req := &core.ResponsesRequest{
				Model: "claude-sonnet-4-5-20250929",
				Input: "Hello",
			}

			resp, err := provider.Responses(context.Background(), req)

			if tt.expectedError {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tt.checkResponse != nil {
					tt.checkResponse(t, resp)
				}
			}
		})
	}
}

func TestResponsesWithArrayInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request body is converted to Anthropic format
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		var req anthropicRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}

		// Verify messages are properly converted
		if len(req.Messages) != 2 {
			t.Errorf("len(Messages) = %d, want 2", len(req.Messages))
		}
		if req.Messages[0].Role != "user" {
			t.Errorf("Messages[0].Role = %q, want %q", req.Messages[0].Role, "user")
		}
		if req.Messages[0].Content != "Hello" {
			t.Errorf("Messages[0].Content = %q, want %q", req.Messages[0].Content, "Hello")
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "msg_123",
			"type": "message",
			"role": "assistant",
			"model": "claude-sonnet-4-5-20250929",
			"content": [{
				"type": "text",
				"text": "Hello!"
			}],
			"stop_reason": "end_turn",
			"usage": {
				"input_tokens": 10,
				"output_tokens": 5
			}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	req := &core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
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
		Instructions: "Be helpful",
	}

	resp, err := provider.Responses(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID != "msg_123" {
		t.Errorf("ID = %q, want %q", resp.ID, "msg_123")
	}
}

func TestResponsesWithInstructions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		var req anthropicRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}

		// Verify system instruction is set
		if req.System != "You are a helpful assistant" {
			t.Errorf("System = %q, want %q", req.System, "You are a helpful assistant")
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "msg_123",
			"type": "message",
			"role": "assistant",
			"model": "claude-sonnet-4-5-20250929",
			"content": [{
				"type": "text",
				"text": "Hello!"
			}],
			"stop_reason": "end_turn",
			"usage": {
				"input_tokens": 10,
				"output_tokens": 5
			}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	req := &core.ResponsesRequest{
		Model:        "claude-sonnet-4-5-20250929",
		Input:        "Hello",
		Instructions: "You are a helpful assistant",
	}

	_, err := provider.Responses(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStreamResponses(t *testing.T) {
	tests := []struct {
		name          string
		statusCode    int
		responseBody  string
		expectedError bool
		checkStream   func(*testing.T, io.ReadCloser)
	}{
		{
			name:       "successful streaming request",
			statusCode: http.StatusOK,
			responseBody: `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"!"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}
`,
			expectedError: false,
			checkStream: func(t *testing.T, body io.ReadCloser) {
				if body == nil {
					t.Fatal("body should not be nil")
				}
				defer func() { _ = body.Close() }()

				// Read and verify the streaming response
				respBody, err := io.ReadAll(body)
				if err != nil {
					t.Fatalf("failed to read response body: %v", err)
				}

				// The response should be converted to Responses API format
				responseStr := string(respBody)
				if !strings.Contains(responseStr, "response.created") {
					t.Error("response should contain response.created event")
				}
				if !strings.Contains(responseStr, "response.output_text.delta") {
					t.Error("response should contain response.output_text.delta event")
				}
				if !strings.Contains(responseStr, "[DONE]") {
					t.Error("response should end with [DONE]")
				}
			},
		},
		{
			name:          "API error - unauthorized",
			statusCode:    http.StatusUnauthorized,
			responseBody:  `{"type": "error", "error": {"type": "authentication_error", "message": "Invalid API key"}}`,
			expectedError: true,
		},
		{
			name:          "rate limit error",
			statusCode:    http.StatusTooManyRequests,
			responseBody:  `{"type": "error", "error": {"type": "rate_limit_error", "message": "Rate limit exceeded"}}`,
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify request headers
				if r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("Content-Type = %q, want %q", r.Header.Get("Content-Type"), "application/json")
				}
				apiKey := r.Header.Get("x-api-key")
				if apiKey == "" {
					t.Error("x-api-key header should not be empty")
				}

				// Verify stream is set in request body
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("failed to read request body: %v", err)
				}
				var req anthropicRequest
				if err := json.Unmarshal(body, &req); err != nil {
					t.Fatalf("failed to unmarshal request: %v", err)
				}
				if !req.Stream {
					t.Error("Stream should be true in request")
				}

				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			req := &core.ResponsesRequest{
				Model: "claude-sonnet-4-5-20250929",
				Input: "Hello",
			}

			body, err := provider.StreamResponses(context.Background(), req)

			if tt.expectedError {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tt.checkStream != nil {
					tt.checkStream(t, body)
				}
			}
		})
	}
}

func TestResponsesWithContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a slow response
		<-r.Context().Done()
		w.WriteHeader(http.StatusRequestTimeout)
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	req := &core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: "Hello",
	}

	_, err := provider.Responses(ctx, req)
	if err == nil {
		t.Error("expected error when context is cancelled, got nil")
	}
}

func TestConvertResponsesRequestToAnthropic(t *testing.T) {
	temp := 0.7
	maxTokens := 1024

	tests := []struct {
		name    string
		input   *core.ResponsesRequest
		checkFn func(*testing.T, *anthropicRequest)
	}{
		{
			name: "string input",
			input: &core.ResponsesRequest{
				Model: "claude-sonnet-4-5-20250929",
				Input: "Hello",
			},
			checkFn: func(t *testing.T, req *anthropicRequest) {
				if req.Model != "claude-sonnet-4-5-20250929" {
					t.Errorf("Model = %q, want %q", req.Model, "claude-sonnet-4-5-20250929")
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
				Model:        "claude-sonnet-4-5-20250929",
				Input:        "Hello",
				Instructions: "Be helpful",
			},
			checkFn: func(t *testing.T, req *anthropicRequest) {
				if req.System != "Be helpful" {
					t.Errorf("System = %q, want %q", req.System, "Be helpful")
				}
			},
		},
		{
			name: "with parameters",
			input: &core.ResponsesRequest{
				Model:           "claude-sonnet-4-5-20250929",
				Input:           "Hello",
				Temperature:     &temp,
				MaxOutputTokens: &maxTokens,
			},
			checkFn: func(t *testing.T, req *anthropicRequest) {
				if req.Temperature == nil || *req.Temperature != 0.7 {
					t.Errorf("Temperature = %v, want 0.7", req.Temperature)
				}
				if req.MaxTokens != 1024 {
					t.Errorf("MaxTokens = %d, want 1024", req.MaxTokens)
				}
			},
		},
		{
			name: "array input with content parts",
			input: &core.ResponsesRequest{
				Model: "claude-sonnet-4-5-20250929",
				Input: []interface{}{
					map[string]interface{}{
						"role": "user",
						"content": []interface{}{
							map[string]interface{}{
								"type": "text",
								"text": "Hello",
							},
							map[string]interface{}{
								"type": "text",
								"text": "World",
							},
						},
					},
				},
			},
			checkFn: func(t *testing.T, req *anthropicRequest) {
				if len(req.Messages) != 1 {
					t.Fatalf("len(Messages) = %d, want 1", len(req.Messages))
				}
				if req.Messages[0].Content != "Hello World" {
					t.Errorf("Messages[0].Content = %q, want %q", req.Messages[0].Content, "Hello World")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := convertResponsesRequestToAnthropic(tt.input)
			if err != nil {
				t.Fatalf("convertResponsesRequestToAnthropic() error = %v", err)
			}
			tt.checkFn(t, result)
		})
	}
}

func TestConvertAnthropicResponseToResponses(t *testing.T) {
	resp := &anthropicResponse{
		ID:    "msg_123",
		Type:  "message",
		Role:  "assistant",
		Model: "claude-sonnet-4-5-20250929",
		Content: []anthropicContent{
			{Type: "text", Text: "Hello! How can I help you today?"},
		},
		StopReason: "end_turn",
		Usage: anthropicUsage{
			InputTokens:  10,
			OutputTokens: 20,
		},
	}

	result := convertAnthropicResponseToResponses(resp, "claude-sonnet-4-5-20250929")

	if result.ID != "msg_123" {
		t.Errorf("ID = %q, want %q", result.ID, "msg_123")
	}
	if result.Object != "response" {
		t.Errorf("Object = %q, want %q", result.Object, "response")
	}
	if result.Model != "claude-sonnet-4-5-20250929" {
		t.Errorf("Model = %q, want %q", result.Model, "claude-sonnet-4-5-20250929")
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
	if len(result.Output[0].Content) != 1 {
		t.Fatalf("len(Output[0].Content) = %d, want 1", len(result.Output[0].Content))
	}
	if result.Output[0].Content[0].Text != "Hello! How can I help you today?" {
		t.Errorf("Content text = %q, want %q", result.Output[0].Content[0].Text, "Hello! How can I help you today?")
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

func TestConvertAnthropicResponseToResponses_WithThinkingBlocks(t *testing.T) {
	tests := []struct {
		name         string
		content      []anthropicContent
		expectedText string
	}{
		{
			name: "thinking then text",
			content: []anthropicContent{
				{Type: "thinking", Text: "The user is asking about geography..."},
				{Type: "text", Text: "The capital of France is Paris."},
			},
			expectedText: "The capital of France is Paris.",
		},
		{
			name: "preamble text then thinking then answer",
			content: []anthropicContent{
				{Type: "text", Text: "\n\n"},
				{Type: "thinking", Text: ""},
				{Type: "text", Text: "The capital of France is Paris."},
			},
			expectedText: "The capital of France is Paris.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &anthropicResponse{
				ID:         "msg_789",
				Type:       "message",
				Role:       "assistant",
				Model:      "claude-opus-4-6",
				Content:    tt.content,
				StopReason: "end_turn",
				Usage:      anthropicUsage{InputTokens: 20, OutputTokens: 50},
			}

			result := convertAnthropicResponseToResponses(resp, "claude-opus-4-6")

			if len(result.Output) != 1 {
				t.Fatalf("len(Output) = %d, want 1", len(result.Output))
			}
			if len(result.Output[0].Content) == 0 {
				t.Fatalf("len(Output[0].Content) = 0, want at least 1")
			}
			if result.Output[0].Content[0].Text != tt.expectedText {
				t.Errorf("expected %q, got %q", tt.expectedText, result.Output[0].Content[0].Text)
			}
			if result.Usage.OutputTokens != 50 {
				t.Errorf("OutputTokens = %d, want 50", result.Usage.OutputTokens)
			}
		})
	}
}

func TestConvertToAnthropicRequest_ReasoningEffort(t *testing.T) {
	tests := []struct {
		name              string
		model             string
		reasoning         *core.Reasoning
		maxTokens         *int
		setTemperature    bool
		setTemperatureOne bool
		expectedThinkType string
		expectedBudget    int
		expectedEffort    string
		expectedMaxTokens int
		expectNilTemp     bool
		expectedTemp      *float64
	}{
		{
			name:              "reasoning nil - no thinking",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         nil,
			maxTokens:         intPtr(1000),
			expectedMaxTokens: 1000,
		},
		{
			name:              "empty effort - no thinking",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: ""},
			maxTokens:         intPtr(1000),
			expectedMaxTokens: 1000,
		},
		{
			name:              "legacy model - low effort",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: "low"},
			maxTokens:         intPtr(10000),
			expectedThinkType: "enabled",
			expectedBudget:    5000,
			expectedMaxTokens: 10000,
			expectNilTemp:     true,
		},
		{
			name:              "legacy model - medium effort",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: "medium"},
			maxTokens:         intPtr(15000),
			expectedThinkType: "enabled",
			expectedBudget:    10000,
			expectedMaxTokens: 15000,
			expectNilTemp:     true,
		},
		{
			name:              "legacy model - high effort",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: "high"},
			maxTokens:         intPtr(25000),
			expectedThinkType: "enabled",
			expectedBudget:    20000,
			expectedMaxTokens: 25000,
			expectNilTemp:     true,
		},
		{
			name:              "legacy model - invalid effort defaults to low",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: "invalid"},
			maxTokens:         intPtr(10000),
			expectedThinkType: "enabled",
			expectedBudget:    5000,
			expectedMaxTokens: 10000,
			expectNilTemp:     true,
		},
		{
			name:              "legacy model - bumps max_tokens when too low",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: "high"},
			maxTokens:         intPtr(1000),
			expectedThinkType: "enabled",
			expectedBudget:    20000,
			expectedMaxTokens: 21024,
			expectNilTemp:     true,
		},
		{
			name:              "legacy model - removes temperature",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: "medium"},
			maxTokens:         intPtr(15000),
			setTemperature:    true,
			expectedThinkType: "enabled",
			expectedBudget:    10000,
			expectedMaxTokens: 15000,
			expectNilTemp:     true,
		},
		{
			name:              "legacy model - preserves temperature=1.0 with reasoning",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: "medium"},
			maxTokens:         intPtr(15000),
			setTemperatureOne: true,
			expectedThinkType: "enabled",
			expectedBudget:    10000,
			expectedMaxTokens: 15000,
			expectNilTemp:     false,
			expectedTemp:      float64Ptr(1.0),
		},
		{
			name:              "4.6 model - adaptive thinking with high effort",
			model:             "claude-opus-4-6",
			reasoning:         &core.Reasoning{Effort: "high"},
			maxTokens:         intPtr(4096),
			expectedThinkType: "adaptive",
			expectedEffort:    "high",
			expectedMaxTokens: 4096,
			expectNilTemp:     true,
		},
		{
			name:              "4.6 model - adaptive thinking with low effort",
			model:             "claude-sonnet-4-6-20260301",
			reasoning:         &core.Reasoning{Effort: "low"},
			maxTokens:         intPtr(4096),
			expectedThinkType: "adaptive",
			expectedEffort:    "low",
			expectedMaxTokens: 4096,
			expectNilTemp:     true,
		},
		{
			name:              "4.6 model - does not bump max_tokens",
			model:             "claude-opus-4-6",
			reasoning:         &core.Reasoning{Effort: "high"},
			maxTokens:         intPtr(1000),
			expectedThinkType: "adaptive",
			expectedEffort:    "high",
			expectedMaxTokens: 1000,
			expectNilTemp:     true,
		},
		{
			name:              "4.6 model - removes temperature",
			model:             "claude-opus-4-6",
			reasoning:         &core.Reasoning{Effort: "medium"},
			maxTokens:         intPtr(4096),
			setTemperature:    true,
			expectedThinkType: "adaptive",
			expectedEffort:    "medium",
			expectedMaxTokens: 4096,
			expectNilTemp:     true,
		},
		{
			name:              "4.6 model - invalid effort normalizes to low",
			model:             "claude-opus-4-6",
			reasoning:         &core.Reasoning{Effort: "extreme"},
			maxTokens:         intPtr(4096),
			expectedThinkType: "adaptive",
			expectedEffort:    "low",
			expectedMaxTokens: 4096,
			expectNilTemp:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &core.ChatRequest{
				Model:     tt.model,
				Messages:  []core.Message{{Role: "user", Content: "test"}},
				MaxTokens: tt.maxTokens,
				Reasoning: tt.reasoning,
			}
			if tt.setTemperatureOne {
				temp := 1.0
				req.Temperature = &temp
			} else if tt.setTemperature {
				temp := 0.7
				req.Temperature = &temp
			}

			result, err := convertToAnthropicRequest(req)
			if err != nil {
				t.Fatalf("convertToAnthropicRequest() error = %v", err)
			}

			if tt.expectedThinkType == "" {
				if result.Thinking != nil {
					t.Errorf("Thinking should be nil but got %+v", result.Thinking)
				}
				if result.OutputConfig != nil {
					t.Errorf("OutputConfig should be nil but got %+v", result.OutputConfig)
				}
			} else {
				if result.Thinking == nil {
					t.Fatal("Thinking should not be nil")
				}
				if result.Thinking.Type != tt.expectedThinkType {
					t.Errorf("Thinking.Type = %q, want %q", result.Thinking.Type, tt.expectedThinkType)
				}
				if tt.expectedThinkType == "enabled" {
					if result.Thinking.BudgetTokens != tt.expectedBudget {
						t.Errorf("BudgetTokens = %d, want %d", result.Thinking.BudgetTokens, tt.expectedBudget)
					}
				}
				if tt.expectedThinkType == "adaptive" {
					if result.OutputConfig == nil {
						t.Fatal("OutputConfig should not be nil for adaptive thinking")
					}
					if result.OutputConfig.Effort != tt.expectedEffort {
						t.Errorf("OutputConfig.Effort = %q, want %q", result.OutputConfig.Effort, tt.expectedEffort)
					}
				}
			}

			if result.MaxTokens != tt.expectedMaxTokens {
				t.Errorf("MaxTokens = %d, want %d", result.MaxTokens, tt.expectedMaxTokens)
			}

			if tt.expectNilTemp && result.Temperature != nil {
				t.Errorf("Temperature should be nil but is %v", *result.Temperature)
			}
			if tt.expectedTemp != nil {
				if result.Temperature == nil {
					t.Errorf("Temperature should be %v but is nil", *tt.expectedTemp)
				} else if *result.Temperature != *tt.expectedTemp {
					t.Errorf("Temperature = %v, want %v", *result.Temperature, *tt.expectedTemp)
				}
			}
		})
	}
}

func TestConvertResponsesRequestToAnthropic_ReasoningEffort(t *testing.T) {
	tests := []struct {
		name              string
		model             string
		reasoning         *core.Reasoning
		maxOutputTokens   *int
		setTemperature    bool
		expectedThinkType string
		expectedBudget    int
		expectedEffort    string
		expectedMaxTokens int
		expectNilTemp     bool
	}{
		{
			name:              "no reasoning",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         nil,
			maxOutputTokens:   intPtr(1000),
			expectedMaxTokens: 1000,
		},
		{
			name:              "empty effort",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: ""},
			maxOutputTokens:   intPtr(1000),
			expectedMaxTokens: 1000,
		},
		{
			name:              "legacy model - low effort bumps max tokens",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: "low"},
			maxOutputTokens:   intPtr(1000),
			expectedThinkType: "enabled",
			expectedBudget:    5000,
			expectedMaxTokens: 6024,
			expectNilTemp:     true,
		},
		{
			name:              "legacy model - high effort with sufficient tokens",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: "high"},
			maxOutputTokens:   intPtr(25000),
			expectedThinkType: "enabled",
			expectedBudget:    20000,
			expectedMaxTokens: 25000,
			expectNilTemp:     true,
		},
		{
			name:              "legacy model - removes temperature",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: "medium"},
			maxOutputTokens:   intPtr(15000),
			setTemperature:    true,
			expectedThinkType: "enabled",
			expectedBudget:    10000,
			expectedMaxTokens: 15000,
			expectNilTemp:     true,
		},
		{
			name:              "4.6 model - adaptive thinking",
			model:             "claude-opus-4-6",
			reasoning:         &core.Reasoning{Effort: "high"},
			maxOutputTokens:   intPtr(4096),
			expectedThinkType: "adaptive",
			expectedEffort:    "high",
			expectedMaxTokens: 4096,
			expectNilTemp:     true,
		},
		{
			name:              "4.6 model - does not bump max_tokens",
			model:             "claude-opus-4-6",
			reasoning:         &core.Reasoning{Effort: "high"},
			maxOutputTokens:   intPtr(1000),
			expectedThinkType: "adaptive",
			expectedEffort:    "high",
			expectedMaxTokens: 1000,
			expectNilTemp:     true,
		},
		{
			name:              "4.6 model - removes temperature",
			model:             "claude-opus-4-6",
			reasoning:         &core.Reasoning{Effort: "medium"},
			maxOutputTokens:   intPtr(4096),
			setTemperature:    true,
			expectedThinkType: "adaptive",
			expectedEffort:    "medium",
			expectedMaxTokens: 4096,
			expectNilTemp:     true,
		},
		{
			name:              "4.6 model - invalid effort normalizes to low",
			model:             "claude-opus-4-6",
			reasoning:         &core.Reasoning{Effort: "extreme"},
			maxOutputTokens:   intPtr(4096),
			expectedThinkType: "adaptive",
			expectedEffort:    "low",
			expectedMaxTokens: 4096,
			expectNilTemp:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &core.ResponsesRequest{
				Model:           tt.model,
				Input:           "test input",
				MaxOutputTokens: tt.maxOutputTokens,
				Reasoning:       tt.reasoning,
			}
			if tt.setTemperature {
				temp := 0.7
				req.Temperature = &temp
			}

			result, err := convertResponsesRequestToAnthropic(req)
			if err != nil {
				t.Fatalf("convertResponsesRequestToAnthropic() error = %v", err)
			}

			if tt.expectedThinkType == "" {
				if result.Thinking != nil {
					t.Errorf("Thinking should be nil but got %+v", result.Thinking)
				}
				if result.OutputConfig != nil {
					t.Errorf("OutputConfig should be nil but got %+v", result.OutputConfig)
				}
			} else {
				if result.Thinking == nil {
					t.Fatal("Thinking should not be nil")
				}
				if result.Thinking.Type != tt.expectedThinkType {
					t.Errorf("Thinking.Type = %q, want %q", result.Thinking.Type, tt.expectedThinkType)
				}
				if tt.expectedThinkType == "enabled" {
					if result.Thinking.BudgetTokens != tt.expectedBudget {
						t.Errorf("BudgetTokens = %d, want %d", result.Thinking.BudgetTokens, tt.expectedBudget)
					}
				}
				if tt.expectedThinkType == "adaptive" {
					if result.OutputConfig == nil {
						t.Fatal("OutputConfig should not be nil for adaptive thinking")
					}
					if result.OutputConfig.Effort != tt.expectedEffort {
						t.Errorf("OutputConfig.Effort = %q, want %q", result.OutputConfig.Effort, tt.expectedEffort)
					}
				}
			}

			if result.MaxTokens != tt.expectedMaxTokens {
				t.Errorf("MaxTokens = %d, want %d", result.MaxTokens, tt.expectedMaxTokens)
			}

			if tt.expectNilTemp && result.Temperature != nil {
				t.Errorf("Temperature should be nil but is %v", *result.Temperature)
			}
		})
	}
}

func TestIsAdaptiveThinkingModel(t *testing.T) {
	tests := []struct {
		model    string
		expected bool
	}{
		{"claude-opus-4-6", true},
		{"claude-opus-4-6-20260301", true},
		{"claude-sonnet-4-6", true},
		{"claude-sonnet-4-6-20260301", true},
		{"claude-haiku-4-6", false},
		{"claude-haiku-4-6-20260501", false},
		{"claude-3-5-sonnet-20241022", false},
		{"claude-opus-4-5-20251101", false},
		{"claude-4-60", false},
		{"claude-opus-4-6x", false},
		{"claude-opus-4-65", false},
		{"something-claude-opus-4-6", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := isAdaptiveThinkingModel(tt.model); got != tt.expected {
				t.Errorf("isAdaptiveThinkingModel(%q) = %v, want %v", tt.model, got, tt.expected)
			}
		})
	}
}

func TestConvertToAnthropicRequest_MultimodalImageContent(t *testing.T) {
	req := &core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{
				Role: "user",
				Content: []core.ContentPart{
					{Type: "text", Text: "Describe the image."},
					{
						Type: "image_url",
						ImageURL: &core.ImageURLContent{
							URL: "data:image/png;base64,ZmFrZQ==",
						},
					},
				},
			},
		},
	}

	result, err := convertToAnthropicRequest(req)
	if err != nil {
		t.Fatalf("convertToAnthropicRequest() error = %v", err)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(result.Messages))
	}

	blocks, ok := result.Messages[0].Content.([]anthropicContentBlock)
	if !ok {
		t.Fatalf("message content type = %T, want []anthropicContentBlock", result.Messages[0].Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2", len(blocks))
	}
	if blocks[0].Type != "text" || blocks[0].Text != "Describe the image." {
		t.Fatalf("unexpected first block: %+v", blocks[0])
	}
	if blocks[1].Type != "image" || blocks[1].Source == nil || blocks[1].Source.MediaType != "image/png" || blocks[1].Source.Data != "ZmFrZQ==" {
		t.Fatalf("unexpected second block: %+v", blocks[1])
	}
}

func TestConvertToAnthropicRequest_RejectsInputAudio(t *testing.T) {
	req := &core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{
				Role: "user",
				Content: []core.ContentPart{
					{
						Type: "input_audio",
						InputAudio: &core.InputAudioContent{
							Data:   "abc",
							Format: "wav",
						},
					},
				},
			},
		},
	}

	_, err := convertToAnthropicRequest(req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "input_audio") {
		t.Fatalf("expected input_audio error, got %v", err)
	}
}

func TestConvertToAnthropicRequest_MultimodalRemoteImageContent(t *testing.T) {
	req := &core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{
				Role: "user",
				Content: []core.ContentPart{
					{
						Type: "image_url",
						ImageURL: &core.ImageURLContent{
							URL:       "https://example.com/image.png",
							MediaType: "image/png",
						},
					},
				},
			},
		},
	}

	result, err := convertToAnthropicRequest(req)
	if err != nil {
		t.Fatalf("convertToAnthropicRequest() error = %v", err)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(result.Messages))
	}

	blocks, ok := result.Messages[0].Content.([]anthropicContentBlock)
	if !ok {
		t.Fatalf("message content type = %T, want []anthropicContentBlock", result.Messages[0].Content)
	}
	if len(blocks) != 1 {
		t.Fatalf("len(blocks) = %d, want 1", len(blocks))
	}
	if blocks[0].Type != "image" || blocks[0].Source == nil {
		t.Fatalf("unexpected image block: %+v", blocks[0])
	}
	if blocks[0].Source.Type != "url" || blocks[0].Source.URL != "https://example.com/image.png" {
		t.Fatalf("unexpected image source: %+v", blocks[0].Source)
	}
	if blocks[0].Source.Data != "" || blocks[0].Source.MediaType != "" {
		t.Fatalf("expected url source without data/media_type, got %+v", blocks[0].Source)
	}
}

func TestConvertToAnthropicRequest_AllowsRemoteImageWithoutMediaType(t *testing.T) {
	req := &core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{
				Role: "user",
				Content: []core.ContentPart{
					{
						Type: "image_url",
						ImageURL: &core.ImageURLContent{
							URL: "https://example.com/image.png",
						},
					},
				},
			},
		},
	}

	result, err := convertToAnthropicRequest(req)
	if err != nil {
		t.Fatalf("convertToAnthropicRequest() error = %v", err)
	}
	blocks, ok := result.Messages[0].Content.([]anthropicContentBlock)
	if !ok || len(blocks) != 1 || blocks[0].Source == nil {
		t.Fatalf("unexpected image block: %#v", result.Messages[0].Content)
	}
	if blocks[0].Source.Type != "url" || blocks[0].Source.URL != "https://example.com/image.png" {
		t.Fatalf("unexpected image source: %+v", blocks[0].Source)
	}
	if blocks[0].Source.MediaType != "" {
		t.Fatalf("expected media_type to be omitted for url source, got %+v", blocks[0].Source)
	}
}

func TestConvertToAnthropicRequest_IgnoresRemoteImageMediaTypeHint(t *testing.T) {
	req := &core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{
				Role: "user",
				Content: []core.ContentPart{
					{
						Type: "image_url",
						ImageURL: &core.ImageURLContent{
							URL:       "https://example.com/image.svg",
							MediaType: "image/svg+xml",
						},
					},
				},
			},
		},
	}

	result, err := convertToAnthropicRequest(req)
	if err != nil {
		t.Fatalf("convertToAnthropicRequest() error = %v", err)
	}
	blocks, ok := result.Messages[0].Content.([]anthropicContentBlock)
	if !ok || len(blocks) != 1 || blocks[0].Source == nil {
		t.Fatalf("unexpected image block: %#v", result.Messages[0].Content)
	}
	if blocks[0].Source.Type != "url" || blocks[0].Source.URL != "https://example.com/image.svg" {
		t.Fatalf("unexpected image source: %+v", blocks[0].Source)
	}
	if blocks[0].Source.MediaType != "" {
		t.Fatalf("expected media_type to be omitted for url source, got %+v", blocks[0].Source)
	}
}

func TestConvertResponsesRequestToAnthropic_RejectsInvalidInputItems(t *testing.T) {
	tests := []struct {
		name  string
		input []interface{}
	}{
		{
			name: "non-object item",
			input: []interface{}{
				"bad-item",
			},
		},
		{
			name: "missing role",
			input: []interface{}{
				map[string]interface{}{
					"content": []interface{}{
						map[string]interface{}{
							"type": "input_text",
							"text": "hello",
						},
					},
				},
			},
		},
		{
			name: "invalid content",
			input: []interface{}{
				map[string]interface{}{
					"role": "user",
					"content": []interface{}{
						map[string]interface{}{
							"type": "unknown",
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := convertResponsesRequestToAnthropic(&core.ResponsesRequest{
				Model: "claude-sonnet-4-5-20250929",
				Input: tt.input,
			})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "invalid responses input item") {
				t.Fatalf("expected invalid responses input item error, got %v", err)
			}
		})
	}
}

func TestConvertResponsesRequestToAnthropic_RejectsUnsupportedInputType(t *testing.T) {
	_, err := convertResponsesRequestToAnthropic(&core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: 123,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid responses input: unsupported type") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEmbeddings_ReturnsUnsupportedError(t *testing.T) {
	p := &Provider{}
	_, err := p.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello",
	})
	if err == nil {
		t.Fatal("expected error from Anthropic Embeddings, got nil")
	}

	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("expected GatewayError, got %T: %v", err, err)
	}
	if gatewayErr.HTTPStatusCode() != 400 {
		t.Errorf("expected HTTP 400, got %d", gatewayErr.HTTPStatusCode())
	}
	if !strings.Contains(err.Error(), "anthropic does not support embeddings") {
		t.Errorf("expected message about anthropic not supporting embeddings, got: %s", err.Error())
	}
}

func intPtr(i int) *int {
	return &i
}

func float64Ptr(f float64) *float64 {
	return &f
}
