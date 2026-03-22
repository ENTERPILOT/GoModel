package groq

import (
	"context"
	"encoding/json"
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
	provider := New("test-api-key", providers.ProviderOptions{})

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
				"id": "chatcmpl-123",
				"object": "chat.completion",
				"created": 1677652288,
				"model": "llama-3.3-70b-versatile",
				"choices": [{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "Hello! How can I help you today?"
					},
					"finish_reason": "stop"
				}],
				"usage": {
					"prompt_tokens": 10,
					"completion_tokens": 20,
					"total_tokens": 30
				}
			}`,
			expectedError: false,
			checkResponse: func(t *testing.T, resp *core.ChatResponse) {
				if resp.ID != "chatcmpl-123" {
					t.Errorf("ID = %q, want %q", resp.ID, "chatcmpl-123")
				}
				if resp.Model != "llama-3.3-70b-versatile" {
					t.Errorf("Model = %q, want %q", resp.Model, "llama-3.3-70b-versatile")
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
			name:          "API error",
			statusCode:    http.StatusUnauthorized,
			responseBody:  `{"error": {"message": "Invalid API key"}}`,
			expectedError: true,
		},
		{
			name:          "rate limit error",
			statusCode:    http.StatusTooManyRequests,
			responseBody:  `{"error": {"message": "Rate limit exceeded"}}`,
			expectedError: true,
		},
		{
			name:          "server error",
			statusCode:    http.StatusInternalServerError,
			responseBody:  `{"error": {"message": "Internal server error"}}`,
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
				authHeader := r.Header.Get("Authorization")
				if !strings.HasPrefix(authHeader, "Bearer ") {
					t.Errorf("Authorization header should start with 'Bearer '")
				}

				// Verify request body
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("failed to read request body: %v", err)
				}
				var req core.ChatRequest
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
				Model: "llama-3.3-70b-versatile",
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
	}{
		{
			name:       "successful streaming request",
			statusCode: http.StatusOK,
			responseBody: `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama-3.3-70b-versatile","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama-3.3-70b-versatile","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":null}]}

data: [DONE]
`,
			expectedError: false,
		},
		{
			name:          "API error",
			statusCode:    http.StatusUnauthorized,
			responseBody:  `{"error": {"message": "Invalid API key"}}`,
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
				authHeader := r.Header.Get("Authorization")
				if !strings.HasPrefix(authHeader, "Bearer ") {
					t.Errorf("Authorization header should start with 'Bearer '")
				}

				// Verify stream is set in request body
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("failed to read request body: %v", err)
				}
				var req core.ChatRequest
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
				Model: "llama-3.3-70b-versatile",
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
				if body == nil {
					t.Fatal("body should not be nil")
				}
				defer func() { _ = body.Close() }()

				// Read and verify the streaming response
				respBody, err := io.ReadAll(body)
				if err != nil {
					t.Fatalf("failed to read response body: %v", err)
				}
				if string(respBody) != tt.responseBody {
					t.Errorf("response body = %q, want %q", string(respBody), tt.responseBody)
				}
			}
		})
	}
}

func TestListModels(t *testing.T) {
	tests := []struct {
		name          string
		statusCode    int
		responseBody  string
		expectedError bool
		checkResponse func(*testing.T, *core.ModelsResponse)
	}{
		{
			name:       "successful request",
			statusCode: http.StatusOK,
			responseBody: `{
				"object": "list",
				"data": [
					{
						"id": "llama-3.3-70b-versatile",
						"object": "model",
						"created": 1687882411,
						"owned_by": "groq"
					},
					{
						"id": "mixtral-8x7b-32768",
						"object": "model",
						"created": 1687882410,
						"owned_by": "groq"
					}
				]
			}`,
			expectedError: false,
			checkResponse: func(t *testing.T, resp *core.ModelsResponse) {
				if resp.Object != "list" {
					t.Errorf("Object = %q, want %q", resp.Object, "list")
				}
				if len(resp.Data) != 2 {
					t.Fatalf("len(Data) = %d, want 2", len(resp.Data))
				}
				if resp.Data[0].ID != "llama-3.3-70b-versatile" {
					t.Errorf("Data[0].ID = %q, want %q", resp.Data[0].ID, "llama-3.3-70b-versatile")
				}
				if resp.Data[0].OwnedBy != "groq" {
					t.Errorf("Data[0].OwnedBy = %q, want %q", resp.Data[0].OwnedBy, "groq")
				}
			},
		},
		{
			name:          "API error",
			statusCode:    http.StatusUnauthorized,
			responseBody:  `{"error": {"message": "Invalid API key"}}`,
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify request method and path
				if r.Method != http.MethodGet {
					t.Errorf("Method = %q, want %q", r.Method, http.MethodGet)
				}
				if r.URL.Path != "/models" {
					t.Errorf("Path = %q, want %q", r.URL.Path, "/models")
				}

				// Verify authorization header
				authHeader := r.Header.Get("Authorization")
				if !strings.HasPrefix(authHeader, "Bearer ") {
					t.Errorf("Authorization header should start with 'Bearer '")
				}

				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			resp, err := provider.ListModels(context.Background())

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
		Model: "llama-3.3-70b-versatile",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	_, err := provider.ChatCompletion(ctx, req)
	if err == nil {
		t.Error("expected error when context is cancelled, got nil")
	}
}

func TestResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request path for chat completions (Groq converts Responses to chat)
		if r.URL.Path != "/chat/completions" {
			t.Errorf("Path = %q, want %q", r.URL.Path, "/chat/completions")
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-123",
			"object": "chat.completion",
			"created": 1677652288,
			"model": "llama-3.3-70b-versatile",
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "Hello! How can I help you today?"
				},
				"finish_reason": "stop"
			}],
			"usage": {
				"prompt_tokens": 10,
				"completion_tokens": 20,
				"total_tokens": 30
			}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	req := &core.ResponsesRequest{
		Model: "llama-3.3-70b-versatile",
		Input: "Hello",
	}

	resp, err := provider.Responses(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID != "chatcmpl-123" {
		t.Errorf("ID = %q, want %q", resp.ID, "chatcmpl-123")
	}
	if resp.Object != "response" {
		t.Errorf("Object = %q, want %q", resp.Object, "response")
	}
	if resp.Model != "llama-3.3-70b-versatile" {
		t.Errorf("Model = %q, want %q", resp.Model, "llama-3.3-70b-versatile")
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
}

func TestResponsesWithArrayInput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request body is converted to chat format
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		var req map[string]interface{}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}

		// Verify messages array exists (converted from input)
		messages, ok := req["messages"].([]interface{})
		if !ok {
			t.Fatal("messages should be an array")
		}
		// Should have system message + 2 input messages
		if len(messages) != 3 {
			t.Errorf("len(messages) = %d, want 3", len(messages))
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-123",
			"object": "chat.completion",
			"created": 1677652288,
			"model": "llama-3.3-70b-versatile",
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "Hello!"
				},
				"finish_reason": "stop"
			}],
			"usage": {
				"prompt_tokens": 10,
				"completion_tokens": 5,
				"total_tokens": 15
			}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	req := &core.ResponsesRequest{
		Model: "llama-3.3-70b-versatile",
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

	if resp.ID != "chatcmpl-123" {
		t.Errorf("ID = %q, want %q", resp.ID, "chatcmpl-123")
	}
}

func TestResponses_PreservesOpaqueFieldsThroughChatAdapter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("Path = %q, want %q", r.URL.Path, "/chat/completions")
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		var req core.ChatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to unmarshal chat request: %v", err)
		}
		if req.ExtraFields.Lookup("response_format") == nil {
			t.Fatal("response_format missing after responses-to-chat conversion")
		}
		if len(req.Messages) != 1 {
			t.Fatalf("len(Messages) = %d, want 1", len(req.Messages))
		}
		if req.Messages[0].ExtraFields.Lookup("x_message_hint") == nil {
			t.Fatal("message extras missing after conversion")
		}
		parts, ok := req.Messages[0].Content.([]core.ContentPart)
		if !ok {
			t.Fatalf("Messages[0].Content type = %T, want []core.ContentPart", req.Messages[0].Content)
		}
		if parts[0].ExtraFields.Lookup("cache_control") == nil {
			t.Fatal("content part extras missing after conversion")
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-opaque",
			"object": "chat.completion",
			"created": 1677652288,
			"model": "llama-3.3-70b-versatile",
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "ok"
				},
				"finish_reason": "stop"
			}],
			"usage": {
				"prompt_tokens": 1,
				"completion_tokens": 1,
				"total_tokens": 2
			}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	req := &core.ResponsesRequest{
		Model: "llama-3.3-70b-versatile",
		Input: []core.ResponsesInputElement{
			{
				Role: "user",
				Content: []core.ContentPart{
					{
						Type: "input_text",
						Text: "Hello",
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

	if _, err := provider.Responses(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStreamResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify stream is set in request body
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		var req core.ChatRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}
		if !req.Stream {
			t.Error("Stream should be true in request")
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama-3.3-70b-versatile","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama-3.3-70b-versatile","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":null}]}

data: [DONE]
`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	req := &core.ResponsesRequest{
		Model: "llama-3.3-70b-versatile",
		Input: "Hello",
	}

	body, err := provider.StreamResponses(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body == nil {
		t.Fatal("body should not be nil")
	}
	defer func() { _ = body.Close() }()

	respBody, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

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
		Model: "llama-3.3-70b-versatile",
		Input: "Hello",
	}

	_, err := provider.Responses(ctx, req)
	if err == nil {
		t.Error("expected error when context is cancelled, got nil")
	}
}

func TestGroqResponsesStreamConverter(t *testing.T) {
	// Test the stream converter with mock chat completion stream
	mockStream := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama-3.3-70b-versatile","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama-3.3-70b-versatile","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}

data: [DONE]
`

	reader := io.NopCloser(strings.NewReader(mockStream))
	converter := providers.NewOpenAIResponsesStreamConverter(reader, "llama-3.3-70b-versatile", "groq")

	// Read all data from converter
	data, err := io.ReadAll(converter)
	if err != nil {
		t.Fatalf("failed to read from converter: %v", err)
	}

	result := string(data)

	// Check that the stream contains expected events
	if !strings.Contains(result, "response.created") {
		t.Error("stream should contain response.created event")
	}
	if !strings.Contains(result, "response.output_text.delta") {
		t.Error("stream should contain response.output_text.delta event")
	}
	if !strings.Contains(result, "Hello") {
		t.Error("stream should contain 'Hello' content")
	}
	if !strings.Contains(result, " world") {
		t.Error("stream should contain ' world' content")
	}
	if !strings.Contains(result, "response.completed") {
		t.Error("stream should contain response.completed event")
	}
	if !strings.Contains(result, "[DONE]") {
		t.Error("stream should contain [DONE] marker")
	}
}

func TestGroqResponsesStreamConverter_Close(t *testing.T) {
	reader := io.NopCloser(strings.NewReader("data: [DONE]\n"))
	converter := providers.NewOpenAIResponsesStreamConverter(reader, "test-model", "groq")

	err := converter.Close()
	if err != nil {
		t.Errorf("Close() returned error: %v", err)
	}

	// Subsequent reads should return EOF
	buf := make([]byte, 100)
	n, err := converter.Read(buf)
	if n != 0 || err != io.EOF {
		t.Errorf("Read after Close: n=%d, err=%v, want n=0, err=EOF", n, err)
	}
}

func TestGroqResponsesStreamConverter_EmptyDelta(t *testing.T) {
	// Test that empty deltas are not emitted
	mockStream := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama-3.3-70b-versatile","choices":[{"index":0,"delta":{},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama-3.3-70b-versatile","choices":[{"index":0,"delta":{"content":""},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama-3.3-70b-versatile","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: [DONE]
`

	reader := io.NopCloser(strings.NewReader(mockStream))
	converter := providers.NewOpenAIResponsesStreamConverter(reader, "llama-3.3-70b-versatile", "groq")

	data, err := io.ReadAll(converter)
	if err != nil {
		t.Fatalf("failed to read from converter: %v", err)
	}

	result := string(data)

	// Count delta event lines - should only have one with "Hello"
	// Each event has "event: response.output_text.delta\n" line
	deltaCount := strings.Count(result, "event: response.output_text.delta")
	if deltaCount != 1 {
		t.Errorf("expected 1 delta event line, got %d", deltaCount)
	}

	// Verify the Hello content is present
	if !strings.Contains(result, `"delta":"Hello"`) {
		t.Error("expected delta with Hello content")
	}
}

func TestNewWithHTTPClient(t *testing.T) {
	customClient := &http.Client{}
	apiKey := "test-api-key"

	provider := NewWithHTTPClient(apiKey, customClient, llmclient.Hooks{})

	if provider.apiKey != apiKey {
		t.Errorf("apiKey = %q, want %q", provider.apiKey, apiKey)
	}
	if provider.client == nil {
		t.Error("client should not be nil")
	}
}

func TestSetBaseURL(t *testing.T) {
	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	customURL := "https://custom.groq.api.com/v1"

	provider.SetBaseURL(customURL)

	// We can't directly check the baseURL as it's encapsulated in llmclient
	// but we can verify the provider still works by making a test request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer server.Close()

	provider.SetBaseURL(server.URL)
	_, err := provider.ListModels(context.Background())
	if err != nil {
		t.Errorf("SetBaseURL should allow using custom URL: %v", err)
	}
}
