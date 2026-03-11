package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func TestStreamConverter_DrainsBufferedDoneMessage(t *testing.T) {
	stream := newStreamConverter(io.NopCloser(strings.NewReader("")), "claude-sonnet-4-5-20250929")
	defer func() { _ = stream.Close() }()

	buf := make([]byte, 4)
	var out strings.Builder

	for {
		n, err := stream.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}
	}

	if out.String() != "data: [DONE]\n\n" {
		t.Fatalf("stream output = %q, want %q", out.String(), "data: [DONE]\\n\\n")
	}
}

func TestSetBatchResultEndpoints_PreservesOlderBatches(t *testing.T) {
	provider := &Provider{
		batchResultEndpoints: make(map[string]map[string]string),
	}

	for i := 0; i <= 1024; i++ {
		batchID := "batch-" + strconv.Itoa(i)
		provider.setBatchResultEndpoints(batchID, map[string]string{
			"req-1": "/v1/chat/completions",
		})
	}

	if got := provider.getBatchResultEndpoints("batch-0"); got == nil {
		t.Fatal("batch-0 should still be present")
	}
	if got := provider.getBatchResultEndpoints("batch-1"); got == nil {
		t.Fatal("batch-1 should still be present")
	}
	if got := provider.getBatchResultEndpoints("batch-1024"); got == nil {
		t.Fatal("newest batch should still be present")
	}
	if got := len(provider.batchResultEndpoints); got != 1025 {
		t.Fatalf("len(batchResultEndpoints) = %d, want 1025", got)
	}
}

func TestSetBatchResultEndpoints_OverwritesExistingBatch(t *testing.T) {
	provider := &Provider{
		batchResultEndpoints: make(map[string]map[string]string),
	}

	provider.setBatchResultEndpoints("batch-0", map[string]string{
		"req-1": "/v1/chat/completions",
	})
	provider.setBatchResultEndpoints("batch-0", map[string]string{
		"req-1": "/v1/responses",
	})

	refreshed := provider.getBatchResultEndpoints("batch-0")
	if refreshed == nil {
		t.Fatal("batch-0 should still be present after refresh")
	}
	if refreshed["req-1"] != "/v1/responses" {
		t.Fatalf("batch-0 endpoint = %q, want /v1/responses", refreshed["req-1"])
	}
	if got := len(provider.batchResultEndpoints); got != 1 {
		t.Fatalf("len(batchResultEndpoints) = %d, want 1", got)
	}
}

func TestGetBatchResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages/batches/batch_1/results" {
			http.NotFound(w, r)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			`{"custom_id":"ok-1","result":{"type":"succeeded","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}}}` + "\n" +
				`{"custom_id":"err-1","result":{"type":"errored","error":{"type":"invalid_request_error","message":"bad request"}}}`,
		))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)
	provider.setBatchResultEndpoints("batch_1", map[string]string{
		"ok-1":  "/v1/responses",
		"err-1": "/v1/chat/completions",
	})

	resp, err := provider.GetBatchResults(context.Background(), "batch_1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.BatchID != "batch_1" {
		t.Fatalf("BatchID = %q, want %q", resp.BatchID, "batch_1")
	}
	if len(resp.Data) != 2 {
		t.Fatalf("len(Data) = %d, want 2", len(resp.Data))
	}
	if resp.Data[0].URL != "/v1/responses" || resp.Data[0].StatusCode != http.StatusOK {
		t.Fatalf("unexpected first row: %+v", resp.Data[0])
	}
	if resp.Data[1].Error == nil || resp.Data[1].Error.Message != "bad request" {
		t.Fatalf("unexpected error row: %+v", resp.Data[1])
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

func TestStreamChatCompletion_MergesUsageFromMessageStart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0,"cache_read_input_tokens":6}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}
`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	responseStr := string(raw)
	if !strings.Contains(responseStr, `"prompt_tokens":10`) {
		t.Fatalf("expected prompt_tokens in streamed usage, got %q", responseStr)
	}
	if !strings.Contains(responseStr, `"completion_tokens":2`) {
		t.Fatalf("expected completion_tokens in streamed usage, got %q", responseStr)
	}
	if !strings.Contains(responseStr, `"total_tokens":12`) {
		t.Fatalf("expected total_tokens in streamed usage, got %q", responseStr)
	}
	if !strings.Contains(responseStr, `"cache_read_input_tokens":6`) {
		t.Fatalf("expected cache_read_input_tokens in streamed usage, got %q", responseStr)
	}
}

func TestStreamChatCompletion_WithToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_123","name":"lookup_weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"War"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"saw\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":10,"output_tokens":4}}

event: message_stop
data: {"type":"message_stop"}
`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{Role: "user", Content: "What's the weather?"},
		},
		Tools: []map[string]any{
			{
				"type": "function",
				"function": map[string]any{
					"name": "lookup_weather",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	events := parseTestSSEEvents(t, string(raw))
	foundToolStart := false
	foundFinish := false
	var argumentDeltas strings.Builder

	for _, event := range events {
		if event.Done {
			continue
		}

		choices, ok := event.Payload["choices"].([]interface{})
		if !ok || len(choices) == 0 {
			continue
		}
		choice, ok := choices[0].(map[string]interface{})
		if !ok {
			continue
		}

		if finishReason, _ := choice["finish_reason"].(string); finishReason == "tool_calls" {
			foundFinish = true
		}

		delta, ok := choice["delta"].(map[string]interface{})
		if !ok {
			continue
		}
		toolCalls, ok := delta["tool_calls"].([]interface{})
		if !ok || len(toolCalls) == 0 {
			continue
		}
		toolCall, ok := toolCalls[0].(map[string]interface{})
		if !ok {
			continue
		}
		function, _ := toolCall["function"].(map[string]interface{})

		if toolCall["id"] == "toolu_123" && function["name"] == "lookup_weather" {
			foundToolStart = true
		}
		if arguments, _ := function["arguments"].(string); arguments != "" {
			argumentDeltas.WriteString(arguments)
		}
	}

	if !foundToolStart {
		t.Fatal("expected a streaming tool call header chunk")
	}
	if argumentDeltas.String() != `{"city":"Warsaw"}` {
		t.Fatalf("streamed tool call arguments = %q, want %q", argumentDeltas.String(), `{"city":"Warsaw"}`)
	}
	if !foundFinish {
		t.Fatal("expected a final tool_calls finish_reason chunk")
	}
}

func TestStreamChatCompletion_WithEmptyToolArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_123","name":"lookup_weather","input":{}}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":10,"output_tokens":4}}

event: message_stop
data: {"type":"message_stop"}
`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{Role: "user", Content: "What's the weather?"},
		},
		Tools: []map[string]any{
			{
				"type": "function",
				"function": map[string]any{
					"name": "lookup_weather",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	events := parseTestSSEEvents(t, string(raw))
	foundToolCall := false
	foundFinish := false

	for _, event := range events {
		if event.Done {
			continue
		}
		choices, ok := event.Payload["choices"].([]interface{})
		if !ok || len(choices) == 0 {
			continue
		}
		choice, ok := choices[0].(map[string]interface{})
		if !ok {
			continue
		}
		if finishReason, _ := choice["finish_reason"].(string); finishReason == "tool_calls" {
			foundFinish = true
		}
		delta, _ := choice["delta"].(map[string]interface{})
		toolCalls, _ := delta["tool_calls"].([]interface{})
		if len(toolCalls) == 0 {
			continue
		}
		toolCall, _ := toolCalls[0].(map[string]interface{})
		function, _ := toolCall["function"].(map[string]interface{})
		if toolCall["id"] == "toolu_123" && function["name"] == "lookup_weather" && function["arguments"] == "{}" {
			foundToolCall = true
		}
	}

	if !foundToolCall {
		t.Fatal("expected streamed tool call with {} arguments for zero-arg tool")
	}
	if !foundFinish {
		t.Fatal("expected a final tool_calls finish_reason chunk")
	}
}

func TestStreamChatCompletion_ToolUseWithoutToolChunksKeepsRawFinishReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}
`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{Role: "user", Content: "call tool"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	events := parseTestSSEEvents(t, string(raw))
	if len(events) == 0 {
		t.Fatal("expected at least one SSE event")
	}

	for _, event := range events {
		if event.Done {
			continue
		}

		choices, ok := event.Payload["choices"].([]interface{})
		if !ok || len(choices) == 0 {
			continue
		}
		choice, ok := choices[0].(map[string]interface{})
		if !ok {
			continue
		}
		if choice["finish_reason"] != "tool_use" {
			t.Fatalf("finish_reason = %#v, want %q", choice["finish_reason"], "tool_use")
		}

		delta, _ := choice["delta"].(map[string]interface{})
		if _, ok := delta["tool_calls"]; ok {
			t.Fatalf("did not expect tool_calls in malformed stream fallback, got %#v", delta["tool_calls"])
		}
		return
	}

	t.Fatal("expected a chat completion chunk")
}

func TestStreamChatCompletion_MalformedEventReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"broken"}
`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	if err == nil {
		t.Fatal("expected malformed stream error")
	}

	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("expected GatewayError, got %T", err)
	}
	if gatewayErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", gatewayErr.StatusCode, http.StatusBadGateway)
	}
	if !strings.Contains(gatewayErr.Message, "failed to decode anthropic stream event") {
		t.Fatalf("message = %q, want decode failure", gatewayErr.Message)
	}
	if !strings.Contains(string(raw), `"content":"Hello"`) {
		t.Fatalf("expected stream to include prior converted chunk, got %q", string(raw))
	}
	if strings.Contains(string(raw), "[DONE]") {
		t.Fatalf("did not expect [DONE] after malformed event, got %q", string(raw))
	}
}

type testSSEEvent struct {
	Name    string
	Payload map[string]interface{}
	Done    bool
}

func parseTestSSEEvents(t *testing.T, raw string) []testSSEEvent {
	t.Helper()

	lines := strings.Split(raw, "\n")
	events := make([]testSSEEvent, 0)
	currentEventName := ""

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			currentEventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			events = append(events, testSSEEvent{Name: currentEventName, Done: true})
			currentEventName = ""
			continue
		}

		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			t.Fatalf("failed to unmarshal SSE payload %q: %v", data, err)
		}

		events = append(events, testSSEEvent{
			Name:    currentEventName,
			Payload: payload,
		})
		currentEventName = ""
	}

	return events
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
		{
			name: "request with function tools",
			input: &core.ChatRequest{
				Model: "claude-sonnet-4-5-20250929",
				Tools: []map[string]any{
					{
						"type": "function",
						"function": map[string]any{
							"name":        "lookup_weather",
							"description": "Get the weather for a city.",
							"parameters": map[string]any{
								"type": "object",
							},
						},
					},
				},
				ToolChoice: map[string]any{
					"type": "function",
					"function": map[string]any{
						"name": "lookup_weather",
					},
				},
				Messages: []core.Message{
					{Role: "user", Content: "What's the weather?"},
				},
			},
			checkFn: func(t *testing.T, req *anthropicRequest) {
				if len(req.Tools) != 1 {
					t.Fatalf("len(Tools) = %d, want 1", len(req.Tools))
				}
				if req.Tools[0].Name != "lookup_weather" {
					t.Fatalf("tool name = %q, want lookup_weather", req.Tools[0].Name)
				}
				if req.ToolChoice == nil || req.ToolChoice.Type != "tool" || req.ToolChoice.Name != "lookup_weather" {
					t.Fatalf("tool choice = %+v, want named tool choice", req.ToolChoice)
				}
			},
		},
		{
			name: "request disables parallel tool use",
			input: func() *core.ChatRequest {
				parallelToolCalls := false
				return &core.ChatRequest{
					Model: "claude-sonnet-4-5-20250929",
					Tools: []map[string]any{
						{
							"type": "function",
							"function": map[string]any{
								"name": "lookup_weather",
							},
						},
					},
					ParallelToolCalls: &parallelToolCalls,
					Messages: []core.Message{
						{Role: "user", Content: "What's the weather?"},
					},
				}
			}(),
			checkFn: func(t *testing.T, req *anthropicRequest) {
				if req.ToolChoice == nil {
					t.Fatal("ToolChoice should not be nil when parallel_tool_calls=false")
				}
				if req.ToolChoice.Type != "auto" {
					t.Fatalf("tool choice type = %q, want auto", req.ToolChoice.Type)
				}
				if req.ToolChoice.DisableParallelToolUse == nil || !*req.ToolChoice.DisableParallelToolUse {
					t.Fatalf("disable_parallel_tool_use = %#v, want true", req.ToolChoice.DisableParallelToolUse)
				}
			},
		},
		{
			name: "request with tool result messages",
			input: &core.ChatRequest{
				Model: "claude-sonnet-4-5-20250929",
				Messages: []core.Message{
					{
						Role: "assistant",
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
					{Role: "tool", ToolCallID: "call_123", Content: `{"temperature_c":21}`},
				},
			},
			checkFn: func(t *testing.T, req *anthropicRequest) {
				if len(req.Messages) != 2 {
					t.Fatalf("len(Messages) = %d, want 2", len(req.Messages))
				}

				assistantBlocks, ok := req.Messages[0].Content.([]anthropicContentBlock)
				if !ok || len(assistantBlocks) != 1 {
					t.Fatalf("assistant content = %#v, want one tool_use block", req.Messages[0].Content)
				}
				if assistantBlocks[0].Type != "tool_use" || assistantBlocks[0].Name != "lookup_weather" || assistantBlocks[0].ID != "call_123" {
					t.Fatalf("assistant tool block = %+v, want lookup_weather/call_123", assistantBlocks[0])
				}

				toolBlocks, ok := req.Messages[1].Content.([]anthropicContentBlock)
				if !ok || len(toolBlocks) != 1 {
					t.Fatalf("tool content = %#v, want one tool_result block", req.Messages[1].Content)
				}
				if req.Messages[1].Role != "user" {
					t.Fatalf("tool role = %q, want user", req.Messages[1].Role)
				}
				if toolBlocks[0].Type != "tool_result" || toolBlocks[0].ToolUseID != "call_123" || toolBlocks[0].Content != `{"temperature_c":21}` {
					t.Fatalf("tool result block = %+v, want call_123 payload", toolBlocks[0])
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

func TestConvertToAnthropicRequest_InvalidToolArguments(t *testing.T) {
	_, err := convertToAnthropicRequest(&core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{
				Role: "assistant",
				ToolCalls: []core.ToolCall{
					{
						ID:   "call_123",
						Type: "function",
						Function: core.FunctionCall{
							Name:      "lookup_weather",
							Arguments: `{"city":"Warsaw"`,
						},
					},
				},
			},
		},
	})
	if err == nil {
		t.Fatal("expected invalid request error, got nil")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("error type = %q, want invalid_request_error", gatewayErr.Type)
	}
	if gatewayErr.HTTPStatusCode() != http.StatusBadRequest {
		t.Fatalf("HTTPStatusCode() = %d, want %d", gatewayErr.HTTPStatusCode(), http.StatusBadRequest)
	}
}

func TestConvertToAnthropicRequest_RejectsTrailingToolArgumentContent(t *testing.T) {
	_, err := convertToAnthropicRequest(&core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{
				Role: "assistant",
				ToolCalls: []core.ToolCall{
					{
						ID:   "call_123",
						Type: "function",
						Function: core.FunctionCall{
							Name:      "lookup_weather",
							Arguments: `{"city":"Warsaw"} garbage`,
						},
					},
				},
			},
		},
	})
	if err == nil {
		t.Fatal("expected invalid request error, got nil")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("error type = %q, want invalid_request_error", gatewayErr.Type)
	}
	if gatewayErr.HTTPStatusCode() != http.StatusBadRequest {
		t.Fatalf("HTTPStatusCode() = %d, want %d", gatewayErr.HTTPStatusCode(), http.StatusBadRequest)
	}
	if !strings.Contains(gatewayErr.Message, "invalid character") && !strings.Contains(gatewayErr.Message, "exactly one JSON object") {
		t.Fatalf("error message = %q, want trailing content validation", gatewayErr.Message)
	}
}

func TestConvertToAnthropicRequest_InvalidToolDefinition(t *testing.T) {
	_, err := convertToAnthropicRequest(&core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
		Tools: []map[string]any{
			{
				"type": "function",
				"function": map[string]any{
					"name":       "lookup_weather",
					"parameters": []any{"invalid"},
				},
			},
		},
	})
	if err == nil {
		t.Fatal("expected invalid request error, got nil")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("error type = %q, want invalid_request_error", gatewayErr.Type)
	}
	if gatewayErr.HTTPStatusCode() != http.StatusBadRequest {
		t.Fatalf("HTTPStatusCode() = %d, want %d", gatewayErr.HTTPStatusCode(), http.StatusBadRequest)
	}
}

func TestConvertOpenAIToolsToAnthropic(t *testing.T) {
	tests := []struct {
		name      string
		tools     []map[string]any
		wantNil   bool
		wantLen   int
		checkFn   func(t *testing.T, tools []anthropicTool)
		wantError bool
	}{
		{
			name:    "nil tools returns nil",
			tools:   nil,
			wantNil: true,
		},
		{
			name:    "empty tools returns nil",
			tools:   []map[string]any{},
			wantNil: true,
		},
		{
			name: "valid function tool",
			tools: []map[string]any{
				{
					"type": "function",
					"function": map[string]any{
						"name":        "lookup_weather",
						"description": "Get weather for a city",
						"parameters": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"city": map[string]any{"type": "string"},
							},
						},
					},
				},
			},
			wantLen: 1,
			checkFn: func(t *testing.T, tools []anthropicTool) {
				if tools[0].Name != "lookup_weather" {
					t.Fatalf("Name = %q, want lookup_weather", tools[0].Name)
				}
				if tools[0].Description != "Get weather for a city" {
					t.Fatalf("Description = %q, want tool description", tools[0].Description)
				}
				if schemaType, _ := tools[0].InputSchema["type"].(string); schemaType != "object" {
					t.Fatalf("InputSchema.type = %q, want object", schemaType)
				}
			},
		},
		{
			name: "missing parameters uses default object schema",
			tools: []map[string]any{
				{
					"type": "function",
					"function": map[string]any{
						"name": "lookup_weather",
					},
				},
			},
			wantLen: 1,
			checkFn: func(t *testing.T, tools []anthropicTool) {
				if schemaType, _ := tools[0].InputSchema["type"].(string); schemaType != "object" {
					t.Fatalf("InputSchema.type = %q, want object", schemaType)
				}
				if _, ok := tools[0].InputSchema["properties"].(map[string]any); !ok {
					t.Fatalf("InputSchema.properties = %#v, want object map", tools[0].InputSchema["properties"])
				}
			},
		},
		{
			name: "unsupported tool type returns error",
			tools: []map[string]any{
				{
					"type": "web_search",
				},
			},
			wantError: true,
		},
		{
			name: "missing function object returns error",
			tools: []map[string]any{
				{
					"type": "function",
				},
			},
			wantError: true,
		},
		{
			name: "empty function name returns error",
			tools: []map[string]any{
				{
					"type": "function",
					"function": map[string]any{
						"name": "   ",
					},
				},
			},
			wantError: true,
		},
		{
			name: "non object parameters returns error",
			tools: []map[string]any{
				{
					"type": "function",
					"function": map[string]any{
						"name":       "lookup_weather",
						"parameters": []any{"invalid"},
					},
				},
			},
			wantError: true,
		},
		{
			name: "non object schema type returns error",
			tools: []map[string]any{
				{
					"type": "function",
					"function": map[string]any{
						"name": "lookup_weather",
						"parameters": map[string]any{
							"type": "array",
						},
					},
				},
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := convertOpenAIToolsToAnthropic(tt.tools)
			if tt.wantError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				var gatewayErr *core.GatewayError
				if !errors.As(err, &gatewayErr) {
					t.Fatalf("error = %T, want *core.GatewayError", err)
				}
				if gatewayErr.Type != core.ErrorTypeInvalidRequest {
					t.Fatalf("error type = %q, want invalid_request_error", gatewayErr.Type)
				}
				if gatewayErr.HTTPStatusCode() != http.StatusBadRequest {
					t.Fatalf("HTTPStatusCode() = %d, want %d", gatewayErr.HTTPStatusCode(), http.StatusBadRequest)
				}
				return
			}

			if err != nil {
				t.Fatalf("convertOpenAIToolsToAnthropic() error = %v, want nil", err)
			}
			if tt.wantNil {
				if result != nil {
					t.Fatalf("result = %#v, want nil", result)
				}
				return
			}
			if len(result) != tt.wantLen {
				t.Fatalf("len(result) = %d, want %d", len(result), tt.wantLen)
			}
			if tt.checkFn != nil {
				tt.checkFn(t, result)
			}
		})
	}
}

func TestConvertToAnthropicRequest_InvalidToolChoice(t *testing.T) {
	_, err := convertToAnthropicRequest(&core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
		Tools: []map[string]any{
			{
				"type": "function",
				"function": map[string]any{
					"name": "lookup_weather",
				},
			},
		},
		ToolChoice: map[string]any{
			"type":     "function",
			"function": map[string]any{},
		},
	})
	if err == nil {
		t.Fatal("expected invalid request error, got nil")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("error type = %q, want invalid_request_error", gatewayErr.Type)
	}
	if gatewayErr.HTTPStatusCode() != http.StatusBadRequest {
		t.Fatalf("HTTPStatusCode() = %d, want %d", gatewayErr.HTTPStatusCode(), http.StatusBadRequest)
	}
}

func TestConvertToAnthropicRequest_ToolMessageRequiresToolCallID(t *testing.T) {
	_, err := convertToAnthropicRequest(&core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{Role: "tool", Content: `{"temperature_c":21}`},
		},
	})
	if err == nil {
		t.Fatal("expected invalid request error, got nil")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("error type = %q, want invalid_request_error", gatewayErr.Type)
	}
	if gatewayErr.HTTPStatusCode() != http.StatusBadRequest {
		t.Fatalf("HTTPStatusCode() = %d, want %d", gatewayErr.HTTPStatusCode(), http.StatusBadRequest)
	}
}

func TestConvertToAnthropicRequest_ToolChoiceRequiresTools(t *testing.T) {
	_, err := convertToAnthropicRequest(&core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
		ToolChoice: "auto",
	})
	if err == nil {
		t.Fatal("expected invalid request error, got nil")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("error type = %q, want invalid_request_error", gatewayErr.Type)
	}
	if gatewayErr.HTTPStatusCode() != http.StatusBadRequest {
		t.Fatalf("HTTPStatusCode() = %d, want %d", gatewayErr.HTTPStatusCode(), http.StatusBadRequest)
	}
}

func TestConvertToAnthropicRequest_ToolArgumentsMustBeJSONObject(t *testing.T) {
	_, err := convertToAnthropicRequest(&core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{
				Role: "assistant",
				ToolCalls: []core.ToolCall{
					{
						ID:   "call_123",
						Type: "function",
						Function: core.FunctionCall{
							Name:      "lookup_weather",
							Arguments: `["Warsaw"]`,
						},
					},
				},
			},
		},
	})
	if err == nil {
		t.Fatal("expected invalid request error, got nil")
	}
	if !strings.Contains(err.Error(), "tool arguments must be a JSON object") {
		t.Fatalf("error = %v, want JSON object validation", err)
	}
}

func TestConvertToAnthropicRequest_NormalizesToolCallIDAndName(t *testing.T) {
	result, err := convertToAnthropicRequest(&core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{
				Role: "assistant",
				ToolCalls: []core.ToolCall{
					{
						ID:   "  ",
						Type: "function",
						Function: core.FunctionCall{
							Name:      "  lookup_weather  ",
							Arguments: `{"city":"Warsaw"}`,
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("convertToAnthropicRequest() error = %v, want nil", err)
	}

	blocks, ok := result.Messages[0].Content.([]anthropicContentBlock)
	if !ok || len(blocks) != 1 {
		t.Fatalf("content = %#v, want one tool_use block", result.Messages[0].Content)
	}
	if blocks[0].Name != "lookup_weather" {
		t.Fatalf("tool name = %q, want lookup_weather", blocks[0].Name)
	}
	if blocks[0].ID == "" {
		t.Fatal("tool id should not be empty")
	}
}

func TestConvertToAnthropicRequest_NormalizesToolResultID(t *testing.T) {
	result, err := convertToAnthropicRequest(&core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{Role: "tool", ToolCallID: "  call_123  ", Content: `{"temperature_c":21}`},
		},
	})
	if err != nil {
		t.Fatalf("convertToAnthropicRequest() error = %v, want nil", err)
	}

	blocks, ok := result.Messages[0].Content.([]anthropicContentBlock)
	if !ok || len(blocks) != 1 {
		t.Fatalf("content = %#v, want one tool_result block", result.Messages[0].Content)
	}
	if blocks[0].ToolUseID != "call_123" {
		t.Fatalf("ToolUseID = %q, want call_123", blocks[0].ToolUseID)
	}
}

func TestParseToolCallArguments_UsesJSONNumber(t *testing.T) {
	parsed, err := parseToolCallArguments(`{"value":9007199254740993}`)
	if err != nil {
		t.Fatalf("parseToolCallArguments() error = %v, want nil", err)
	}

	obj, ok := parsed.(map[string]any)
	if !ok {
		t.Fatalf("parsed = %T, want map[string]any", parsed)
	}
	num, ok := obj["value"].(json.Number)
	if !ok {
		t.Fatalf("value = %T, want json.Number", obj["value"])
	}
	if string(num) != "9007199254740993" {
		t.Fatalf("value = %q, want exact integer string", string(num))
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
	if result.Choices[0].FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want %q", result.Choices[0].FinishReason, "stop")
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

func TestConvertFromAnthropicResponse_WithToolUseStopReason(t *testing.T) {
	resp := &anthropicResponse{
		ID:    "msg_tool_use",
		Type:  "message",
		Role:  "assistant",
		Model: "claude-sonnet-4-5-20250929",
		Content: []anthropicContent{
			{
				Type:  "tool_use",
				ID:    "toolu_123",
				Name:  "lookup_weather",
				Input: json.RawMessage(`{"city":"Warsaw"}`),
			},
		},
		StopReason: "tool_use",
		Usage: anthropicUsage{
			InputTokens:  12,
			OutputTokens: 7,
		},
	}

	result := convertFromAnthropicResponse(resp)

	if len(result.Choices) != 1 {
		t.Fatalf("len(Choices) = %d, want 1", len(result.Choices))
	}
	if result.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("FinishReason = %q, want tool_calls", result.Choices[0].FinishReason)
	}
	if len(result.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("len(ToolCalls) = %d, want 1", len(result.Choices[0].Message.ToolCalls))
	}
	if result.Choices[0].Message.ToolCalls[0].ID != "toolu_123" {
		t.Fatalf("ToolCalls[0].ID = %q, want toolu_123", result.Choices[0].Message.ToolCalls[0].ID)
	}
	if result.Choices[0].Message.ToolCalls[0].Function.Name != "lookup_weather" {
		t.Fatalf("ToolCalls[0].Function.Name = %q, want lookup_weather", result.Choices[0].Message.ToolCalls[0].Function.Name)
	}
	if result.Choices[0].Message.ToolCalls[0].Function.Arguments != `{"city":"Warsaw"}` {
		t.Fatalf("ToolCalls[0].Function.Arguments = %q, want canonical JSON", result.Choices[0].Message.ToolCalls[0].Function.Arguments)
	}
}

func TestNormalizeAnthropicStopReason(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "tool use", in: "tool_use", want: "tool_calls"},
		{name: "end turn", in: "end_turn", want: "stop"},
		{name: "stop sequence", in: "stop_sequence", want: "stop"},
		{name: "max tokens", in: "max_tokens", want: "length"},
		{name: "context window exceeded", in: "model_context_window_exceeded", want: "length"},
		{name: "unknown", in: "pause_turn", want: "pause_turn"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeAnthropicStopReason(tt.in); got != tt.want {
				t.Fatalf("normalizeAnthropicStopReason(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
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

func TestStreamResponses_MergesUsageFromMessageStart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0,"cache_creation_input_tokens":4}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}
`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	body, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: "Hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	responseStr := string(raw)
	if !strings.Contains(responseStr, `"type":"response.completed"`) {
		t.Fatalf("expected response.completed event, got %q", responseStr)
	}
	if !strings.Contains(responseStr, `"input_tokens":10`) {
		t.Fatalf("expected input_tokens in response.completed usage, got %q", responseStr)
	}
	if !strings.Contains(responseStr, `"output_tokens":2`) {
		t.Fatalf("expected output_tokens in response.completed usage, got %q", responseStr)
	}
	if !strings.Contains(responseStr, `"total_tokens":12`) {
		t.Fatalf("expected total_tokens in response.completed usage, got %q", responseStr)
	}
	if !strings.Contains(responseStr, `"cache_creation_input_tokens":4`) {
		t.Fatalf("expected cache_creation_input_tokens in response.completed usage, got %q", responseStr)
	}
}

func TestStreamResponses_WithToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I'll check that for you."}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_123","name":"lookup_weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"War"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"saw\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":10,"output_tokens":4}}

event: message_stop
data: {"type":"message_stop"}
`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	body, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: "What's the weather?",
		Tools: []map[string]any{
			{
				"type": "function",
				"function": map[string]any{
					"name": "lookup_weather",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	events := parseTestSSEEvents(t, string(raw))
	foundAdded := false
	foundAssistantAdded := false
	foundAssistantDone := false
	foundTextDelta := false
	foundArgumentsDone := false
	foundItemDone := false
	var argumentsDelta strings.Builder

	for _, event := range events {
		if event.Done {
			continue
		}
		switch event.Name {
		case "response.output_item.added":
			item, _ := event.Payload["item"].(map[string]interface{})
			if item["type"] == "message" && item["role"] == "assistant" && event.Payload["output_index"] == float64(0) {
				foundAssistantAdded = true
			}
			if item["type"] == "function_call" && item["call_id"] == "toolu_123" && item["name"] == "lookup_weather" && item["arguments"] == "{}" && event.Payload["output_index"] == float64(1) {
				foundAdded = true
			}
		case "response.output_item.done":
			item, _ := event.Payload["item"].(map[string]interface{})
			if item["type"] == "message" && item["role"] == "assistant" && event.Payload["output_index"] == float64(0) {
				foundAssistantDone = true
			}
			if item["type"] == "function_call" && item["arguments"] == `{"city":"Warsaw"}` {
				foundItemDone = true
			}
		case "response.output_text.delta":
			if event.Payload["delta"] == "I'll check that for you." {
				foundTextDelta = true
			}
		case "response.function_call_arguments.delta":
			if delta, _ := event.Payload["delta"].(string); delta != "" {
				argumentsDelta.WriteString(delta)
			}
		case "response.function_call_arguments.done":
			if event.Payload["arguments"] == `{"city":"Warsaw"}` {
				foundArgumentsDone = true
			}
		}
	}

	if !foundAdded {
		t.Fatal("expected response.output_item.added for function_call")
	}
	if !foundAssistantAdded {
		t.Fatal("expected assistant message response.output_item.added at output_index 0")
	}
	if !foundAssistantDone {
		t.Fatal("expected assistant message response.output_item.done at output_index 0")
	}
	if !foundTextDelta {
		t.Fatal("expected response.output_text.delta for assistant preamble")
	}
	if argumentsDelta.String() != `{"city":"Warsaw"}` {
		t.Fatalf("streamed response.function_call_arguments.delta = %q, want %q", argumentsDelta.String(), `{"city":"Warsaw"}`)
	}
	if !foundArgumentsDone {
		t.Fatal("expected response.function_call_arguments.done for function_call")
	}
	if !foundItemDone {
		t.Fatal("expected response.output_item.done for function_call")
	}
}

func TestStreamResponses_WithEmptyToolArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_123","name":"lookup_weather","input":{}}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":10,"output_tokens":4}}

event: message_stop
data: {"type":"message_stop"}
`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	body, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: "What's the weather?",
		Tools: []map[string]any{
			{
				"type": "function",
				"function": map[string]any{
					"name": "lookup_weather",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	events := parseTestSSEEvents(t, string(raw))
	foundAdded := false
	foundDone := false

	for _, event := range events {
		if event.Done {
			continue
		}
		switch event.Name {
		case "response.output_item.added":
			item, _ := event.Payload["item"].(map[string]interface{})
			if item["type"] == "function_call" && item["arguments"] == "{}" {
				foundAdded = true
			}
		case "response.function_call_arguments.done":
			if event.Payload["arguments"] == "{}" {
				foundDone = true
			}
		}
	}

	if !foundAdded {
		t.Fatal("expected response.output_item.added with {} arguments")
	}
	if !foundDone {
		t.Fatal("expected response.function_call_arguments.done with {} arguments")
	}
}

func TestStreamResponses_MalformedEventReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"broken"}
`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	body, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: "Hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	if err == nil {
		t.Fatal("expected malformed stream error")
	}

	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("expected GatewayError, got %T", err)
	}
	if gatewayErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", gatewayErr.StatusCode, http.StatusBadGateway)
	}
	if !strings.Contains(gatewayErr.Message, "failed to decode anthropic stream event") {
		t.Fatalf("message = %q, want decode failure", gatewayErr.Message)
	}
	if !strings.Contains(string(raw), "response.created") {
		t.Fatalf("expected stream to include prior response.created event, got %q", string(raw))
	}
	if strings.Contains(string(raw), "[DONE]") {
		t.Fatalf("did not expect [DONE] after malformed event, got %q", string(raw))
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
		{
			name: "with tools and parallel tool calls disabled",
			input: func() *core.ResponsesRequest {
				parallelToolCalls := false
				return &core.ResponsesRequest{
					Model: "claude-sonnet-4-5-20250929",
					Input: "Hello",
					Tools: []map[string]any{
						{
							"type": "function",
							"function": map[string]any{
								"name": "lookup_weather",
							},
						},
					},
					ToolChoice:        "auto",
					ParallelToolCalls: &parallelToolCalls,
				}
			}(),
			checkFn: func(t *testing.T, req *anthropicRequest) {
				if len(req.Tools) != 1 {
					t.Fatalf("len(Tools) = %d, want 1", len(req.Tools))
				}
				if req.ToolChoice == nil {
					t.Fatal("ToolChoice should not be nil")
				}
				if req.ToolChoice.DisableParallelToolUse == nil || !*req.ToolChoice.DisableParallelToolUse {
					t.Fatalf("disable_parallel_tool_use = %#v, want true", req.ToolChoice.DisableParallelToolUse)
				}
			},
		},
		{
			name: "with function call loop input items",
			input: &core.ResponsesRequest{
				Model: "claude-sonnet-4-5-20250929",
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
			checkFn: func(t *testing.T, req *anthropicRequest) {
				if len(req.Messages) != 2 {
					t.Fatalf("len(Messages) = %d, want 2", len(req.Messages))
				}

				assistantBlocks, ok := req.Messages[0].Content.([]anthropicContentBlock)
				if !ok || len(assistantBlocks) != 1 {
					t.Fatalf("assistant content = %#v, want one tool_use block", req.Messages[0].Content)
				}
				if assistantBlocks[0].Type != "tool_use" || assistantBlocks[0].ID != "call_123" || assistantBlocks[0].Name != "lookup_weather" {
					t.Fatalf("assistant tool block = %+v, want lookup_weather/call_123", assistantBlocks[0])
				}

				toolBlocks, ok := req.Messages[1].Content.([]anthropicContentBlock)
				if !ok || len(toolBlocks) != 1 {
					t.Fatalf("tool content = %#v, want one tool_result block", req.Messages[1].Content)
				}
				if req.Messages[1].Role != "user" {
					t.Fatalf("tool role = %q, want user", req.Messages[1].Role)
				}
				if toolBlocks[0].Type != "tool_result" || toolBlocks[0].ToolUseID != "call_123" || toolBlocks[0].Content != `{"temperature_c":21}` {
					t.Fatalf("tool result block = %+v, want call_123 payload", toolBlocks[0])
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

func TestConvertResponsesRequestToAnthropic_InvalidToolArguments(t *testing.T) {
	_, err := convertResponsesRequestToAnthropic(&core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: []interface{}{
			map[string]interface{}{
				"type":      "function_call",
				"call_id":   "call_123",
				"name":      "lookup_weather",
				"arguments": `{"city":"Warsaw"`,
			},
		},
	})
	if err == nil {
		t.Fatal("expected invalid request error, got nil")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("error type = %q, want invalid_request_error", gatewayErr.Type)
	}
	if gatewayErr.HTTPStatusCode() != http.StatusBadRequest {
		t.Fatalf("HTTPStatusCode() = %d, want %d", gatewayErr.HTTPStatusCode(), http.StatusBadRequest)
	}
}

func TestConvertResponsesRequestToAnthropic_RejectsTrailingToolArgumentContent(t *testing.T) {
	_, err := convertResponsesRequestToAnthropic(&core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: []interface{}{
			map[string]interface{}{
				"type":      "function_call",
				"call_id":   "call_123",
				"name":      "lookup_weather",
				"arguments": `{"city":"Warsaw"} garbage`,
			},
		},
	})
	if err == nil {
		t.Fatal("expected invalid request error, got nil")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("error type = %q, want invalid_request_error", gatewayErr.Type)
	}
	if gatewayErr.HTTPStatusCode() != http.StatusBadRequest {
		t.Fatalf("HTTPStatusCode() = %d, want %d", gatewayErr.HTTPStatusCode(), http.StatusBadRequest)
	}
}

func TestConvertResponsesRequestToAnthropic_ToolChoiceRequiresTools(t *testing.T) {
	_, err := convertResponsesRequestToAnthropic(&core.ResponsesRequest{
		Model:      "claude-sonnet-4-5-20250929",
		Input:      "Hello",
		ToolChoice: "auto",
	})
	if err == nil {
		t.Fatal("expected invalid request error, got nil")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("error type = %q, want invalid_request_error", gatewayErr.Type)
	}
	if gatewayErr.HTTPStatusCode() != http.StatusBadRequest {
		t.Fatalf("HTTPStatusCode() = %d, want %d", gatewayErr.HTTPStatusCode(), http.StatusBadRequest)
	}
}

func TestBuildAnthropicBatchCreateRequest_PreservesGatewayErrorDetails(t *testing.T) {
	req := &core.BatchRequest{
		Requests: []core.BatchRequestItem{
			{
				URL: "/v1/chat/completions",
				Body: json.RawMessage(`{
					"model":"claude-sonnet-4-5-20250929",
					"messages":[{"role":"user","content":"Hello"}],
					"tool_choice":"auto"
				}`),
			},
		},
	}

	_, _, err := buildAnthropicBatchCreateRequest(req)
	if err == nil {
		t.Fatal("expected invalid request error, got nil")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("error type = %q, want invalid_request_error", gatewayErr.Type)
	}
	if gatewayErr.Message != "batch item 0: tool_choice requires at least one tool" {
		t.Fatalf("error message = %q", gatewayErr.Message)
	}
}

func TestBuildAnthropicBatchCreateRequest_PrefixesToolArgumentErrors(t *testing.T) {
	req := &core.BatchRequest{
		Requests: []core.BatchRequestItem{
			{
				URL: "/v1/chat/completions",
				Body: json.RawMessage(`{
					"model":"claude-sonnet-4-5-20250929",
					"messages":[{
						"role":"assistant",
						"tool_calls":[{
							"id":"call_123",
							"type":"function",
							"function":{
								"name":"lookup_weather",
								"arguments":"{\"city\":\"Warsaw\"} garbage"
							}
						}]
					}]
				}`),
			},
		},
	}

	_, _, err := buildAnthropicBatchCreateRequest(req)
	if err == nil {
		t.Fatal("expected invalid request error, got nil")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %T, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("error type = %q, want invalid_request_error", gatewayErr.Type)
	}
	if !strings.HasPrefix(gatewayErr.Message, "batch item 0: ") {
		t.Fatalf("error message = %q, want batch item prefix", gatewayErr.Message)
	}
}

func TestBuildAnthropicBatchCreateRequest_NormalizesFullURLResponsesEndpoint(t *testing.T) {
	req := &core.BatchRequest{
		Requests: []core.BatchRequestItem{
			{
				CustomID: "resp-1",
				Method:   http.MethodPost,
				URL:      "https://provider.example/v1/responses/?trace=1",
				Body: json.RawMessage(`{
					"model":"claude-sonnet-4-5-20250929",
					"input":"Hello"
				}`),
			},
		},
	}

	anthropicReq, endpointByCustomID, err := buildAnthropicBatchCreateRequest(req)
	if err != nil {
		t.Fatalf("buildAnthropicBatchCreateRequest() error = %v", err)
	}
	if anthropicReq == nil {
		t.Fatal("anthropicReq = nil")
	}
	if len(anthropicReq.Requests) != 1 {
		t.Fatalf("len(Requests) = %d, want 1", len(anthropicReq.Requests))
	}
	if anthropicReq.Requests[0].Params.Stream {
		t.Fatal("Params.Stream = true, want false")
	}
	if got := endpointByCustomID["resp-1"]; got != "/v1/responses" {
		t.Fatalf("endpointByCustomID[resp-1] = %q, want /v1/responses", got)
	}
}

func TestConvertDecodedBatchItemToAnthropic_ResponsesUsesSharedSemanticTranslator(t *testing.T) {
	decoded := &core.DecodedBatchItemRequest{
		Endpoint:  "/v1/responses",
		Operation: "responses",
		Request: &core.ResponsesRequest{
			Model:        "claude-sonnet-4-5-20250929",
			Instructions: "Be helpful",
			Input: []core.ResponsesInputElement{
				{
					Role:    "user",
					Content: "Hello",
				},
			},
		},
	}

	result, err := convertDecodedBatchItemToAnthropic(decoded)
	if err != nil {
		t.Fatalf("convertDecodedBatchItemToAnthropic() error = %v", err)
	}
	if result.System != "Be helpful" {
		t.Fatalf("System = %q, want Be helpful", result.System)
	}
	if result.Stream {
		t.Fatal("Stream = true, want false")
	}
	if len(result.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(result.Messages))
	}
	if result.Messages[0].Role != "user" {
		t.Fatalf("Messages[0].Role = %q, want user", result.Messages[0].Role)
	}
	if result.Messages[0].Content != "Hello" {
		t.Fatalf("Messages[0].Content = %#v, want Hello", result.Messages[0].Content)
	}
}

func TestConvertDecodedBatchItemToAnthropic_RejectsStreaming(t *testing.T) {
	decoded := &core.DecodedBatchItemRequest{
		Endpoint:  "/v1/chat/completions",
		Operation: "chat_completions",
		Request: &core.ChatRequest{
			Model:  "claude-sonnet-4-5-20250929",
			Stream: true,
			Messages: []core.Message{
				{
					Role:    "user",
					Content: "Hello",
				},
			},
		},
	}

	_, err := convertDecodedBatchItemToAnthropic(decoded)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "streaming is not supported for native batch") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConvertDecodedBatchItemToAnthropic_RejectsEmbeddings(t *testing.T) {
	decoded := &core.DecodedBatchItemRequest{
		Endpoint:  "/v1/embeddings",
		Operation: "embeddings",
		Request: &core.EmbeddingRequest{
			Model: "text-embedding-3-small",
			Input: "Hello",
		},
	}

	_, err := convertDecodedBatchItemToAnthropic(decoded)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "anthropic does not support native embedding batches") {
		t.Fatalf("unexpected error: %v", err)
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

func TestConvertAnthropicResponseToResponses_WithToolUse(t *testing.T) {
	resp := &anthropicResponse{
		ID:    "msg_123",
		Type:  "message",
		Role:  "assistant",
		Model: "claude-sonnet-4-5-20250929",
		Content: []anthropicContent{
			{Type: "text", Text: "I'll check that for you."},
			{
				Type:  "tool_use",
				ID:    "toolu_123",
				Name:  "lookup_weather",
				Input: json.RawMessage(`{"city":"Warsaw"}`),
			},
		},
		StopReason: "tool_use",
		Usage: anthropicUsage{
			InputTokens:  10,
			OutputTokens: 20,
		},
	}

	result := convertAnthropicResponseToResponses(resp, "claude-sonnet-4-5-20250929")

	if len(result.Output) != 2 {
		t.Fatalf("len(Output) = %d, want 2", len(result.Output))
	}
	if result.Output[0].Type != "message" {
		t.Fatalf("Output[0].Type = %q, want message", result.Output[0].Type)
	}
	if result.Output[0].Content[0].Text != "I'll check that for you." {
		t.Fatalf("Output[0].Content[0].Text = %q, want tool preamble", result.Output[0].Content[0].Text)
	}
	if result.Output[1].Type != "function_call" {
		t.Fatalf("Output[1].Type = %q, want function_call", result.Output[1].Type)
	}
	if result.Output[1].CallID != "toolu_123" {
		t.Fatalf("Output[1].CallID = %q, want toolu_123", result.Output[1].CallID)
	}
	if result.Output[1].Name != "lookup_weather" {
		t.Fatalf("Output[1].Name = %q, want lookup_weather", result.Output[1].Name)
	}
	if result.Output[1].Arguments != `{"city":"Warsaw"}` {
		t.Fatalf("Output[1].Arguments = %q, want canonical JSON", result.Output[1].Arguments)
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

func TestConvertToAnthropicRequest_PreservesAllSystemMessages(t *testing.T) {
	req := &core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{Role: "system", Content: "first system"},
			{Role: "system", Content: "second system"},
			{Role: "user", Content: "hello"},
		},
	}

	result, err := convertToAnthropicRequest(req)
	if err != nil {
		t.Fatalf("convertToAnthropicRequest() error = %v", err)
	}
	if result.System != "first system\n\nsecond system" {
		t.Fatalf("System = %q, want merged system text", result.System)
	}
}

func TestConvertToAnthropicRequest_RejectsNilRequest(t *testing.T) {
	_, err := convertToAnthropicRequest(nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "anthropic chat request is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConvertToAnthropicRequest_MultimodalImageContent_DataURLWithExtraMetadata(t *testing.T) {
	req := &core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{
				Role: "user",
				Content: []core.ContentPart{
					{
						Type: "image_url",
						ImageURL: &core.ImageURLContent{
							URL: "data:image/png;charset=utf-8;BASE64,ZmFrZQ==",
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
	if blocks[0].Source.Type != "base64" || blocks[0].Source.MediaType != "image/png" || blocks[0].Source.Data != "ZmFrZQ==" {
		t.Fatalf("unexpected image source: %+v", blocks[0].Source)
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

func TestConvertToAnthropicRequest_RejectsInvalidRemoteImageURLs(t *testing.T) {
	tests := []string{
		"https:",
		"https://",
		"/relative/path.png",
	}

	for _, rawURL := range tests {
		t.Run(rawURL, func(t *testing.T) {
			req := &core.ChatRequest{
				Model: "claude-sonnet-4-5-20250929",
				Messages: []core.Message{
					{
						Role: "user",
						Content: []core.ContentPart{
							{
								Type: "image_url",
								ImageURL: &core.ImageURLContent{
									URL: rawURL,
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
			if !strings.Contains(err.Error(), "anthropic chat image_url must be a data: URL or http/https URL") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
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

func TestConvertResponsesRequestToAnthropic_TrimsRoleBeforeAppend(t *testing.T) {
	req, err := convertResponsesRequestToAnthropic(&core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: []interface{}{
			map[string]interface{}{
				"role":    "  user  ",
				"content": "hello",
			},
		},
	})
	if err != nil {
		t.Fatalf("convertResponsesRequestToAnthropic() error = %v", err)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(req.Messages))
	}
	if req.Messages[0].Role != "user" {
		t.Fatalf("Messages[0].Role = %q, want user", req.Messages[0].Role)
	}
}

func TestConvertResponsesRequestToAnthropic_PreservesAllSystemMessages(t *testing.T) {
	req, err := convertResponsesRequestToAnthropic(&core.ResponsesRequest{
		Model:        "claude-sonnet-4-5-20250929",
		Instructions: "instruction system",
		Input: []core.ResponsesInputElement{
			{
				Role:    "system",
				Content: "input system",
			},
			{
				Role:    "user",
				Content: "hello",
			},
		},
	})
	if err != nil {
		t.Fatalf("convertResponsesRequestToAnthropic() error = %v", err)
	}
	if req.System != "instruction system\n\ninput system" {
		t.Fatalf("System = %q, want merged system text", req.System)
	}
}

func TestConvertResponsesRequestToAnthropic_RejectsNilRequest(t *testing.T) {
	_, err := convertResponsesRequestToAnthropic(nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "anthropic responses request is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConvertResponsesRequestToAnthropic_TypedInputPromotesSystemRole(t *testing.T) {
	req, err := convertResponsesRequestToAnthropic(&core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: []core.ResponsesInputElement{
			{
				Role:    "system",
				Content: "be concise",
			},
			{
				Role: " user ",
				Content: []core.ContentPart{
					{Type: "input_text", Text: "hello"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("convertResponsesRequestToAnthropic() error = %v", err)
	}
	if req.System != "be concise" {
		t.Fatalf("System = %q, want be concise", req.System)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(req.Messages))
	}
	if req.Messages[0].Role != "user" {
		t.Fatalf("Messages[0].Role = %q, want user", req.Messages[0].Role)
	}
	if req.Messages[0].Content != "hello" {
		t.Fatalf("Messages[0].Content = %#v, want hello", req.Messages[0].Content)
	}
}

func TestConvertResponsesRequestToAnthropic_PreservesMultimodalImageInput(t *testing.T) {
	req, err := convertResponsesRequestToAnthropic(&core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
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
							"url": "data:image/png;base64,ZmFrZQ==",
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("convertResponsesRequestToAnthropic() error = %v", err)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("len(Messages) = %d, want 1", len(req.Messages))
	}

	blocks, ok := req.Messages[0].Content.([]anthropicContentBlock)
	if !ok {
		t.Fatalf("Messages[0].Content = %#v, want []anthropicContentBlock", req.Messages[0].Content)
	}
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2", len(blocks))
	}
	if blocks[0].Type != "text" || blocks[0].Text != "Describe the image." {
		t.Fatalf("unexpected text block: %+v", blocks[0])
	}
	if blocks[1].Type != "image" || blocks[1].Source == nil {
		t.Fatalf("unexpected image block: %+v", blocks[1])
	}
	if blocks[1].Source.Type != "base64" || blocks[1].Source.MediaType != "image/png" || blocks[1].Source.Data != "ZmFrZQ==" {
		t.Fatalf("unexpected image source: %+v", blocks[1].Source)
	}
}

func TestConvertResponsesRequestToAnthropic_ToolRoleRequiresToolCallID(t *testing.T) {
	_, err := convertResponsesRequestToAnthropic(&core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: []core.ResponsesInputElement{
			{
				Role:    "tool",
				Content: "hello",
			},
		},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "tool message is missing tool_call_id") {
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

func TestConvertToAnthropicRequest_NormalizesInputTextType(t *testing.T) {
	req := &core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{
				Role: "user",
				Content: []core.ContentPart{
					{Type: "input_text", Text: "First part."},
					{Type: "input_text", Text: "Second part."},
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
	for i, block := range blocks {
		if block.Type != "text" {
			t.Errorf("blocks[%d].Type = %q, want \"text\"", i, block.Type)
		}
	}
	if blocks[0].Text != "First part." {
		t.Errorf("blocks[0].Text = %q, want \"First part.\"", blocks[0].Text)
	}
	if blocks[1].Text != "Second part." {
		t.Errorf("blocks[1].Text = %q, want \"Second part.\"", blocks[1].Text)
	}
}

func intPtr(i int) *int {
	return &i
}

func float64Ptr(f float64) *float64 {
	return &f
}

func TestPassthrough(t *testing.T) {
	var gotPath string
	var gotAPIKey string
	var gotVersion string
	var gotBody string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "messages",
		Body:     io.NopCloser(strings.NewReader(`{"model":"claude-sonnet-4-5"}`)),
		Headers: http.Header{
			"Content-Type":      {"application/json"},
			"anthropic-version": {"2024-10-22"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if gotPath != "/messages" {
		t.Fatalf("path = %q, want /messages", gotPath)
	}
	if gotAPIKey != "test-api-key" {
		t.Fatalf("x-api-key = %q", gotAPIKey)
	}
	if gotVersion != "2024-10-22" {
		t.Fatalf("anthropic-version = %q", gotVersion)
	}
	if gotBody != `{"model":"claude-sonnet-4-5"}` {
		t.Fatalf("body = %q", gotBody)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}
	if string(body) != `{"error":{"message":"bad request"}}` {
		t.Fatalf("response body = %q", string(body))
	}
}
