package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestNilRequests_ReturnInvalidRequestError(t *testing.T) {
	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "chat completion",
			call: func() error {
				_, err := provider.ChatCompletion(context.Background(), nil)
				return err
			},
		},
		{
			name: "stream chat completion",
			call: func() error {
				_, err := provider.StreamChatCompletion(context.Background(), nil)
				return err
			},
		},
		{
			name: "responses",
			call: func() error {
				_, err := provider.Responses(context.Background(), nil)
				return err
			},
		},
		{
			name: "stream responses",
			call: func() error {
				_, err := provider.StreamResponses(context.Background(), nil)
				return err
			},
		},
		{
			name: "embeddings",
			call: func() error {
				_, err := provider.Embeddings(context.Background(), nil)
				return err
			},
		},
		{
			name: "create batch",
			call: func() error {
				_, err := provider.CreateBatch(context.Background(), nil)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("unexpected panic: %v", r)
				}
			}()

			err := tt.call()
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			gatewayErr, ok := err.(*core.GatewayError)
			if !ok {
				t.Fatalf("error type = %T, want *core.GatewayError", err)
			}
			if gatewayErr.Type != core.ErrorTypeInvalidRequest {
				t.Fatalf("error type = %q, want %q", gatewayErr.Type, core.ErrorTypeInvalidRequest)
			}
		})
	}
}

func TestCompatibleProvider_FileHelpersApplyRequestMutator(t *testing.T) {
	mutate := func(req *llmclient.Request) {
		if req.Headers == nil {
			req.Headers = make(http.Header)
		}
		req.Headers.Set("X-Test-Mutated", "yes")

		endpoint, err := url.Parse(req.Endpoint)
		if err != nil {
			t.Fatalf("unexpected parse error: %v", err)
		}
		query := endpoint.Query()
		query.Set("mutated", "1")
		endpoint.RawQuery = query.Encode()
		req.Endpoint = endpoint.String()
	}

	tests := []struct {
		name string
		call func(*CompatibleProvider) error
	}{
		{
			name: "create file",
			call: func(p *CompatibleProvider) error {
				_, err := p.CreateFile(context.Background(), &core.FileCreateRequest{
					Purpose:  "batch",
					Filename: "input.jsonl",
					Content:  []byte(`{"custom_id":"req-1"}`),
				})
				return err
			},
		},
		{
			name: "list files",
			call: func(p *CompatibleProvider) error {
				_, err := p.ListFiles(context.Background(), "batch", 10, "file_122")
				return err
			},
		},
		{
			name: "get file",
			call: func(p *CompatibleProvider) error {
				_, err := p.GetFile(context.Background(), "file_123")
				return err
			},
		},
		{
			name: "delete file",
			call: func(p *CompatibleProvider) error {
				_, err := p.DeleteFile(context.Background(), "file_123")
				return err
			},
		},
		{
			name: "get file content",
			call: func(p *CompatibleProvider) error {
				_, err := p.GetFileContent(context.Background(), "file_123")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotMutatedHeader string
			var gotMutatedQuery string

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMutatedHeader = r.Header.Get("X-Test-Mutated")
				gotMutatedQuery = r.URL.Query().Get("mutated")
				w.Header().Set("Content-Type", "application/json")

				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/files":
					_, _ = w.Write([]byte(`{"id":"file_123","object":"file","purpose":"batch"}`))
				case r.Method == http.MethodGet && r.URL.Path == "/files":
					_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
				case r.Method == http.MethodGet && r.URL.Path == "/files/file_123":
					_, _ = w.Write([]byte(`{"id":"file_123","object":"file","purpose":"batch"}`))
				case r.Method == http.MethodDelete && r.URL.Path == "/files/file_123":
					_, _ = w.Write([]byte(`{"id":"file_123","object":"file.deleted","deleted":true}`))
				case r.Method == http.MethodGet && r.URL.Path == "/files/file_123/content":
					w.Header().Set("Content-Type", "application/octet-stream")
					_, _ = w.Write([]byte("file-bytes"))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			provider := NewCompatibleProviderWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{}, CompatibleProviderConfig{
				ProviderName:   "test",
				DefaultBaseURL: server.URL,
			})
			provider.SetRequestMutator(mutate)

			if err := tt.call(provider); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotMutatedHeader != "yes" {
				t.Fatalf("X-Test-Mutated = %q, want yes", gotMutatedHeader)
			}
			if gotMutatedQuery != "1" {
				t.Fatalf("mutated query = %q, want 1", gotMutatedQuery)
			}
		})
	}
}

func TestCompatibleProvider_GetBatchResultsAppliesRequestMutator(t *testing.T) {
	mutate := func(req *llmclient.Request) {
		if req.Headers == nil {
			req.Headers = make(http.Header)
		}
		req.Headers.Set("X-Test-Mutated", "yes")

		endpoint, err := url.Parse(req.Endpoint)
		if err != nil {
			t.Fatalf("unexpected parse error: %v", err)
		}
		query := endpoint.Query()
		query.Set("mutated", "1")
		endpoint.RawQuery = query.Encode()
		req.Endpoint = endpoint.String()
	}

	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path+"?"+r.URL.RawQuery+"|"+r.Header.Get("X-Test-Mutated"))
		switch r.URL.Path {
		case "/batches/batch_1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"batch_1","status":"completed","output_file_id":"file_1","endpoint":"/v1/chat/completions"}`))
		case "/files/file_1/content":
			w.Header().Set("Content-Type", "application/jsonl")
			_, _ = w.Write([]byte(`{"custom_id":"ok-1","response":{"status_code":200,"url":"/v1/chat/completions","body":{"id":"resp-1","model":"gpt-4o-mini"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := NewCompatibleProviderWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{}, CompatibleProviderConfig{
		ProviderName:   "test",
		DefaultBaseURL: server.URL,
	})
	provider.SetRequestMutator(mutate)

	_, err := provider.GetBatchResults(context.Background(), "batch_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("saw %d requests, want 2", len(seen))
	}
	for _, request := range seen {
		if !strings.Contains(request, "mutated=1") {
			t.Fatalf("request = %q, want mutated query", request)
		}
		if !strings.HasSuffix(request, "|yes") {
			t.Fatalf("request = %q, want mutated header", request)
		}
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
				"model": "gpt-4o",
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
				if resp.Model != "gpt-4o" {
					t.Errorf("Model = %q, want %q", resp.Model, "gpt-4o")
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
				Model: "gpt-4o",
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

func TestChatCompletion_PreservesMultimodalContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		var req map[string]interface{}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}

		messages, ok := req["messages"].([]interface{})
		if !ok || len(messages) != 1 {
			t.Fatalf("messages = %#v, want single message", req["messages"])
		}
		message, ok := messages[0].(map[string]interface{})
		if !ok {
			t.Fatalf("message type = %T", messages[0])
		}
		content, ok := message["content"].([]interface{})
		if !ok {
			t.Fatalf("content type = %T, want []interface{}", message["content"])
		}
		if len(content) != 2 {
			t.Fatalf("len(content) = %d, want 2", len(content))
		}
		second, ok := content[1].(map[string]interface{})
		if !ok {
			t.Fatalf("second part type = %T", content[1])
		}
		if second["type"] != "image_url" {
			t.Fatalf("second part type = %v, want image_url", second["type"])
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-123",
			"object": "chat.completion",
			"created": 1677652288,
			"model": "gpt-4o-mini",
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "ok"
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

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	req := &core.ChatRequest{
		Model: "gpt-4o-mini",
		Messages: []core.Message{
			{
				Role: "user",
				Content: []core.ContentPart{
					{Type: "text", Text: "Describe the image."},
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

	resp, err := provider.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Choices[0].Message.Content != "ok" {
		t.Fatalf("response content = %q, want ok", resp.Choices[0].Message.Content)
	}
}

func TestChatCompletion_PreservesUnknownTopLevelFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}

		responseFormat, ok := req["response_format"].(map[string]any)
		if !ok {
			t.Fatalf("response_format = %#v, want object", req["response_format"])
		}
		if responseFormat["type"] != "json_schema" {
			t.Fatalf("response_format.type = %#v, want json_schema", responseFormat["type"])
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-123",
			"object": "chat.completion",
			"created": 1677652288,
			"model": "gpt-5-mini",
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "ok"
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

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	req := &core.ChatRequest{
		Model: "gpt-5-mini",
		Messages: []core.Message{
			{Role: "user", Content: "Return JSON."},
		},
		ExtraFields: map[string]json.RawMessage{
			"response_format": json.RawMessage(`{
				"type":"json_schema",
				"json_schema":{
					"name":"math_response",
					"schema":{"type":"object","properties":{"answer":{"type":"string"}}}
				}
			}`),
		},
	}

	resp, err := provider.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Choices[0].Message.Content != "ok" {
		t.Fatalf("response content = %q, want ok", resp.Choices[0].Message.Content)
	}
}

func TestChatCompletion_PreservesUnknownNestedFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}

		messages, ok := req["messages"].([]any)
		if !ok || len(messages) != 1 {
			t.Fatalf("messages = %#v, want []any len=1", req["messages"])
		}
		message, ok := messages[0].(map[string]any)
		if !ok {
			t.Fatalf("messages[0] = %#v, want object", messages[0])
		}
		if message["name"] != "alice" {
			t.Fatalf("messages[0].name = %#v, want alice", message["name"])
		}
		content, ok := message["content"].([]any)
		if !ok || len(content) != 1 {
			t.Fatalf("messages[0].content = %#v, want []any len=1", message["content"])
		}
		part, ok := content[0].(map[string]any)
		if !ok {
			t.Fatalf("messages[0].content[0] = %#v, want object", content[0])
		}
		if _, ok := part["cache_control"].(map[string]any); !ok {
			t.Fatalf("messages[0].content[0].cache_control = %#v, want object", part["cache_control"])
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-123",
			"object": "chat.completion",
			"created": 1677652288,
			"model": "gpt-5-mini",
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "ok"
				},
				"finish_reason": "stop"
			}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	req := &core.ChatRequest{
		Model: "gpt-5-mini",
		Messages: []core.Message{
			{
				Role:        "user",
				Content:     []core.ContentPart{{Type: "text", Text: "hello", ExtraFields: map[string]json.RawMessage{"cache_control": json.RawMessage(`{"type":"ephemeral"}`)}}},
				ExtraFields: map[string]json.RawMessage{"name": json.RawMessage(`"alice"`)},
			},
		},
	}

	resp, err := provider.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Choices[0].Message.Content != "ok" {
		t.Fatalf("response content = %q, want ok", resp.Choices[0].Message.Content)
	}
}

func TestChatCompletion_PreservesUnknownTopLevelFieldsForOSeries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}

		if _, exists := req["temperature"]; exists {
			t.Fatalf("temperature should be removed for o-series models, got %#v", req["temperature"])
		}
		if req["max_completion_tokens"] != float64(128) {
			t.Fatalf("max_completion_tokens = %#v, want 128", req["max_completion_tokens"])
		}
		responseFormat, ok := req["response_format"].(map[string]any)
		if !ok {
			t.Fatalf("response_format = %#v, want object", req["response_format"])
		}
		if responseFormat["type"] != "json_schema" {
			t.Fatalf("response_format.type = %#v, want json_schema", responseFormat["type"])
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-123",
			"object": "chat.completion",
			"created": 1677652288,
			"model": "o3-mini",
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "ok"
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

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	maxTokens := 128
	temperature := 0.7
	req := &core.ChatRequest{
		Model:       "o3-mini",
		Temperature: &temperature,
		MaxTokens:   &maxTokens,
		Messages: []core.Message{
			{Role: "user", Content: "Return JSON."},
		},
		ExtraFields: map[string]json.RawMessage{
			"response_format": json.RawMessage(`{
				"type":"json_schema",
				"json_schema":{
					"name":"math_response",
					"schema":{"type":"object","properties":{"answer":{"type":"string"}}}
				}
			}`),
		},
	}

	resp, err := provider.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Choices[0].Message.Content != "ok" {
		t.Fatalf("response content = %q, want ok", resp.Choices[0].Message.Content)
	}
}

func TestChatCompletion_OSeriesMarshalErrorReturnsInvalidRequest(t *testing.T) {
	provider := NewWithHTTPClient("test-api-key", http.DefaultClient, llmclient.Hooks{})

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "o3-mini",
		Messages: []core.Message{
			{Role: "user", Content: "hello"},
		},
		ExtraFields: map[string]json.RawMessage{
			"x_invalid": json.RawMessage(`{`),
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	gwErr, ok := err.(*core.GatewayError)
	if !ok {
		t.Fatalf("expected GatewayError, got %T: %v", err, err)
	}
	if gwErr.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("Type = %q, want %q", gwErr.Type, core.ErrorTypeInvalidRequest)
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
			responseBody: `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":null}]}

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
				Model: "gpt-4o",
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
						"id": "gpt-4o",
						"object": "model",
						"created": 1687882411,
						"owned_by": "openai"
					},
					{
						"id": "gpt-4",
						"object": "model",
						"created": 1687882410,
						"owned_by": "openai"
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
				if resp.Data[0].ID != "gpt-4o" {
					t.Errorf("Data[0].ID = %q, want %q", resp.Data[0].ID, "gpt-4o")
				}
				if resp.Data[0].OwnedBy != "openai" {
					t.Errorf("Data[0].OwnedBy = %q, want %q", resp.Data[0].OwnedBy, "openai")
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
		Model: "gpt-4o",
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
				"id": "resp_123",
				"object": "response",
				"created_at": 1677652288,
				"model": "gpt-4o",
				"status": "completed",
				"output": [{
					"id": "msg_123",
					"type": "message",
					"role": "assistant",
					"status": "completed",
					"content": [{
						"type": "output_text",
						"text": "Hello! How can I help you today?"
					}]
				}],
				"usage": {
					"input_tokens": 10,
					"output_tokens": 20,
					"total_tokens": 30
				}
			}`,
			expectedError: false,
			checkResponse: func(t *testing.T, resp *core.ResponsesResponse) {
				if resp.ID != "resp_123" {
					t.Errorf("ID = %q, want %q", resp.ID, "resp_123")
				}
				if resp.Object != "response" {
					t.Errorf("Object = %q, want %q", resp.Object, "response")
				}
				if resp.Model != "gpt-4o" {
					t.Errorf("Model = %q, want %q", resp.Model, "gpt-4o")
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
			name:       "successful request with structured annotations",
			statusCode: http.StatusOK,
			responseBody: `{
				"id": "resp_annotated",
				"object": "response",
				"created_at": 1677652288,
				"model": "gpt-4o",
				"status": "completed",
				"output": [{
					"id": "msg_annotated",
					"type": "message",
					"role": "assistant",
					"status": "completed",
					"content": [{
						"type": "output_text",
						"text": "Search result summary",
						"annotations": [{
							"type": "url_citation",
							"title": "Example Domain",
							"url": "https://example.com"
						}]
					}]
				}]
			}`,
			checkResponse: func(t *testing.T, resp *core.ResponsesResponse) {
				if len(resp.Output) != 1 || len(resp.Output[0].Content) != 1 {
					t.Fatalf("unexpected output shape: %+v", resp.Output)
				}

				annotations := resp.Output[0].Content[0].Annotations
				if len(annotations) != 1 {
					t.Fatalf("len(Annotations) = %d, want 1", len(annotations))
				}

				var annotation map[string]any
				if err := json.Unmarshal(annotations[0], &annotation); err != nil {
					t.Fatalf("json.Unmarshal(annotation) error = %v", err)
				}
				if annotation["type"] != "url_citation" {
					t.Fatalf("annotation.type = %#v, want url_citation", annotation["type"])
				}
			},
		},
		{
			name:          "API error - unauthorized",
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

				// Verify request path
				if r.URL.Path != "/responses" {
					t.Errorf("Path = %q, want %q", r.URL.Path, "/responses")
				}

				// Verify request body
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("failed to read request body: %v", err)
				}
				var req core.ResponsesRequest
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
				Model: "gpt-4o",
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
		// Verify request body contains array input
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		var req map[string]interface{}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}

		// Verify input is an array
		input, ok := req["input"].([]interface{})
		if !ok {
			t.Fatal("input should be an array")
		}
		if len(input) != 2 {
			t.Errorf("len(input) = %d, want 2", len(input))
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "resp_123",
			"object": "response",
			"created_at": 1677652288,
			"model": "gpt-4o",
			"status": "completed",
			"output": [{
				"id": "msg_123",
				"type": "message",
				"role": "assistant",
				"status": "completed",
				"content": [{
					"type": "output_text",
					"text": "Hello!"
				}]
			}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	req := &core.ResponsesRequest{
		Model: "gpt-4o",
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

	if resp.ID != "resp_123" {
		t.Errorf("ID = %q, want %q", resp.ID, "resp_123")
	}
}

func TestResponses_PreservesUnknownNestedFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}
		input, ok := req["input"].([]any)
		if !ok || len(input) != 1 {
			t.Fatalf("input = %#v, want []any len=1", req["input"])
		}
		first, ok := input[0].(map[string]any)
		if !ok {
			t.Fatalf("input[0] = %#v, want object", input[0])
		}
		if _, ok := first["x_trace"].(map[string]any); !ok {
			t.Fatalf("input[0].x_trace = %#v, want object", first["x_trace"])
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "resp_123",
			"object": "response",
			"created_at": 1677652288,
			"model": "gpt-4o",
			"status": "completed",
			"output": [{
				"id": "msg_123",
				"type": "message",
				"role": "assistant",
				"status": "completed",
				"content": [{
					"type": "output_text",
					"text": "Hello!"
				}]
			}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	req := &core.ResponsesRequest{
		Model: "gpt-4o",
		Input: []core.ResponsesInputElement{
			{
				Type:        "message",
				Role:        "user",
				Content:     "Hello",
				ExtraFields: map[string]json.RawMessage{"x_trace": json.RawMessage(`{"id":"trace-1"}`)},
			},
		},
	}

	resp, err := provider.Responses(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "resp_123" {
		t.Errorf("ID = %q, want %q", resp.ID, "resp_123")
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
			responseBody: `event: response.created
data: {"type":"response.created","response":{"id":"resp_123","object":"response","status":"in_progress","model":"gpt-4o"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hello"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"!"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_123","object":"response","status":"completed","model":"gpt-4o"}}
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
			responseBody:  `{"error": {"message": "Invalid API key"}}`,
			expectedError: true,
		},
		{
			name:          "rate limit error",
			statusCode:    http.StatusTooManyRequests,
			responseBody:  `{"error": {"message": "Rate limit exceeded"}}`,
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

				// Verify request path
				if r.URL.Path != "/responses" {
					t.Errorf("Path = %q, want %q", r.URL.Path, "/responses")
				}

				// Verify stream is set in request body
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("failed to read request body: %v", err)
				}
				var req core.ResponsesRequest
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
				Model: "gpt-4o",
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
		Model: "gpt-4o",
		Input: "Hello",
	}

	_, err := provider.Responses(ctx, req)
	if err == nil {
		t.Error("expected error when context is cancelled, got nil")
	}
}

func TestIsOSeriesModel(t *testing.T) {
	tests := []struct {
		model    string
		expected bool
	}{
		{"o3-mini", true},
		{"o4-mini", true},
		{"o3", true},
		{"o4", true},
		{"o1-preview", true},
		{"o1-mini", true},
		{"o3-mini-2025-01-31", true},
		{"gpt-4o", false},
		{"gpt-4o-mini", false},
		{"gpt-4", false},
		{"gpt-3.5-turbo", false},
		{"claude-3-opus", false},
		{"", false},
		{"o", false},
		{"openai", false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := isOSeriesModel(tt.model); got != tt.expected {
				t.Errorf("isOSeriesModel(%q) = %v, want %v", tt.model, got, tt.expected)
			}
		})
	}
}

func TestChatCompletion_ReasoningModel_AdaptsParameters(t *testing.T) {
	maxTokens := 1000

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		var raw map[string]interface{}
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}

		// max_tokens must NOT be present
		if _, ok := raw["max_tokens"]; ok {
			t.Error("reasoning model request should not contain max_tokens")
		}

		// max_completion_tokens must be present with the right value
		mct, ok := raw["max_completion_tokens"]
		if !ok {
			t.Fatal("reasoning model request should contain max_completion_tokens")
		}
		if int(mct.(float64)) != maxTokens {
			t.Errorf("max_completion_tokens = %v, want %d", mct, maxTokens)
		}

		// temperature must NOT be present
		if _, ok := raw["temperature"]; ok {
			t.Error("reasoning model request should not contain temperature")
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-123",
			"object": "chat.completion",
			"model": "o3-mini",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "Hi"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 5, "completion_tokens": 10, "total_tokens": 15}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	temp := 0.7
	req := &core.ChatRequest{
		Model:       "o3-mini",
		Messages:    []core.Message{{Role: "user", Content: "Hello"}},
		MaxTokens:   &maxTokens,
		Temperature: &temp,
	}

	resp, err := provider.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Model != "o3-mini" {
		t.Errorf("Model = %q, want %q", resp.Model, "o3-mini")
	}
}

func TestChatCompletion_NonReasoningModel_PassesMaxTokens(t *testing.T) {
	maxTokens := 1000

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		var raw map[string]interface{}
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}

		// max_tokens must be present
		mt, ok := raw["max_tokens"]
		if !ok {
			t.Fatal("non-reasoning model request should contain max_tokens")
		}
		if int(mt.(float64)) != maxTokens {
			t.Errorf("max_tokens = %v, want %d", mt, maxTokens)
		}

		// max_completion_tokens must NOT be present
		if _, ok := raw["max_completion_tokens"]; ok {
			t.Error("non-reasoning model request should not contain max_completion_tokens")
		}

		// temperature must be present
		if _, ok := raw["temperature"]; !ok {
			t.Error("non-reasoning model request should contain temperature")
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-456",
			"object": "chat.completion",
			"model": "gpt-4o",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "Hi"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 5, "completion_tokens": 10, "total_tokens": 15}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	temp := 0.7
	req := &core.ChatRequest{
		Model:       "gpt-4o",
		Messages:    []core.Message{{Role: "user", Content: "Hello"}},
		MaxTokens:   &maxTokens,
		Temperature: &temp,
	}

	resp, err := provider.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Model != "gpt-4o" {
		t.Errorf("Model = %q, want %q", resp.Model, "gpt-4o")
	}
}

func TestChatCompletion_NonReasoningModel_PreservesToolConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		var raw map[string]interface{}
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}

		tools, ok := raw["tools"].([]interface{})
		if !ok || len(tools) != 1 {
			t.Fatalf("tools = %#v, want one tool", raw["tools"])
		}

		toolChoice, ok := raw["tool_choice"].(map[string]interface{})
		if !ok {
			t.Fatalf("tool_choice = %#v, want object", raw["tool_choice"])
		}
		function, ok := toolChoice["function"].(map[string]interface{})
		if !ok || function["name"] != "lookup_weather" {
			t.Fatalf("tool_choice.function = %#v, want lookup_weather", toolChoice["function"])
		}

		parallelToolCalls, ok := raw["parallel_tool_calls"].(bool)
		if !ok || parallelToolCalls {
			t.Fatalf("parallel_tool_calls = %#v, want false", raw["parallel_tool_calls"])
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-tools",
			"object": "chat.completion",
			"model": "gpt-4o-mini",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "", "tool_calls": [{"id": "call_123", "type": "function", "function": {"name": "lookup_weather", "arguments": "{\"city\":\"Warsaw\"}"}}]}, "finish_reason": "tool_calls"}],
			"usage": {"prompt_tokens": 5, "completion_tokens": 10, "total_tokens": 15}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	parallelToolCalls := false
	req := &core.ChatRequest{
		Model: "gpt-4o-mini",
		Messages: []core.Message{
			{Role: "user", Content: "What's the weather?"},
		},
		Tools: []map[string]any{
			{
				"type": "function",
				"function": map[string]any{
					"name":        "lookup_weather",
					"description": "Get the weather for a city.",
					"parameters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"city": map[string]any{"type": "string"},
						},
						"required": []string{"city"},
					},
				},
			},
		},
		ToolChoice:        map[string]any{"type": "function", "function": map[string]any{"name": "lookup_weather"}},
		ParallelToolCalls: &parallelToolCalls,
	}

	resp, err := provider.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("FinishReason = %q, want tool_calls", resp.Choices[0].FinishReason)
	}
	if len(resp.Choices[0].Message.ToolCalls) != 1 || resp.Choices[0].Message.ToolCalls[0].Function.Name != "lookup_weather" {
		t.Fatalf("tool_calls = %+v, want lookup_weather", resp.Choices[0].Message.ToolCalls)
	}
}

func TestStreamChatCompletion_ReasoningModel_AdaptsParameters(t *testing.T) {
	maxTokens := 2000

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		var raw map[string]interface{}
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}

		// Must use max_completion_tokens, not max_tokens
		if _, ok := raw["max_tokens"]; ok {
			t.Error("streaming reasoning model request should not contain max_tokens")
		}
		mct, ok := raw["max_completion_tokens"]
		if !ok {
			t.Fatal("streaming reasoning model request should contain max_completion_tokens")
		}
		if int(mct.(float64)) != maxTokens {
			t.Errorf("max_completion_tokens = %v, want %d", mct, maxTokens)
		}

		// stream must be true
		if stream, ok := raw["stream"].(bool); !ok || !stream {
			t.Error("stream should be true")
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl-123","object":"chat.completion.chunk","model":"o4-mini","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}

data: [DONE]
`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	req := &core.ChatRequest{
		Model:     "o4-mini",
		Messages:  []core.Message{{Role: "user", Content: "Hello"}},
		MaxTokens: &maxTokens,
	}

	body, err := provider.StreamChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = body.Close() }()

	respBody, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	if !strings.Contains(string(respBody), "o4-mini") {
		t.Error("response should contain o4-mini model")
	}
}

func TestChatCompletion_ReasoningModel_PreservesToolConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}

		var raw map[string]interface{}
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}

		tools, ok := raw["tools"].([]interface{})
		if !ok || len(tools) != 1 {
			t.Fatalf("tools = %#v, want one tool", raw["tools"])
		}

		toolChoice, ok := raw["tool_choice"].(map[string]interface{})
		if !ok {
			t.Fatalf("tool_choice = %#v, want object", raw["tool_choice"])
		}
		function, ok := toolChoice["function"].(map[string]interface{})
		if !ok || function["name"] != "lookup_weather" {
			t.Fatalf("tool_choice.function = %#v, want lookup_weather", toolChoice["function"])
		}

		if _, ok := raw["max_tokens"]; ok {
			t.Fatal("reasoning model request should not contain max_tokens")
		}
		if _, ok := raw["max_completion_tokens"]; !ok {
			t.Fatal("reasoning model request should contain max_completion_tokens")
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-tools-o3",
			"object": "chat.completion",
			"model": "o3-mini",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "", "tool_calls": [{"id": "call_123", "type": "function", "function": {"name": "lookup_weather", "arguments": "{\"city\":\"Warsaw\"}"}}]}, "finish_reason": "tool_calls"}],
			"usage": {"prompt_tokens": 5, "completion_tokens": 10, "total_tokens": 15}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	maxTokens := 256
	req := &core.ChatRequest{
		Model: "o3-mini",
		Messages: []core.Message{
			{Role: "user", Content: "What's the weather?"},
		},
		MaxTokens: &maxTokens,
		Tools: []map[string]any{
			{
				"type": "function",
				"function": map[string]any{
					"name": "lookup_weather",
					"parameters": map[string]any{
						"type": "object",
					},
				},
			},
		},
		ToolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "lookup_weather"}},
	}

	resp, err := provider.ChatCompletion(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("FinishReason = %q, want tool_calls", resp.Choices[0].FinishReason)
	}
	if len(resp.Choices[0].Message.ToolCalls) != 1 || resp.Choices[0].Message.ToolCalls[0].Function.Name != "lookup_weather" {
		t.Fatalf("tool_calls = %+v, want lookup_weather", resp.Choices[0].Message.ToolCalls)
	}
}

func TestIsValidClientRequestID(t *testing.T) {
	tests := []struct {
		name  string
		id    string
		valid bool
	}{
		{
			name:  "valid UUID",
			id:    "123e4567-e89b-12d3-a456-426614174000",
			valid: true,
		},
		{
			name:  "valid short ID",
			id:    "req-123",
			valid: true,
		},
		{
			name:  "valid empty string",
			id:    "",
			valid: true,
		},
		{
			name:  "valid 512 chars",
			id:    strings.Repeat("a", 512),
			valid: true,
		},
		{
			name:  "invalid - 513 chars (too long)",
			id:    strings.Repeat("a", 513),
			valid: false,
		},
		{
			name:  "invalid - non-ASCII character",
			id:    "req-123-日本語",
			valid: false,
		},
		{
			name:  "invalid - emoji",
			id:    "req-123-🎉",
			valid: false,
		},
		{
			name:  "valid - all printable ASCII",
			id:    "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.",
			valid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isValidClientRequestID(tt.id)
			if got != tt.valid {
				t.Errorf("isValidClientRequestID(%q) = %v, want %v", tt.id, got, tt.valid)
			}
		})
	}
}

func TestPassthrough(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBeta string
	var gotBody string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		gotAuth = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("OpenAI-Beta")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "responses?foo=bar",
		Body:     io.NopCloser(strings.NewReader(`{"model":"gpt-5-mini"}`)),
		Headers: http.Header{
			"Content-Type": {"application/json"},
			"OpenAI-Beta":  {"responses=v1"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if gotPath != "/responses?foo=bar" {
		t.Fatalf("path = %q, want /responses?foo=bar", gotPath)
	}
	if gotAuth != "Bearer test-api-key" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotBeta != "responses=v1" {
		t.Fatalf("OpenAI-Beta = %q", gotBeta)
	}
	if gotBody != `{"model":"gpt-5-mini"}` {
		t.Fatalf("body = %q", gotBody)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	if string(body) != `{"error":"rate limited"}` {
		t.Fatalf("response body = %q", string(body))
	}
}
