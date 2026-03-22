package ollama

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
	if provider.nativeClient == nil {
		t.Error("nativeClient should not be nil")
	}
}

func TestNew_ReturnsProvider(t *testing.T) {
	provider := New("test-api-key", providers.ProviderOptions{})

	if provider == nil {
		t.Error("provider should not be nil")
	}
}

func TestNew_WithoutAPIKey(t *testing.T) {
	// Ollama doesn't require an API key
	provider := NewWithHTTPClient("", nil, llmclient.Hooks{})

	if provider.apiKey != "" {
		t.Errorf("apiKey = %q, want empty", provider.apiKey)
	}
	if provider.client == nil {
		t.Error("client should not be nil")
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
				"model": "llama3.2",
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
				if resp.Model != "llama3.2" {
					t.Errorf("Model = %q, want %q", resp.Model, "llama3.2")
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

			provider := NewWithHTTPClient("", nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			req := &core.ChatRequest{
				Model: "llama3.2",
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

func TestChatCompletion_WithAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify authorization header is set when API key is provided
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			t.Errorf("Authorization header should start with 'Bearer '")
		}
		if authHeader != "Bearer test-api-key" {
			t.Errorf("Authorization = %q, want %q", authHeader, "Bearer test-api-key")
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-123",
			"object": "chat.completion",
			"created": 1677652288,
			"model": "llama3.2",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "Hi"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	req := &core.ChatRequest{
		Model:    "llama3.2",
		Messages: []core.Message{{Role: "user", Content: "Hello"}},
	}

	_, err := provider.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestChatCompletion_WithoutAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify no authorization header when API key is not provided
		authHeader := r.Header.Get("Authorization")
		if authHeader != "" {
			t.Errorf("Authorization header should be empty, got %q", authHeader)
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-123",
			"object": "chat.completion",
			"created": 1677652288,
			"model": "llama3.2",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "Hi"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	req := &core.ChatRequest{
		Model:    "llama3.2",
		Messages: []core.Message{{Role: "user", Content: "Hello"}},
	}

	_, err := provider.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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
			responseBody: `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama3.2","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama3.2","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":null}]}

data: [DONE]
`,
			expectedError: false,
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

			provider := NewWithHTTPClient("", nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			req := &core.ChatRequest{
				Model: "llama3.2",
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
						"id": "llama3.2",
						"object": "model",
						"created": 1687882411,
						"owned_by": "library"
					},
					{
						"id": "mistral:7b-instruct",
						"object": "model",
						"created": 1687882410,
						"owned_by": "library"
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
				if resp.Data[0].ID != "llama3.2" {
					t.Errorf("Data[0].ID = %q, want %q", resp.Data[0].ID, "llama3.2")
				}
				if resp.Data[0].OwnedBy != "library" {
					t.Errorf("Data[0].OwnedBy = %q, want %q", resp.Data[0].OwnedBy, "library")
				}
			},
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
				// Verify request method and path
				if r.Method != http.MethodGet {
					t.Errorf("Method = %q, want %q", r.Method, http.MethodGet)
				}
				if r.URL.Path != "/models" {
					t.Errorf("Path = %q, want %q", r.URL.Path, "/models")
				}

				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			provider := NewWithHTTPClient("", nil, llmclient.Hooks{})
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

	provider := NewWithHTTPClient("", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	req := &core.ChatRequest{
		Model: "llama3.2",
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
		// Verify request path for chat completions (Ollama converts Responses to chat)
		if r.URL.Path != "/chat/completions" {
			t.Errorf("Path = %q, want %q", r.URL.Path, "/chat/completions")
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-123",
			"object": "chat.completion",
			"created": 1677652288,
			"model": "llama3.2",
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

	provider := NewWithHTTPClient("", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	req := &core.ResponsesRequest{
		Model: "llama3.2",
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
	if resp.Model != "llama3.2" {
		t.Errorf("Model = %q, want %q", resp.Model, "llama3.2")
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

		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}

		// Verify messages array exists (converted from input)
		messages, ok := req["messages"].([]any)
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
			"model": "llama3.2",
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

	provider := NewWithHTTPClient("", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	req := &core.ResponsesRequest{
		Model: "llama3.2",
		Input: []any{
			map[string]any{
				"role":    "user",
				"content": "Hello",
			},
			map[string]any{
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
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama3.2","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama3.2","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":null}]}

data: [DONE]
`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	req := &core.ResponsesRequest{
		Model: "llama3.2",
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

	provider := NewWithHTTPClient("", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	req := &core.ResponsesRequest{
		Model: "llama3.2",
		Input: "Hello",
	}

	_, err := provider.Responses(ctx, req)
	if err == nil {
		t.Error("expected error when context is cancelled, got nil")
	}
}

func TestOllamaResponsesStreamConverter(t *testing.T) {
	// Test the stream converter with mock chat completion stream
	mockStream := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama3.2","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama3.2","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}

data: [DONE]
`

	reader := io.NopCloser(strings.NewReader(mockStream))
	converter := providers.NewOpenAIResponsesStreamConverter(reader, "llama3.2", "ollama")

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
	if provider.nativeClient == nil {
		t.Error("nativeClient should not be nil")
	}
}

func TestSetBaseURL(t *testing.T) {
	provider := NewWithHTTPClient("", nil, llmclient.Hooks{})
	customURL := "http://custom.ollama.server:11434/v1"

	provider.SetBaseURL(customURL)

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

func TestSetBaseURL_TrailingSlash(t *testing.T) {
	var nativePath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nativePath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"model":"nomic-embed-text","embeddings":[[0.1,0.2]]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL + "/v1/")

	_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "nomic-embed-text",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if nativePath != "/api/embed" {
		t.Errorf("native client path = %q, want /api/embed (trailing slash not normalized)", nativePath)
	}
}

func TestEmbeddings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("Path = %q, want %q", r.URL.Path, "/api/embed")
		}
		if r.Method != http.MethodPost {
			t.Errorf("Method = %q, want %q", r.Method, http.MethodPost)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		var req ollamaEmbedRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}
		if req.Model != "nomic-embed-text" {
			t.Errorf("Model = %q, want %q", req.Model, "nomic-embed-text")
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"model": "nomic-embed-text",
			"embeddings": [[0.1, 0.2, 0.3], [0.4, 0.5, 0.6]],
			"prompt_eval_count": 8
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL + "/v1")

	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "nomic-embed-text",
		Input: []string{"hello", "world"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Object != "list" {
		t.Errorf("Object = %q, want %q", resp.Object, "list")
	}
	if resp.Model != "nomic-embed-text" {
		t.Errorf("Model = %q, want %q", resp.Model, "nomic-embed-text")
	}
	if len(resp.Data) != 2 {
		t.Fatalf("len(Data) = %d, want 2", len(resp.Data))
	}
	if resp.Data[0].Object != "embedding" {
		t.Errorf("Data[0].Object = %q, want %q", resp.Data[0].Object, "embedding")
	}
	var floats []float64
	if err := json.Unmarshal(resp.Data[0].Embedding, &floats); err != nil {
		t.Fatalf("failed to unmarshal embedding: %v", err)
	}
	if len(floats) != 3 {
		t.Errorf("len(embedding floats) = %d, want 3", len(floats))
	}
	if resp.Data[1].Index != 1 {
		t.Errorf("Data[1].Index = %d, want 1", resp.Data[1].Index)
	}
	if resp.Usage.PromptTokens != 8 {
		t.Errorf("PromptTokens = %d, want 8", resp.Usage.PromptTokens)
	}
	if resp.Usage.TotalTokens != 8 {
		t.Errorf("TotalTokens = %d, want 8", resp.Usage.TotalTokens)
	}
}

func TestEmbeddings_ModelFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"model": "",
			"embeddings": [[0.1]],
			"prompt_eval_count": 1
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL + "/v1")

	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "nomic-embed-text",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Model != "nomic-embed-text" {
		t.Errorf("Model = %q, want %q (should fall back to request model)", resp.Model, "nomic-embed-text")
	}
}
