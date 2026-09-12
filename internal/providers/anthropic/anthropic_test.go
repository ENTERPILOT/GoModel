package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/anthropicapi"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
		require.NoError(t, err)
	}

	require.Equal(t, "data: [DONE]\n\n", out.String())
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
	got := provider.getBatchResultEndpoints("batch-0")
	require.NotNil(t, got)
	got = provider.getBatchResultEndpoints("batch-1")
	require.NotNil(t, got)
	got = provider.getBatchResultEndpoints("batch-1024")
	require.NotNil(t, got)
	require.Len(t, provider.batchResultEndpoints, 1025)
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
	require.NotNil(t, refreshed)
	assert.Equal(t, "/v1/responses", refreshed["req-1"])
	assert.Len(t, provider.batchResultEndpoints, 1)
}

func TestGetBatchResults(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK,
		`{"custom_id":"ok-1","result":{"type":"succeeded","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}}}`+"\n"+
			`{"custom_id":"err-1","result":{"type":"errored","error":{"type":"invalid_request_error","message":"bad request"}}}`,
	)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)
	provider.setBatchResultEndpoints("batch_1", map[string]string{
		"ok-1":  "/v1/responses",
		"err-1": "/v1/chat/completions",
	})

	resp, err := provider.GetBatchResults(context.Background(), "batch_1")
	require.NoError(t, err)
	assert.Equal(t, "/messages/batches/batch_1/results", capture.Last(t).Path)
	require.Equal(t, "batch_1", resp.BatchID)
	require.Len(t, resp.Data, 2)
	require.Equal(t, "/v1/responses", resp.Data[0].URL)
	require.Equal(t, http.StatusOK, resp.Data[0].StatusCode, "unexpected first row: %+v", resp.Data[0])
	require.NotNil(t, resp.Data[1].Error)
	require.Equal(t, "bad request", resp.Data[1].Error.Message, "unexpected error row: %+v", resp.Data[1])
}

func TestGetBatchResultsWithHints(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK,
		`{"custom_id":"ok-1","result":{"type":"succeeded","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}}}`,
	)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.GetBatchResultsWithHints(context.Background(), "batch_1", map[string]string{
		"ok-1": "/v1/responses",
	})
	require.NoError(t, err)
	assert.Equal(t, "/messages/batches/batch_1/results", capture.Last(t).Path)
	require.Len(t, resp.Data, 1)
	require.Equal(t, "/v1/responses", resp.Data[0].URL)
}

func TestGetBatchResultsWithHints_ExplicitEmptyHintsDoNotUseTransientHints(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK,
		`{"custom_id":"ok-1","result":{"type":"succeeded","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}}}`,
	)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)
	provider.setBatchResultEndpoints("batch_1", map[string]string{
		"ok-1": "/v1/responses",
	})

	resp, err := provider.GetBatchResultsWithHints(context.Background(), "batch_1", map[string]string{})
	require.NoError(t, err)
	assert.Equal(t, "/messages/batches/batch_1/results", capture.Last(t).Path)
	require.Len(t, resp.Data, 1)
	require.Equal(t, "/v1/chat/completions", resp.Data[0].URL)
}

func TestClearBatchResultHints(t *testing.T) {
	provider := &Provider{
		batchResultEndpoints: map[string]map[string]string{
			"batch_1": {
				"resp-1": "/v1/responses",
			},
		},
	}

	provider.ClearBatchResultHints("batch_1")
	got := provider.getBatchResultEndpoints("batch_1")
	require.Nil(t, got)
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
				assert.Equal(t, "msg_123", resp.ID)
				assert.Equal(t, "claude-sonnet-4-5-20250929", resp.Model)
				require.Len(t, resp.Choices, 1)
				assert.Equal(t, "Hello! How can I help you today?", resp.Choices[0].Message.Content)
				assert.Equal(t, 10, resp.Usage.PromptTokens)
				assert.Equal(t, 20, resp.Usage.CompletionTokens)
				assert.Equal(t, 30, resp.Usage.TotalTokens)
			},
		},
		{
			name:       "stop sequence hit carries the matched sequence",
			statusCode: http.StatusOK,
			responseBody: `{
				"id": "msg_stop",
				"type": "message",
				"role": "assistant",
				"model": "claude-sonnet-4-5-20250929",
				"content": [{"type": "text", "text": "1 2 3 "}],
				"stop_reason": "stop_sequence",
				"stop_sequence": "7",
				"usage": {"input_tokens": 6, "output_tokens": 4}
			}`,
			expectedError: false,
			checkResponse: func(t *testing.T, resp *core.ChatResponse) {
				choice := resp.Choices[0]
				assert.Equal(t, "stop", choice.FinishReason)
				assert.Equal(t, "7", choice.StopSequence)
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
			server, capture := providertest.JSONServer(t, tt.statusCode, tt.responseBody)

			provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			req := &core.ChatRequest{
				Model: "claude-sonnet-4-5-20250929",
				Messages: []core.Message{
					{Role: "user", Content: "Hello"},
				},
			}

			resp, err := provider.ChatCompletion(context.Background(), req)

			sent := capture.Last(t)
			assert.Equal(t, "/messages", sent.Path)
			assert.Equal(t, "application/json", sent.Header.Get("Content-Type"))
			assert.NotEmpty(t, sent.Header.Get("x-api-key"))
			assert.Equal(t, anthropicAPIVersion, sent.Header.Get("anthropic-version"))
			var anthropicReq anthropicRequest
			require.NoError(t, json.Unmarshal(sent.Body, &anthropicReq))

			if tt.expectedError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.checkResponse != nil {
				tt.checkResponse(t, resp)
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
				require.NotNil(t, body)

				defer func() { _ = body.Close() }()

				// Read and verify the streaming response
				respBody, err := io.ReadAll(body)
				require.NoError(t, err)

				// The response should be converted to OpenAI format
				responseStr := string(respBody)
				assert.Contains(t, responseStr, "data:")
				assert.Contains(t, responseStr, `"role":"assistant"`)
				assert.Contains(t, responseStr, "[DONE]")
			},
		},
		{
			name:       "stop sequence hit rides the chunk delta",
			statusCode: http.StatusOK,
			responseBody: `event: message_start
data: {"type":"message_start","message":{"id":"msg_stop","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":6,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"1 2 3 "}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"stop_sequence","stop_sequence":"7"},"usage":{"output_tokens":4}}

event: message_stop
data: {"type":"message_stop"}
`,
			expectedError: false,
			checkStream: func(t *testing.T, body io.ReadCloser) {
				defer func() { _ = body.Close() }()
				respBody, err := io.ReadAll(body)
				require.NoError(t, err)

				responseStr := string(respBody)
				assert.Contains(t, responseStr, `"stop_sequence":"7"`)
				assert.Contains(t, responseStr, `"finish_reason":"stop"`)
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
			server, capture := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = io.WriteString(w, tt.responseBody)
			})

			provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			req := &core.ChatRequest{
				Model: "claude-sonnet-4-5-20250929",
				Messages: []core.Message{
					{Role: "user", Content: "Hello"},
				},
			}

			body, err := provider.StreamChatCompletion(context.Background(), req)

			sent := capture.Last(t)
			assert.Equal(t, "application/json", sent.Header.Get("Content-Type"))
			assert.NotEmpty(t, sent.Header.Get("x-api-key"))
			var anthropicReq anthropicRequest
			require.NoError(t, json.Unmarshal(sent.Body, &anthropicReq))
			assert.True(t, anthropicReq.Stream)

			if tt.expectedError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.checkStream != nil {
				tt.checkStream(t, body)
			}
		})
	}
}

func TestStreamChatCompletion_MergesUsageFromMessageStart(t *testing.T) {
	server, _ := providertest.SSEServer(t, `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0,"cache_read_input_tokens":6}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}
`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
	})
	require.NoError(t, err)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)

	responseStr := string(raw)
	require.Contains(t, responseStr, `"prompt_tokens":10`)
	require.Contains(t, responseStr, `"completion_tokens":2`)
	require.Contains(t, responseStr, `"total_tokens":12`)
	require.Contains(t, responseStr, `"cache_read_input_tokens":6`)
}

func TestStreamChatCompletion_WithToolCalls(t *testing.T) {
	server, _ := providertest.SSEServer(t, `event: message_start
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
`)

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
	require.NoError(t, err)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)

	events := parseTestSSEEvents(t, string(raw))
	foundToolStart := false
	foundFinish := false
	var argumentDeltas strings.Builder

	for _, event := range events {
		if event.Done {
			continue
		}

		choices, ok := event.Payload["choices"].([]any)
		if !ok || len(choices) == 0 {
			continue
		}
		choice, ok := choices[0].(map[string]any)
		if !ok {
			continue
		}

		if finishReason, _ := choice["finish_reason"].(string); finishReason == "tool_calls" {
			foundFinish = true
		}

		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			continue
		}
		toolCalls, ok := delta["tool_calls"].([]any)
		if !ok || len(toolCalls) == 0 {
			continue
		}
		toolCall, ok := toolCalls[0].(map[string]any)
		if !ok {
			continue
		}
		function, _ := toolCall["function"].(map[string]any)

		if toolCall["id"] == "toolu_123" && function["name"] == "lookup_weather" {
			foundToolStart = true
		}
		if arguments, _ := function["arguments"].(string); arguments != "" {
			argumentDeltas.WriteString(arguments)
		}
	}

	require.True(t, foundToolStart)
	require.Equal(t, `{"city":"Warsaw"}`, argumentDeltas.String())
	require.True(t, foundFinish)
}

func TestStreamChatCompletion_WithEmptyToolArguments(t *testing.T) {
	server, _ := providertest.SSEServer(t, `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_123","name":"lookup_weather","input":{}}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":10,"output_tokens":4}}

event: message_stop
data: {"type":"message_stop"}
`)

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
	require.NoError(t, err)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)

	events := parseTestSSEEvents(t, string(raw))
	foundToolCall := false
	foundFinish := false

	for _, event := range events {
		if event.Done {
			continue
		}
		choices, ok := event.Payload["choices"].([]any)
		if !ok || len(choices) == 0 {
			continue
		}
		choice, ok := choices[0].(map[string]any)
		if !ok {
			continue
		}
		if finishReason, _ := choice["finish_reason"].(string); finishReason == "tool_calls" {
			foundFinish = true
		}
		delta, _ := choice["delta"].(map[string]any)
		toolCalls, _ := delta["tool_calls"].([]any)
		if len(toolCalls) == 0 {
			continue
		}
		toolCall, _ := toolCalls[0].(map[string]any)
		function, _ := toolCall["function"].(map[string]any)
		if toolCall["id"] == "toolu_123" && function["name"] == "lookup_weather" && function["arguments"] == "{}" {
			foundToolCall = true
		}
	}

	require.True(t, foundToolCall)
	require.True(t, foundFinish)
}

func TestStreamChatCompletion_ToolUseWithoutToolChunksKeepsRawFinishReason(t *testing.T) {
	server, _ := providertest.SSEServer(t, `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}
`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{Role: "user", Content: "call tool"},
		},
	})
	require.NoError(t, err)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)

	events := parseTestSSEEvents(t, string(raw))
	require.NotEmpty(t, events)

	foundTerminalChunk := false
	for _, event := range events {
		if event.Done {
			continue
		}

		choices, ok := event.Payload["choices"].([]any)
		if !ok || len(choices) == 0 {
			continue
		}
		choice, ok := choices[0].(map[string]any)
		if !ok {
			continue
		}

		delta, _ := choice["delta"].(map[string]any)
		_, ok = delta["tool_calls"]
		require.False(t, ok, "did not expect tool_calls in malformed stream fallback, got %#v", delta["tool_calls"])

		if choice["finish_reason"] == nil {
			continue
		}
		foundTerminalChunk = true
		require.Equal(t, "tool_use", choice["finish_reason"])
	}

	require.True(t, foundTerminalChunk)
}

func TestStreamChatCompletion_MalformedEventReturnsError(t *testing.T) {
	server, _ := providertest.SSEServer(t, `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"broken"}
`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
	})
	require.NoError(t, err)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, http.StatusBadGateway, gatewayErr.StatusCode)
	require.Contains(t, gatewayErr.Message, "failed to decode anthropic stream event")
	require.Contains(t, string(raw), `"content":"Hello"`)
	require.NotContains(t, string(raw), "[DONE]", "no [DONE] after a malformed event")
}

type testSSEEvent struct {
	Name    string
	Payload map[string]any
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
		if after, ok := strings.CutPrefix(line, "event:"); ok {
			currentEventName = strings.TrimSpace(after)
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

		var payload map[string]any
		err := json.Unmarshal([]byte(data), &payload)
		require.NoError(t, err, "failed to unmarshal SSE payload %q: %v", data, err)

		events = append(events, testSSEEvent{
			Name:    currentEventName,
			Payload: payload,
		})
		currentEventName = ""
	}

	return events
}

func TestListModels(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"data": [
			{"id": "claude-sonnet-4-5-20250929", "type": "model", "created_at": "2025-09-29T00:00:00Z", "display_name": "Claude Sonnet 4.5"},
			{"id": "claude-opus-4-5-20251101", "type": "model", "created_at": "2025-11-01T00:00:00Z", "display_name": "Claude Opus 4.5"},
			{"id": "claude-3-haiku-20240307", "type": "model", "created_at": "2024-03-07T00:00:00Z", "display_name": "Claude 3 Haiku"}
		],
		"has_more": false
	}`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)

	sent := capture.Last(t)
	assert.Equal(t, http.MethodGet, sent.Method)
	assert.Equal(t, "/models", sent.Path)
	assert.Equal(t, "1000", sent.Query.Get("limit"))
	assert.NotEmpty(t, sent.Header.Get("x-api-key"))
	assert.Equal(t, anthropicAPIVersion, sent.Header.Get("anthropic-version"))

	assert.Equal(t, "list", resp.Object)
	assert.Len(t, resp.Data, 3)

	// Verify that all models have the correct fields
	for _, model := range resp.Data {
		assert.NotEmpty(t, model.ID)
		assert.True(t, strings.HasPrefix(model.ID, "claude-"), "Model ID %q should start with 'claude-'", model.ID)
		assert.Equal(t, "model", model.Object)
		assert.Equal(t, "anthropic", model.OwnedBy)
		assert.NotZero(t, model.Created)
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
		assert.True(t, found, "expected model %q in list", model)
	}

	// Verify created timestamps are parsed correctly
	for _, model := range resp.Data {
		if model.ID == "claude-sonnet-4-5-20250929" {
			// 2025-09-29T00:00:00Z in Unix
			expected := int64(1759104000)
			assert.Equal(t, expected, model.Created)
		}
	}
}

func TestListModels_APIError(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusUnauthorized, `{"type": "error", "error": {"type": "authentication_error", "message": "Invalid API key"}}`)

	provider := NewWithHTTPClient("invalid-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	_, err := provider.ListModels(context.Background())
	assert.Error(t, err)
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
			assert.Equal(t, tt.wantTime, got)
		})
	}
}

func TestParseCreatedAt_InvalidFormat(t *testing.T) {
	// For invalid format, it should return current time (non-zero)
	assert.NotZero(t, parseCreatedAt("invalid-date"))
}

func TestChatCompletionWithContext(t *testing.T) {
	server, _ := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		// Simulate a slow response
		<-r.Context().Done()
		w.WriteHeader(http.StatusRequestTimeout)
	})

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
	assert.Error(t, err)
}

func TestConvertToAnthropicRequest(t *testing.T) {
	t.Setenv(defaultMaxTokensEnvVar, "")

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
				assert.Equal(t, "claude-sonnet-4-5-20250929", req.Model)
				require.Len(t, req.Messages, 1)
				assert.Equal(t, "Hello", req.Messages[0].Content)
				assert.Equal(t, 4096, req.MaxTokens)
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
				assert.Equal(t, "You are a helpful assistant", req.System)
				assert.Len(t, req.Messages, 1)
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
				require.NotNil(t, req.Temperature)
				assert.Equal(t, 0.7, *req.Temperature)
				assert.Equal(t, 1024, req.MaxTokens)
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
				require.Len(t, req.Tools, 1)
				require.Equal(t, "lookup_weather", req.Tools[0].Name)
				require.NotNil(t, req.ToolChoice)
				require.Equal(t, "tool", req.ToolChoice.Type)
				require.Equal(t, "lookup_weather", req.ToolChoice.Name)
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
				require.NotNil(t, req.ToolChoice)
				require.Equal(t, "auto", req.ToolChoice.Type)
				require.NotNil(t, req.ToolChoice.DisableParallelToolUse)
				require.True(t, *req.ToolChoice.DisableParallelToolUse)
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
				require.Len(t, req.Messages, 2)

				assistantBlocks, ok := req.Messages[0].Content.([]anthropicContentBlock)
				require.True(t, ok)
				require.Len(t, assistantBlocks, 1, "assistant content = %#v, want one tool_use block", req.Messages[0].Content)
				require.Equal(t, "tool_use", assistantBlocks[0].Type)
				require.Equal(t, "lookup_weather", assistantBlocks[0].Name)
				require.Equal(t, "call_123", assistantBlocks[0].ID, "assistant tool block = %+v, want lookup_weather/call_123", assistantBlocks[0])

				toolBlocks, ok := req.Messages[1].Content.([]anthropicContentBlock)
				require.True(t, ok)
				require.Len(t, toolBlocks, 1, "tool content = %#v, want one tool_result block", req.Messages[1].Content)
				require.Equal(t, "user", req.Messages[1].Role)
				require.Equal(t, "tool_result", toolBlocks[0].Type)
				require.Equal(t, "call_123", toolBlocks[0].ToolUseID)
				require.Equal(t, `{"temperature_c":21}`, toolBlocks[0].Content, "tool result block = %+v, want call_123 payload", toolBlocks[0])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := convertToAnthropicRequest(tt.input)
			require.NoError(t, err)

			tt.checkFn(t, result)
		})
	}
}

func TestConvertToAnthropicRequest_MapsStopSequences(t *testing.T) {
	tests := []struct {
		name string
		stop string
		want []string
	}{
		{name: "array", stop: `["FOO","BAR"]`, want: []string{"FOO", "BAR"}},
		{name: "single string", stop: `"END"`, want: []string{"END"}},
		{name: "empty entries dropped", stop: `["",""]`, want: nil},
		{name: "null", stop: `null`, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &core.ChatRequest{
				Model:    "claude-sonnet-4-5-20250929",
				Messages: []core.Message{{Role: "user", Content: "hi"}},
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					"stop": json.RawMessage(tt.stop),
				}),
			}
			result, err := convertToAnthropicRequest(req)
			require.NoError(t, err)
			assert.Equal(t, tt.want, result.StopSequences)
		})
	}
}

func TestConvertToAnthropicRequest_RejectsUnsupportedChatExtras(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value json.RawMessage
	}{
		{
			name:  "response format",
			field: "response_format",
			value: json.RawMessage(`{"type":"json_schema","json_schema":{"name":"answer"}}`),
		},
		{
			name:  "verbosity",
			field: "verbosity",
			value: json.RawMessage(`"low"`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := convertToAnthropicRequest(&core.ChatRequest{
				Model:    "claude-sonnet-4-5-20250929",
				Messages: []core.Message{{Role: "user", Content: "hi"}},
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					tt.field: tt.value,
				}),
			})
			require.Error(t, err)

			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			require.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
			require.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
			require.Contains(t, gatewayErr.Message, tt.field)
		})
	}
}

func TestConvertToAnthropicRequest_IgnoresNoopChatExtras(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value json.RawMessage
	}{
		{
			name:  "null response format",
			field: "response_format",
			value: json.RawMessage(`null`),
		},
		{
			name:  "text response format",
			field: "response_format",
			value: json.RawMessage(`{"type":"text"}`),
		},
		{
			name:  "null verbosity",
			field: "verbosity",
			value: json.RawMessage(`null`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := convertToAnthropicRequest(&core.ChatRequest{
				Model:    "claude-sonnet-4-5-20250929",
				Messages: []core.Message{{Role: "user", Content: "hi"}},
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					tt.field: tt.value,
				}),
			})
			require.NoError(t, err)
		})
	}
}

func TestConvertToAnthropicRequest_PreservesTopP(t *testing.T) {
	topP := 0.2
	result, err := convertToAnthropicRequest(&core.ChatRequest{
		Model:    "claude-sonnet-4-5-20250929",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		TopP:     &topP,
	})
	require.NoError(t, err)
	require.NotNil(t, result.TopP)
	require.Equal(t, 0.2, *result.TopP)
}

func TestConvertToAnthropicRequest_TopPFromExtraFields(t *testing.T) {
	result, err := convertToAnthropicRequest(&core.ChatRequest{
		Model:    "claude-sonnet-4-5-20250929",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"top_p": json.RawMessage("0.3"),
		}),
	})
	require.NoError(t, err)
	require.NotNil(t, result.TopP)
	require.Equal(t, 0.3, *result.TopP)
}

func TestConvertToAnthropicRequest_TypedTopPWinsOverExtraFields(t *testing.T) {
	topP := 0.2
	result, err := convertToAnthropicRequest(&core.ChatRequest{
		Model:    "claude-sonnet-4-5-20250929",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		TopP:     &topP,
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"top_p": json.RawMessage("0.9"),
		}),
	})
	require.NoError(t, err)
	require.NotNil(t, result.TopP)
	require.Equal(t, 0.2, *result.TopP)
}

func TestConvertToAnthropicRequest_ReasoningEffortFromExtraFields(t *testing.T) {
	result, err := convertToAnthropicRequest(&core.ChatRequest{
		Model:    "claude-fable-5",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"reasoning_effort": json.RawMessage(`"high"`),
		}),
	})
	require.NoError(t, err)
	require.NotNil(t, result.Thinking)
	require.Equal(t, "adaptive", result.Thinking.Type)
	require.NotNil(t, result.OutputConfig)
	require.Equal(t, "high", result.OutputConfig.Effort)
}

func TestConvertToAnthropicRequest_ReasoningObjectWinsOverReasoningEffort(t *testing.T) {
	result, err := convertToAnthropicRequest(&core.ChatRequest{
		Model:     "claude-fable-5",
		Messages:  []core.Message{{Role: "user", Content: "hi"}},
		Reasoning: &core.Reasoning{Effort: "low"},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"reasoning_effort": json.RawMessage(`"max"`),
		}),
	})
	require.NoError(t, err)
	require.NotNil(t, result.OutputConfig)
	require.Equal(t, "low", result.OutputConfig.Effort)
}

func TestConvertToAnthropicRequest_EmptyReasoningObjectFallsBackToReasoningEffort(t *testing.T) {
	result, err := convertToAnthropicRequest(&core.ChatRequest{
		Model:     "claude-fable-5",
		Messages:  []core.Message{{Role: "user", Content: "hi"}},
		Reasoning: &core.Reasoning{},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"reasoning_effort": json.RawMessage(`"high"`),
		}),
	})
	require.NoError(t, err)
	require.NotNil(t, result.OutputConfig)
	require.Equal(t, "high", result.OutputConfig.Effort)
}

func TestResolveAnthropicReasoningEffort_NormalizesSpelling(t *testing.T) {
	tests := []struct {
		name string
		req  *core.ChatRequest
		want string
	}{
		{
			name: "object form uppercase with whitespace",
			req:  &core.ChatRequest{Reasoning: &core.Reasoning{Effort: " HIGH "}},
			want: "high",
		},
		{
			name: "string form mixed case",
			req: &core.ChatRequest{ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				"reasoning_effort": json.RawMessage(`" Max "`),
			})},
			want: "max",
		},
		{
			name: "whitespace-only object effort falls back to string form",
			req: &core.ChatRequest{
				Reasoning: &core.Reasoning{Effort: "  "},
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					"reasoning_effort": json.RawMessage(`"medium"`),
				}),
			},
			want: "medium",
		},
		{
			name: "whitespace-only string form resolves to empty",
			req: &core.ChatRequest{ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				"reasoning_effort": json.RawMessage(`"  "`),
			})},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveAnthropicReasoningEffort(tt.req)
			require.Equal(t, tt.want, got)
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
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
	require.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
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
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
	require.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())

	assert.True(t,
		strings.Contains(gatewayErr.Message, "invalid character") || strings.Contains(gatewayErr.Message, "exactly one JSON object"),
		"error message = %q, want trailing content validation", gatewayErr.Message)
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
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
	require.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
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
				require.Equal(t, "lookup_weather", tools[0].Name)
				require.Equal(t, "Get weather for a city", tools[0].Description)
				schemaType, _ := tools[0].InputSchema["type"].(string)
				require.Equal(t, "object", schemaType)
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
				schemaType, _ := tools[0].InputSchema["type"].(string)
				require.Equal(t, "object", schemaType)
				_, ok := tools[0].InputSchema["properties"].(map[string]any)
				require.True(t, ok, "InputSchema.properties = %#v, want object map", tools[0].InputSchema["properties"])
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
				require.Error(t, err)

				var gatewayErr *core.GatewayError
				require.ErrorAs(t, err, &gatewayErr)
				require.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
				require.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())

				return
			}

			require.NoError(t, err)

			if tt.wantNil {
				require.Nil(t, result)

				return
			}
			require.Equal(t, tt.wantLen, len(result))

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
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
	require.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
}

func TestConvertToAnthropicRequest_ToolMessageRequiresToolCallID(t *testing.T) {
	_, err := convertToAnthropicRequest(&core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{Role: "tool", Content: `{"temperature_c":21}`},
		},
	})
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
	require.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
}

func TestConvertToAnthropicRequest_ToolMessageWithImage(t *testing.T) {
	req, err := convertToAnthropicRequest(&core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{Role: "tool", ToolCallID: "call_123", Content: []core.ContentPart{
				{Type: "text", Text: "captured"},
				{Type: "image_url", ImageURL: &core.ImageURLContent{URL: "data:image/png;base64,aGVsbG8="}},
			}},
		},
	})
	require.NoError(t, err)
	require.Len(t, req.Messages, 1)
	require.Equal(t, "user", req.Messages[0].Role)

	toolBlocks, ok := req.Messages[0].Content.([]anthropicContentBlock)
	require.True(t, ok)
	require.Len(t, toolBlocks, 1)
	require.Equal(t, "tool_result", toolBlocks[0].Type)
	require.Equal(t, "call_123", toolBlocks[0].ToolUseID, "content = %#v, want one tool_result block", req.Messages[0].Content)

	inner, ok := toolBlocks[0].Content.([]anthropicContentBlock)
	require.True(t, ok)
	require.Len(t, inner, 2, "tool_result content = %#v, want text and image blocks", toolBlocks[0].Content)
	assert.Equal(t, "text", inner[0].Type)
	assert.Equal(t, "captured", inner[0].Text, "inner[0] = %+v, want text block", inner[0])
	assert.Equal(t, "image", inner[1].Type)
	require.NotNil(t, inner[1].Source)
	assert.Equal(t, "base64", inner[1].Source.Type)
	assert.Equal(t, "image/png", inner[1].Source.MediaType)
	assert.Equal(t, "aGVsbG8=", inner[1].Source.Data, "inner[1] = %+v, want base64 image block", inner[1])
}

func TestConvertToAnthropicRequest_FilePartsBecomeDocuments(t *testing.T) {
	tests := []struct {
		name string
		file core.FileContent
		want anthropicContentSource
	}{
		{name: "pdf", file: core.FileContent{FileData: "data:application/pdf;base64,JVBERi0=", Filename: "a.pdf"}, want: anthropicContentSource{Type: "base64", MediaType: "application/pdf", Data: "JVBERi0="}},
		{name: "text", file: core.FileContent{FileData: "data:text/plain;base64,aGVsbG8="}, want: anthropicContentSource{Type: "text", MediaType: "text/plain", Data: "hello"}},
		{name: "url", file: core.FileContent{FileURL: "https://example.com/a.pdf"}, want: anthropicContentSource{Type: "url", URL: "https://example.com/a.pdf"}},
		{name: "url in file_data", file: core.FileContent{FileData: "https://example.com/a.pdf"}, want: anthropicContentSource{Type: "url", URL: "https://example.com/a.pdf"}},
		{name: "file id", file: core.FileContent{FileID: "file_123"}, want: anthropicContentSource{Type: "file", FileID: "file_123"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			file := tc.file
			req, err := convertToAnthropicRequest(&core.ChatRequest{
				Model: "claude-sonnet-4-5-20250929",
				Messages: []core.Message{{Role: "user", Content: []core.ContentPart{
					{Type: "text", Text: "read"},
					{Type: "file", File: &file},
				}}},
			})
			require.NoError(t, err)

			blocks, ok := req.Messages[0].Content.([]anthropicContentBlock)
			require.True(t, ok)
			require.Len(t, blocks, 2)
			require.Equal(t, "document", blocks[1].Type)
			require.NotNil(t, blocks[1].Source)
			assert.Equal(t, tc.want, *blocks[1].Source)
			assert.Equal(t, tc.file.Filename, blocks[1].Title)
		})
	}

	_, err := convertToAnthropicRequest(&core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{{Role: "user", Content: []core.ContentPart{
			{Type: "file", File: &core.FileContent{FileData: "data:image/png;base64,aGVsbG8="}},
		}}},
	})
	assert.Error(t, err)

	for _, file := range []core.FileContent{
		{FileURL: "ftp://example.com/a.pdf"},
		{FileURL: "not a url"},
		{FileData: "ftp://example.com/a.pdf"},
		{FileData: "/relative/a.pdf"},
	} {
		_, err := convertToAnthropicRequest(&core.ChatRequest{
			Model:    "claude-sonnet-4-5-20250929",
			Messages: []core.Message{{Role: "user", Content: []core.ContentPart{{Type: "file", File: &file}}}},
		})
		gatewayErr, ok := err.(*core.GatewayError)
		assert.True(t, ok)
		assert.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type, "file %+v: error = %v, want invalid_request_error", file, err)
	}
}

func TestConvertToAnthropicRequest_ToolMessageIsError(t *testing.T) {
	req, err := convertToAnthropicRequest(&core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{Role: "tool", ToolCallID: "call_1", Content: "boom", ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				core.ExtraContentField: json.RawMessage(`{"anthropic":{"is_error":true}}`),
			})},
			{Role: "tool", ToolCallID: "call_2", Content: "fine", ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				core.ExtraContentField: json.RawMessage(`{"google":{"is_error":true}}`),
			})},
		},
	})
	require.NoError(t, err)

	first := req.Messages[0].Content.([]anthropicContentBlock)[0]
	second := req.Messages[1].Content.([]anthropicContentBlock)[0]
	assert.True(t, first.IsError)
	assert.Equal(t, "boom", first.Content, "first tool_result = %+v, want is_error", first)
	assert.False(t, second.IsError, "second tool_result = %+v, want no is_error", second)
}

func TestConvertToAnthropicRequest_ReplaysThinkingBlocks(t *testing.T) {
	req, err := convertToAnthropicRequest(&core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{Role: "user", Content: "hi"},
			{
				Role:      "assistant",
				Content:   "calling",
				ToolCalls: []core.ToolCall{{ID: "tu_1", Type: "function", Function: core.FunctionCall{Name: "lookup", Arguments: "{}"}}},
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					core.ExtraContentField: json.RawMessage(`{"anthropic":{"thinking_blocks":[{"type":"thinking","thinking":"","signature":"sig1"},{"type":"redacted_thinking","data":"opaque"}]}}`),
				}),
			},
			{Role: "tool", ToolCallID: "tu_1", Content: "result"},
		},
	})
	require.NoError(t, err)

	blocks, ok := req.Messages[1].Content.([]anthropicContentBlock)
	require.True(t, ok)
	require.Len(t, blocks, 4, "assistant content = %#v, want thinking, redacted_thinking, text, tool_use", req.Messages[1].Content)
	assert.Equal(t, "thinking", blocks[0].Type)
	require.NotNil(t, blocks[0].Thinking)
	assert.Empty(t, *blocks[0].Thinking)
	assert.Equal(t, "sig1", blocks[0].Signature, "blocks[0] = %+v", blocks[0])
	assert.Equal(t, "redacted_thinking", blocks[1].Type)
	assert.Equal(t, "opaque", blocks[1].Data, "blocks[1] = %+v", blocks[1])
	assert.Equal(t, "text", blocks[2].Type)
	assert.Equal(t, "tool_use", blocks[3].Type, "blocks[2:] = %+v", blocks[2:])

	encoded, err := json.Marshal(blocks[0])
	require.NoError(t, err)
	assert.Equal(t, `{"type":"thinking","thinking":"","signature":"sig1"}`, string(encoded), "encoded thinking block = %s, want empty thinking text kept", encoded)
}

// Reasoning another provider produced reaches Anthropic as a thinking block
// with no signature of Anthropic's own. Anthropic rejects the whole request for
// it ("signature: Field required", or "Invalid `signature`" for anything the
// gateway could mint), so the block is dropped and the rest of the turn stands.
func TestConvertToAnthropicRequest_DropsUnsignedThinkingBlocks(t *testing.T) {
	tests := []struct {
		name   string
		blocks string
		want   []string
	}{
		{
			name:   "missing signature",
			blocks: `[{"type":"thinking","thinking":"foreign"}]`,
			want:   []string{"text"},
		},
		{
			name:   "empty signature",
			blocks: `[{"type":"thinking","thinking":"foreign","signature":""}]`,
			want:   []string{"text"},
		},
		{
			name:   "signed blocks are kept",
			blocks: `[{"type":"thinking","thinking":"own","signature":"sig1"}]`,
			want:   []string{"thinking", "text"},
		},
		{
			name:   "redacted blocks carry data rather than a signature",
			blocks: `[{"type":"redacted_thinking","data":"opaque"}]`,
			want:   []string{"redacted_thinking", "text"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := convertToAnthropicRequest(&core.ChatRequest{
				Model: "claude-sonnet-4-5-20250929",
				Messages: []core.Message{
					{Role: "user", Content: "hi"},
					{
						Role:    "assistant",
						Content: "391",
						ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
							core.ExtraContentField: json.RawMessage(`{"anthropic":{"thinking_blocks":` + tt.blocks + `}}`),
						}),
					},
					{Role: "user", Content: "and now?"},
				},
			})
			require.NoError(t, err)

			var got []string
			switch content := req.Messages[1].Content.(type) {
			case []anthropicContentBlock:
				for _, block := range content {
					got = append(got, block.Type)
				}
			case string:
				got = []string{"text"}
			}
			assert.Equal(t, tt.want, got, "assistant block types")
		})
	}
}

func TestConvertToAnthropicRequest_RejectsMalformedAnthropicExtraContent(t *testing.T) {
	_, err := convertToAnthropicRequest(&core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "x", ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
				core.ExtraContentField: json.RawMessage(`{"anthropic":{"thinking_blocks":"nope"}}`),
			})},
		},
	})
	gatewayErr, ok := err.(*core.GatewayError)
	require.True(t, ok)
	require.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
}

func TestConvertToAnthropicRequest_ToolChoiceRequiresTools(t *testing.T) {
	_, err := convertToAnthropicRequest(&core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
		ToolChoice: "auto",
	})
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
	require.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
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
	require.Error(t, err)
	require.Contains(t, err.Error(), "tool arguments must be a JSON object")
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
	require.NoError(t, err)

	blocks, ok := result.Messages[0].Content.([]anthropicContentBlock)
	require.True(t, ok)
	require.Len(t, blocks, 1, "content = %#v, want one tool_use block", result.Messages[0].Content)
	require.Equal(t, "lookup_weather", blocks[0].Name)
	require.NotEmpty(t, blocks[0].ID)
}

func TestConvertToAnthropicRequest_NormalizesToolResultID(t *testing.T) {
	result, err := convertToAnthropicRequest(&core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{Role: "tool", ToolCallID: "  call_123  ", Content: `{"temperature_c":21}`},
		},
	})
	require.NoError(t, err)

	blocks, ok := result.Messages[0].Content.([]anthropicContentBlock)
	require.True(t, ok)
	require.Len(t, blocks, 1, "content = %#v, want one tool_result block", result.Messages[0].Content)
	require.Equal(t, "call_123", blocks[0].ToolUseID)
}

func TestParseToolCallArguments_UsesJSONNumber(t *testing.T) {
	parsed, err := parseToolCallArguments(`{"value":9007199254740993}`)
	require.NoError(t, err)

	obj, ok := parsed.(map[string]any)
	require.True(t, ok, "parsed = %T, want map[string]any", parsed)

	num, ok := obj["value"].(json.Number)
	require.True(t, ok, "value = %T, want json.Number", obj["value"])
	require.Equal(t, "9007199254740993", string(num))
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

	assert.Equal(t, "msg_123", result.ID)
	assert.Equal(t, "chat.completion", result.Object)
	assert.Equal(t, "claude-sonnet-4-5-20250929", result.Model)
	require.Len(t, result.Choices, 1)
	assert.Equal(t, "Hello! How can I help you today?", result.Choices[0].Message.Content)
	assert.Equal(t, "assistant", result.Choices[0].Message.Role)
	assert.Equal(t, "stop", result.Choices[0].FinishReason)
	assert.Equal(t, 10, result.Usage.PromptTokens)
	assert.Equal(t, 20, result.Usage.CompletionTokens)
	assert.Equal(t, 30, result.Usage.TotalTokens)
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

	require.Len(t, result.Choices, 1)
	require.Equal(t, "tool_calls", result.Choices[0].FinishReason)
	require.Len(t, result.Choices[0].Message.ToolCalls, 1)
	require.Equal(t, "toolu_123", result.Choices[0].Message.ToolCalls[0].ID)
	require.Equal(t, "lookup_weather", result.Choices[0].Message.ToolCalls[0].Function.Name)
	require.Equal(t, `{"city":"Warsaw"}`, result.Choices[0].Message.ToolCalls[0].Function.Arguments)
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
			got := normalizeAnthropicStopReason(tt.in)
			require.Equal(t, tt.want, got)
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

	require.NotNil(t, result.Usage.RawUsage)
	assert.Equal(t, 50, result.Usage.RawUsage["cache_creation_input_tokens"])
	assert.Equal(t, 30, result.Usage.RawUsage["cache_read_input_tokens"])
}

func TestConvertFromAnthropicResponse_WithThinkingTokens(t *testing.T) {
	tests := []struct {
		name           string
		thinkingTokens int
		wantPresent    bool
	}{
		{name: "zero omitted", thinkingTokens: 0, wantPresent: false},
		{name: "positive preserved", thinkingTokens: 27, wantPresent: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{
		"id": "msg_thinking",
		"type": "message",
		"role": "assistant",
		"model": "claude-sonnet-4-5-20250929",
		"content": [{"type": "text", "text": "Done"}],
		"stop_reason": "end_turn",
		"usage": {
			"input_tokens": 31,
			"output_tokens": 311,
			"output_tokens_details": {"thinking_tokens": ` + strconv.Itoa(tt.thinkingTokens) + `}
		}
	}`
			var resp anthropicResponse
			err := json.Unmarshal([]byte(body), &resp)
			require.NoError(t, err)

			result := convertFromAnthropicResponse(&resp)
			got, present := result.Usage.RawUsage["completion_reasoning_tokens"]

			require.Equal(t, tt.wantPresent, present)
			if tt.wantPresent {
				assert.Equal(t, tt.thinkingTokens, got, "RawUsage[completion_reasoning_tokens]")
			}
		})
	}
}

func TestMergeAnthropicUsage_WithThinkingTokens(t *testing.T) {
	dst := anthropicUsage{}
	src := anthropicUsage{
		OutputTokensDetails: anthropicOutputTokensDetails{ThinkingTokens: 27},
	}

	require.True(t, mergeAnthropicUsage(&dst, &src))
	require.Equal(t, 27, dst.OutputTokensDetails.ThinkingTokens)

	chatDetails, ok := anthropicChatUsagePayload(&dst)["completion_tokens_details"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, 27, chatDetails["reasoning_tokens"])

	responseDetails, ok := anthropicResponsesUsagePayload(&dst)["output_tokens_details"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, 27, responseDetails["reasoning_tokens"])
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

	assert.Nil(t, result.Usage.RawUsage)
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

	require.NotNil(t, result.Usage)
	require.NotNil(t, result.Usage.RawUsage)
	assert.Equal(t, 40, result.Usage.RawUsage["cache_creation_input_tokens"])
	assert.Equal(t, 60, result.Usage.RawUsage["cache_read_input_tokens"])
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

			require.NotEmpty(t, result.Choices)
			assert.Equal(t, tt.expectedText, result.Choices[0].Message.Content)
			assert.Equal(t, 40, result.Usage.CompletionTokens)
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
			assert.Equal(t, tt.expected, result)
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
				assert.Equal(t, "msg_123", resp.ID)
				assert.Equal(t, "response", resp.Object)
				assert.Equal(t, "claude-sonnet-4-5-20250929", resp.Model)
				assert.Equal(t, "completed", resp.Status)
				require.Len(t, resp.Output, 1)
				require.Len(t, resp.Output[0].Content, 1)
				assert.Equal(t, "Hello! How can I help you today?", resp.Output[0].Content[0].Text)
				require.NotNil(t, resp.Usage)
				assert.Equal(t, 10, resp.Usage.InputTokens)
				assert.Equal(t, 20, resp.Usage.OutputTokens)
				assert.Equal(t, 30, resp.Usage.TotalTokens)
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
			server, capture := providertest.JSONServer(t, tt.statusCode, tt.responseBody)

			provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			req := &core.ResponsesRequest{
				Model: "claude-sonnet-4-5-20250929",
				Input: "Hello",
			}

			resp, err := provider.Responses(context.Background(), req)

			sent := capture.Last(t)
			assert.Equal(t, "/messages", sent.Path)
			assert.Equal(t, "application/json", sent.Header.Get("Content-Type"))
			assert.NotEmpty(t, sent.Header.Get("x-api-key"))
			assert.Equal(t, anthropicAPIVersion, sent.Header.Get("anthropic-version"))
			var anthropicReq anthropicRequest
			require.NoError(t, json.Unmarshal(sent.Body, &anthropicReq))

			if tt.expectedError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.checkResponse != nil {
				tt.checkResponse(t, resp)
			}
		})
	}
}

func TestResponsesWithArrayInput(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"model": "claude-sonnet-4-5-20250929",
		"content": [{"type": "text", "text": "Hello!"}],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	req := &core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
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
	require.NoError(t, err)
	assert.Equal(t, "msg_123", resp.ID)

	var sent anthropicRequest
	require.NoError(t, json.Unmarshal(capture.Last(t).Body, &sent))
	require.Len(t, sent.Messages, 2)
	assert.Equal(t, "user", sent.Messages[0].Role)
	assert.Equal(t, "Hello", sent.Messages[0].Content)
}

func TestResponsesWithInstructions(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"model": "claude-sonnet-4-5-20250929",
		"content": [{"type": "text", "text": "Hello!"}],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	req := &core.ResponsesRequest{
		Model:        "claude-sonnet-4-5-20250929",
		Input:        "Hello",
		Instructions: "You are a helpful assistant",
	}

	_, err := provider.Responses(context.Background(), req)
	require.NoError(t, err)

	var sent anthropicRequest
	require.NoError(t, json.Unmarshal(capture.Last(t).Body, &sent))
	assert.Equal(t, "You are a helpful assistant", sent.System)
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
				require.NotNil(t, body)

				defer func() { _ = body.Close() }()

				// Read and verify the streaming response
				respBody, err := io.ReadAll(body)
				require.NoError(t, err)

				// The response should be converted to Responses API format
				responseStr := string(respBody)
				assert.Contains(t, responseStr, "response.created")
				assert.Contains(t, responseStr, "response.output_text.delta")
				assert.Contains(t, responseStr, "[DONE]")
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
			server, capture := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = io.WriteString(w, tt.responseBody)
			})

			provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			req := &core.ResponsesRequest{
				Model: "claude-sonnet-4-5-20250929",
				Input: "Hello",
			}

			body, err := provider.StreamResponses(context.Background(), req)

			sent := capture.Last(t)
			assert.Equal(t, "application/json", sent.Header.Get("Content-Type"))
			assert.NotEmpty(t, sent.Header.Get("x-api-key"))
			var anthropicReq anthropicRequest
			require.NoError(t, json.Unmarshal(sent.Body, &anthropicReq))
			assert.True(t, anthropicReq.Stream)

			if tt.expectedError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.checkStream != nil {
				tt.checkStream(t, body)
			}
		})
	}
}

func TestStreamResponses_MergesUsageFromMessageStart(t *testing.T) {
	server, _ := providertest.SSEServer(t, `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0,"cache_creation_input_tokens":4}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}
`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	body, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: "Hello",
	})
	require.NoError(t, err)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)

	responseStr := string(raw)
	require.Contains(t, responseStr, `"type":"response.completed"`)
	require.Contains(t, responseStr, `"input_tokens":10`)
	require.Contains(t, responseStr, `"output_tokens":2`)
	require.Contains(t, responseStr, `"total_tokens":12`)
	require.Contains(t, responseStr, `"cache_creation_input_tokens":4`)
}

func TestStreamResponses_WithToolCalls(t *testing.T) {
	server, _ := providertest.SSEServer(t, `event: message_start
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
`)

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
	require.NoError(t, err)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)

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
			item, _ := event.Payload["item"].(map[string]any)
			if item["type"] == "message" && item["role"] == "assistant" && event.Payload["output_index"] == float64(0) {
				foundAssistantAdded = true
			}
			if item["type"] == "function_call" && item["call_id"] == "toolu_123" && item["name"] == "lookup_weather" && item["arguments"] == "{}" && event.Payload["output_index"] == float64(1) {
				foundAdded = true
			}
		case "response.output_item.done":
			item, _ := event.Payload["item"].(map[string]any)
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

	require.True(t, foundAdded)
	require.True(t, foundAssistantAdded)
	require.True(t, foundAssistantDone)
	require.True(t, foundTextDelta)
	require.Equal(t, `{"city":"Warsaw"}`, argumentsDelta.String())
	require.True(t, foundArgumentsDone)
	require.True(t, foundItemDone)
}

// TestStreamResponses_CompletedIncludesOutput verifies the terminal
// response.completed event carries the full output array (assistant message,
// then function_call), matching OpenAI's native behavior — strict SDK clients
// index into response.output.
func TestStreamResponses_CompletedIncludesOutput(t *testing.T) {
	server, _ := providertest.SSEServer(t, `event: message_start
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
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Warsaw\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":10,"output_tokens":4}}

event: message_stop
data: {"type":"message_stop"}
`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	body, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: "What's the weather?",
	})
	require.NoError(t, err)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)

	var output []any
	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Done || event.Name != "response.completed" {
			continue
		}
		response, _ := event.Payload["response"].(map[string]any)
		output, _ = response["output"].([]any)
	}

	require.Len(t, output, 2)

	message, _ := output[0].(map[string]any)
	require.Equal(t, "message", message["type"])
	require.Equal(t, "assistant", message["role"])
	require.Equal(t, "completed", message["status"], "output[0] = %#v, want completed assistant message", message)

	messageContent, _ := message["content"].([]any)
	require.Len(t, messageContent, 1, "message content = %#v, want one output_text part", message["content"])
	part, _ := messageContent[0].(map[string]any)
	require.Equal(t, "output_text", part["type"])
	require.Equal(t, "I'll check that for you.", part["text"], "message part = %#v", messageContent[0])

	toolCall, _ := output[1].(map[string]any)
	require.Equal(t, "function_call", toolCall["type"])
	require.Equal(t, "completed", toolCall["status"], "output[1] = %#v, want completed function_call", toolCall)
	require.Equal(t, "toolu_123", toolCall["call_id"])
	require.Equal(t, "lookup_weather", toolCall["name"])
	require.Equal(t, `{"city":"Warsaw"}`, toolCall["arguments"], "function_call = %#v, want toolu_123 lookup_weather with recorded arguments", toolCall)
}

// TestStreamResponses_TruncatedToolCallFinalizedAtEOF covers an upstream stream
// that dies mid tool call (no content_block_stop / message_stop). The converter
// must close the tool call with status "incomplete" and end the stream with
// response.incomplete instead of fabricating completion.
func TestStreamResponses_TruncatedToolCallFinalizedAtEOF(t *testing.T) {
	server, _ := providertest.SSEServer(t, `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_123","name":"lookup_weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"War"}}
`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	body, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: "What's the weather?",
	})
	require.NoError(t, err)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)

	foundArgumentsDone := false
	itemDoneStatus := ""
	foundCompleted := false
	var response map[string]any
	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Done {
			continue
		}
		switch event.Name {
		case "response.function_call_arguments.done":
			foundArgumentsDone = true
		case "response.output_item.done":
			item, _ := event.Payload["item"].(map[string]any)
			if item["type"] == "function_call" {
				itemDoneStatus, _ = item["status"].(string)
			}
		case "response.completed":
			foundCompleted = true
		case "response.incomplete":
			response, _ = event.Payload["response"].(map[string]any)
		}
	}

	require.False(t, foundCompleted)
	require.NotNil(t, response)
	require.True(t, foundArgumentsDone)
	require.Equal(t, "incomplete", itemDoneStatus)
	require.Equal(t, "incomplete", response["status"])

	details, _ := response["incomplete_details"].(map[string]any)
	require.Equal(t, "interrupted", details["reason"], "incomplete_details = %#v, want reason interrupted", response["incomplete_details"])

	output, _ := response["output"].([]any)
	require.Len(t, output, 1)

	toolCall, _ := output[0].(map[string]any)
	require.Equal(t, "function_call", toolCall["type"])
	require.Equal(t, "toolu_123", toolCall["call_id"])
	require.Equal(t, "incomplete", toolCall["status"])
	require.Equal(t, `{"city":"War`, toolCall["arguments"], "output[0] = %#v, want incomplete function_call with accumulated arguments", toolCall)
}

// failingReadCloser returns its data on the first read and the configured
// error afterwards, mimicking an upstream body that dies mid-transfer.
type failingReadCloser struct {
	data []byte
	err  error
	read bool
}

func (r *failingReadCloser) Read(p []byte) (int, error) {
	if !r.read {
		r.read = true
		return copy(p, r.data), nil
	}
	return 0, r.err
}

func (r *failingReadCloser) Close() error { return nil }

// TestStreamResponses_NonEOFReadErrorEndsIncomplete covers an upstream body
// that fails with a non-EOF error mid-message: the client must still receive
// the response.incomplete terminal event and [DONE] before the error surfaces.
func TestStreamResponses_NonEOFReadErrorEndsIncomplete(t *testing.T) {
	reader := &failingReadCloser{
		data: []byte(`event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}

`),
		err: io.ErrUnexpectedEOF,
	}

	converter := newResponsesStreamConverter(reader, "claude-sonnet-4-5-20250929")
	raw, err := io.ReadAll(converter)
	require.Equal(t, io.ErrUnexpectedEOF, err)

	var response map[string]any
	sawDone := false
	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Done {
			sawDone = true
			continue
		}
		if event.Name == "response.incomplete" {
			response, _ = event.Payload["response"].(map[string]any)
		}
	}

	require.NotNil(t, response)
	require.True(t, sawDone)
	require.Equal(t, "incomplete", response["status"])
}

// TestStreamResponses_StopReasonWithoutMessageStopCompletes covers a stream cut
// after message_delta carried a stop_reason but before message_stop arrived:
// Anthropic finished generating, so the stream must still end with
// response.completed.
func TestStreamResponses_StopReasonWithoutMessageStopCompletes(t *testing.T) {
	server, _ := providertest.SSEServer(t, `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}
`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	body, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: "Hello",
	})
	require.NoError(t, err)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)

	var response map[string]any
	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Done || event.Name != "response.completed" {
			continue
		}
		response, _ = event.Payload["response"].(map[string]any)
	}

	require.NotNil(t, response)
	require.Equal(t, "completed", response["status"])
}

// TestStreamResponses_ToolCallBeforeTextKeepsOutputOrder covers a tool_use
// block preceding the first text block. The assistant message must claim the
// next free output index (not collide with the tool call at index 0) and the
// terminal output must preserve stream order.
func TestStreamResponses_ToolCallBeforeTextKeepsOutputOrder(t *testing.T) {
	server, _ := providertest.SSEServer(t, `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_123","name":"lookup_weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Warsaw\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Looking it up."}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_stop
data: {"type":"message_stop"}
`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	body, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: "What's the weather?",
	})
	require.NoError(t, err)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)

	addedIndexes := make(map[string]float64)
	var output []any
	for _, event := range parseTestSSEEvents(t, string(raw)) {
		if event.Done {
			continue
		}
		switch event.Name {
		case "response.output_item.added":
			item, _ := event.Payload["item"].(map[string]any)
			itemType, _ := item["type"].(string)
			addedIndexes[itemType], _ = event.Payload["output_index"].(float64)
		case "response.completed":
			response, _ := event.Payload["response"].(map[string]any)
			output, _ = response["output"].([]any)
		}
	}

	require.Equal(t, float64(0), addedIndexes["function_call"])
	require.Equal(t, float64(1), addedIndexes["message"], "output indexes = %#v, want function_call at 0 and message at 1", addedIndexes)
	require.Len(t, output, 2)

	first, _ := output[0].(map[string]any)
	second, _ := output[1].(map[string]any)
	require.Equal(t, "function_call", first["type"])
	require.Equal(t, "message", second["type"])
}

func TestStreamResponses_WithEmptyToolArguments(t *testing.T) {
	server, _ := providertest.SSEServer(t, `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_123","name":"lookup_weather","input":{}}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":10,"output_tokens":4}}

event: message_stop
data: {"type":"message_stop"}
`)

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
	require.NoError(t, err)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.NoError(t, err)

	events := parseTestSSEEvents(t, string(raw))
	foundAdded := false
	foundDone := false

	for _, event := range events {
		if event.Done {
			continue
		}
		switch event.Name {
		case "response.output_item.added":
			item, _ := event.Payload["item"].(map[string]any)
			if item["type"] == "function_call" && item["arguments"] == "{}" {
				foundAdded = true
			}
		case "response.function_call_arguments.done":
			if event.Payload["arguments"] == "{}" {
				foundDone = true
			}
		}
	}

	require.True(t, foundAdded)
	require.True(t, foundDone)
}

func TestStreamResponses_MalformedEventReturnsError(t *testing.T) {
	server, _ := providertest.SSEServer(t, `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"broken"}
`)

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	body, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: "Hello",
	})
	require.NoError(t, err)

	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, http.StatusBadGateway, gatewayErr.StatusCode)
	require.Contains(t, gatewayErr.Message, "failed to decode anthropic stream event")
	require.Contains(t, string(raw), "response.created")
	require.NotContains(t, string(raw), "[DONE]", "no [DONE] after a malformed event")
}

func TestResponsesWithContext(t *testing.T) {
	server, _ := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		// Simulate a slow response
		<-r.Context().Done()
		w.WriteHeader(http.StatusRequestTimeout)
	})

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	req := &core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: "Hello",
	}

	_, err := provider.Responses(ctx, req)
	assert.Error(t, err)
}

func TestConvertResponsesRequestToAnthropic(t *testing.T) {
	temp := 0.7
	topP := 0.2
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
				assert.Equal(t, "claude-sonnet-4-5-20250929", req.Model)
				require.Len(t, req.Messages, 1)
				assert.Equal(t, "user", req.Messages[0].Role)
				assert.Equal(t, "Hello", req.Messages[0].Content)
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
				assert.Equal(t, "Be helpful", req.System)
			},
		},
		{
			name: "with parameters",
			input: &core.ResponsesRequest{
				Model:           "claude-sonnet-4-5-20250929",
				Input:           "Hello",
				Temperature:     &temp,
				TopP:            &topP,
				MaxOutputTokens: &maxTokens,
			},
			checkFn: func(t *testing.T, req *anthropicRequest) {
				require.NotNil(t, req.Temperature)
				assert.Equal(t, 0.7, *req.Temperature)

				// Anthropic rejects both sampling parameters at once, so
				// top_p is dropped in favour of temperature.
				assert.Nil(t, req.TopP)
				assert.Equal(t, 1024, req.MaxTokens)
			},
		},
		{
			name: "array input with content parts",
			input: &core.ResponsesRequest{
				Model: "claude-sonnet-4-5-20250929",
				Input: []any{
					map[string]any{
						"role": "user",
						"content": []any{
							map[string]any{
								"type": "text",
								"text": "Hello",
							},
							map[string]any{
								"type": "text",
								"text": "World",
							},
						},
					},
				},
			},
			checkFn: func(t *testing.T, req *anthropicRequest) {
				require.Len(t, req.Messages, 1)
				assert.Equal(t, "Hello World", req.Messages[0].Content)
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
				require.Len(t, req.Tools, 1)
				require.NotNil(t, req.ToolChoice)
				require.NotNil(t, req.ToolChoice.DisableParallelToolUse)
				require.True(t, *req.ToolChoice.DisableParallelToolUse)
			},
		},
		{
			name: "with function call loop input items",
			input: &core.ResponsesRequest{
				Model: "claude-sonnet-4-5-20250929",
				Input: []any{
					map[string]any{
						"type":      "function_call",
						"call_id":   "call_123",
						"name":      "lookup_weather",
						"arguments": `{"city":"Warsaw"}`,
					},
					map[string]any{
						"type":    "function_call_output",
						"call_id": "call_123",
						"output":  map[string]any{"temperature_c": 21},
					},
				},
			},
			checkFn: func(t *testing.T, req *anthropicRequest) {
				require.Len(t, req.Messages, 2)

				assistantBlocks, ok := req.Messages[0].Content.([]anthropicContentBlock)
				require.True(t, ok)
				require.Len(t, assistantBlocks, 1, "assistant content = %#v, want one tool_use block", req.Messages[0].Content)
				require.Equal(t, "tool_use", assistantBlocks[0].Type)
				require.Equal(t, "call_123", assistantBlocks[0].ID)
				require.Equal(t, "lookup_weather", assistantBlocks[0].Name, "assistant tool block = %+v, want lookup_weather/call_123", assistantBlocks[0])

				toolBlocks, ok := req.Messages[1].Content.([]anthropicContentBlock)
				require.True(t, ok)
				require.Len(t, toolBlocks, 1, "tool content = %#v, want one tool_result block", req.Messages[1].Content)
				require.Equal(t, "user", req.Messages[1].Role)
				require.Equal(t, "tool_result", toolBlocks[0].Type)
				require.Equal(t, "call_123", toolBlocks[0].ToolUseID)
				require.Equal(t, `{"temperature_c":21}`, toolBlocks[0].Content, "tool result block = %+v, want call_123 payload", toolBlocks[0])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := convertResponsesRequestToAnthropic(tt.input)
			require.NoError(t, err)

			tt.checkFn(t, result)
		})
	}
}

func TestConvertResponsesRequestToAnthropic_InvalidToolArguments(t *testing.T) {
	_, err := convertResponsesRequestToAnthropic(&core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: []any{
			map[string]any{
				"type":      "function_call",
				"call_id":   "call_123",
				"name":      "lookup_weather",
				"arguments": `{"city":"Warsaw"`,
			},
		},
	})
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
	require.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
}

func TestConvertResponsesRequestToAnthropic_RejectsTrailingToolArgumentContent(t *testing.T) {
	_, err := convertResponsesRequestToAnthropic(&core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: []any{
			map[string]any{
				"type":      "function_call",
				"call_id":   "call_123",
				"name":      "lookup_weather",
				"arguments": `{"city":"Warsaw"} garbage`,
			},
		},
	})
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
	require.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
}

func TestConvertResponsesRequestToAnthropic_ToolChoiceRequiresTools(t *testing.T) {
	_, err := convertResponsesRequestToAnthropic(&core.ResponsesRequest{
		Model:      "claude-sonnet-4-5-20250929",
		Input:      "Hello",
		ToolChoice: "auto",
	})
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
	require.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
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
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
	require.Equal(t, "batch item 0: tool_choice requires at least one tool", gatewayErr.Message)
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
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
	require.True(t, strings.HasPrefix(gatewayErr.Message, "batch item 0: "), "error message = %q", gatewayErr.Message)
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
	require.NoError(t, err)
	require.NotNil(t, anthropicReq)
	require.Len(t, anthropicReq.Requests, 1)
	assert.False(t, anthropicReq.Requests[0].Params.Stream)
	assert.Equal(t, "/v1/responses", endpointByCustomID["resp-1"])
}

func TestBuildAnthropicBatchCreateRequest_RejectsDuplicateCustomIDs(t *testing.T) {
	req := &core.BatchRequest{
		Requests: []core.BatchRequestItem{
			{
				CustomID: "dup-1",
				Method:   http.MethodPost,
				URL:      "/v1/chat/completions",
				Body: json.RawMessage(`{
					"model":"claude-sonnet-4-5-20250929",
					"messages":[{"role":"user","content":"hello"}]
				}`),
			},
			{
				CustomID: "dup-1",
				Method:   http.MethodPost,
				URL:      "/v1/responses",
				Body: json.RawMessage(`{
					"model":"claude-sonnet-4-5-20250929",
					"input":"hello"
				}`),
			},
		},
	}

	_, _, err := buildAnthropicBatchCreateRequest(req)
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	require.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
	require.Contains(t, gatewayErr.Message, `duplicate custom_id "dup-1"`)
}

func TestConvertDecodedBatchItemToAnthropic_ResponsesUsesSharedSemanticTranslator(t *testing.T) {
	decoded := &core.DecodedBatchItemRequest{
		Endpoint:  "/v1/responses",
		Operation: core.OperationResponses,
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
	require.NoError(t, err)
	require.Equal(t, "Be helpful", result.System)
	require.False(t, result.Stream)
	require.Len(t, result.Messages, 1)
	require.Equal(t, "user", result.Messages[0].Role)
	require.Equal(t, "Hello", result.Messages[0].Content)
}

func TestConvertDecodedBatchItemToAnthropic_RejectsStreaming(t *testing.T) {
	decoded := &core.DecodedBatchItemRequest{
		Endpoint:  "/v1/chat/completions",
		Operation: core.OperationChatCompletions,
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
	require.Error(t, err)
	require.Contains(t, err.Error(), "streaming is not supported for native batch")
}

func TestConvertDecodedBatchItemToAnthropic_RejectsEmbeddings(t *testing.T) {
	decoded := &core.DecodedBatchItemRequest{
		Endpoint:  "/v1/embeddings",
		Operation: core.OperationEmbeddings,
		Request: &core.EmbeddingRequest{
			Model: "text-embedding-3-small",
			Input: "Hello",
		},
	}

	_, err := convertDecodedBatchItemToAnthropic(decoded)
	require.Error(t, err)
	require.Contains(t, err.Error(), "anthropic does not support native embedding batches")
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

	assert.Equal(t, "msg_123", result.ID)
	assert.Equal(t, "response", result.Object)
	assert.Equal(t, "claude-sonnet-4-5-20250929", result.Model)
	assert.Equal(t, "completed", result.Status)
	require.Len(t, result.Output, 1)
	assert.Equal(t, "message", result.Output[0].Type)
	assert.Equal(t, "assistant", result.Output[0].Role)
	require.Len(t, result.Output[0].Content, 1)
	assert.Equal(t, "Hello! How can I help you today?", result.Output[0].Content[0].Text)
	require.NotNil(t, result.Usage)
	assert.Equal(t, 10, result.Usage.InputTokens)
	assert.Equal(t, 20, result.Usage.OutputTokens)
	assert.Equal(t, 30, result.Usage.TotalTokens)
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

	require.Len(t, result.Output, 2)
	require.Equal(t, "message", result.Output[0].Type)
	require.Equal(t, "I'll check that for you.", result.Output[0].Content[0].Text)
	require.Equal(t, "function_call", result.Output[1].Type)
	require.Equal(t, "toolu_123", result.Output[1].CallID)
	require.Equal(t, "lookup_weather", result.Output[1].Name)
	require.Equal(t, `{"city":"Warsaw"}`, result.Output[1].Arguments)
}

func TestConvertAnthropicResponseToResponses_WithThinkingBlocks(t *testing.T) {
	tests := []struct {
		name         string
		content      []anthropicContent
		expectedText string
		wantReplay   string
	}{
		{
			name: "thinking then text",
			content: []anthropicContent{
				{Type: "thinking", Thinking: "The user is asking about geography...", Signature: "sig-1"},
				{Type: "text", Text: "The capital of France is Paris."},
			},
			expectedText: "The capital of France is Paris.",
			wantReplay:   `{"anthropic":{"thinking_blocks":[{"type":"thinking","thinking":"The user is asking about geography...","signature":"sig-1"}]}}`,
		},
		{
			name: "preamble text then thinking then answer",
			content: []anthropicContent{
				{Type: "text", Text: "\n\n"},
				{Type: "thinking", Thinking: "", Signature: "sig-2"},
				{Type: "text", Text: "The capital of France is Paris."},
			},
			expectedText: "The capital of France is Paris.",
			// A thinking block whose text the model omitted still has to be
			// replayed: the signature covers the block, not the text.
			wantReplay: `{"anthropic":{"thinking_blocks":[{"type":"thinking","thinking":"","signature":"sig-2"}]}}`,
		},
		{
			name: "redacted thinking",
			content: []anthropicContent{
				{Type: "redacted_thinking", Data: "opaque"},
				{Type: "text", Text: "The capital of France is Paris."},
			},
			expectedText: "The capital of France is Paris.",
			wantReplay:   `{"anthropic":{"thinking_blocks":[{"type":"redacted_thinking","data":"opaque"}]}}`,
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

			// A thinking block always produces a leading reasoning item: it
			// carries the signature the next turn has to replay, even when the
			// model left the thinking text empty.
			require.Len(t, result.Output, 2)

			reasoning, message := result.Output[0], result.Output[1]
			require.Equal(t, "reasoning", reasoning.Type)
			raw := reasoning.ExtraFields.Lookup(core.ExtraContentField)
			assert.Equal(t, tt.wantReplay, string(raw))
			require.NotEmpty(t, message.Content)
			assert.Equal(t, tt.expectedText, message.Content[0].Text)
			assert.Equal(t, 50, result.Usage.OutputTokens)
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
			maxTokens:         new(1000),
			expectedMaxTokens: 1000,
		},
		{
			name:              "empty effort - no thinking",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: ""},
			maxTokens:         new(1000),
			expectedMaxTokens: 1000,
		},
		{
			name:              "legacy model - low effort",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: "low"},
			maxTokens:         new(10000),
			expectedThinkType: "enabled",
			expectedBudget:    5000,
			expectedMaxTokens: 10000,
			expectNilTemp:     true,
		},
		{
			name:              "legacy model - medium effort",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: "medium"},
			maxTokens:         new(15000),
			expectedThinkType: "enabled",
			expectedBudget:    10000,
			expectedMaxTokens: 15000,
			expectNilTemp:     true,
		},
		{
			name:              "legacy model - high effort",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: "high"},
			maxTokens:         new(25000),
			expectedThinkType: "enabled",
			expectedBudget:    20000,
			expectedMaxTokens: 25000,
			expectNilTemp:     true,
		},
		{
			name:              "legacy model - invalid effort defaults to low",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: "invalid"},
			maxTokens:         new(10000),
			expectedThinkType: "enabled",
			expectedBudget:    5000,
			expectedMaxTokens: 10000,
			expectNilTemp:     true,
		},
		{
			name:              "legacy model - bumps max_tokens when too low",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: "high"},
			maxTokens:         new(1000),
			expectedThinkType: "enabled",
			expectedBudget:    20000,
			expectedMaxTokens: 21024,
			expectNilTemp:     true,
		},
		{
			name:              "legacy model - removes temperature",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: "medium"},
			maxTokens:         new(15000),
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
			maxTokens:         new(15000),
			setTemperatureOne: true,
			expectedThinkType: "enabled",
			expectedBudget:    10000,
			expectedMaxTokens: 15000,
			expectNilTemp:     false,
			expectedTemp:      new(1.0),
		},
		{
			name:              "4.6 model - adaptive thinking with high effort",
			model:             "claude-opus-4-6",
			reasoning:         &core.Reasoning{Effort: "high"},
			maxTokens:         new(4096),
			expectedThinkType: "adaptive",
			expectedEffort:    "high",
			expectedMaxTokens: 4096,
			expectNilTemp:     true,
		},
		{
			name:              "4.6 model - adaptive thinking with low effort",
			model:             "claude-sonnet-4-6-20260301",
			reasoning:         &core.Reasoning{Effort: "low"},
			maxTokens:         new(4096),
			expectedThinkType: "adaptive",
			expectedEffort:    "low",
			expectedMaxTokens: 4096,
			expectNilTemp:     true,
		},
		{
			name:              "4.6 model - does not bump max_tokens",
			model:             "claude-opus-4-6",
			reasoning:         &core.Reasoning{Effort: "high"},
			maxTokens:         new(1000),
			expectedThinkType: "adaptive",
			expectedEffort:    "high",
			expectedMaxTokens: 1000,
			expectNilTemp:     true,
		},
		{
			name:              "4.6 model - removes temperature",
			model:             "claude-opus-4-6",
			reasoning:         &core.Reasoning{Effort: "medium"},
			maxTokens:         new(4096),
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
			maxTokens:         new(4096),
			expectedThinkType: "adaptive",
			expectedEffort:    "low",
			expectedMaxTokens: 4096,
			expectNilTemp:     true,
		},
		{
			name:              "fable 5 - adaptive thinking with high effort",
			model:             "claude-fable-5",
			reasoning:         &core.Reasoning{Effort: "high"},
			maxTokens:         new(4096),
			expectedThinkType: "adaptive",
			expectedEffort:    "high",
			expectedMaxTokens: 4096,
			expectNilTemp:     true,
		},
		{
			name:              "opus 4.8 - adaptive thinking with xhigh effort",
			model:             "claude-opus-4-8",
			reasoning:         &core.Reasoning{Effort: "xhigh"},
			maxTokens:         new(4096),
			expectedThinkType: "adaptive",
			expectedEffort:    "xhigh",
			expectedMaxTokens: 4096,
			expectNilTemp:     true,
		},
		{
			name:              "opus 4.8 - adaptive thinking with max effort",
			model:             "claude-opus-4-8-20260301",
			reasoning:         &core.Reasoning{Effort: "max"},
			maxTokens:         new(4096),
			expectedThinkType: "adaptive",
			expectedEffort:    "max",
			expectedMaxTokens: 4096,
			expectNilTemp:     true,
		},
		{
			name:              "opus 4.7 - adaptive thinking with high effort",
			model:             "claude-opus-4-7",
			reasoning:         &core.Reasoning{Effort: "high"},
			maxTokens:         new(4096),
			expectedThinkType: "adaptive",
			expectedEffort:    "high",
			expectedMaxTokens: 4096,
			expectNilTemp:     true,
		},
		{
			name:              "legacy model - xhigh effort caps at high budget",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: "xhigh"},
			maxTokens:         new(25000),
			expectedThinkType: "enabled",
			expectedBudget:    20000,
			expectedMaxTokens: 25000,
			expectNilTemp:     true,
		},
		{
			name:              "legacy model - max effort caps at high budget",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: "max"},
			maxTokens:         new(25000),
			expectedThinkType: "enabled",
			expectedBudget:    20000,
			expectedMaxTokens: 25000,
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
			require.NoError(t, err)

			if tt.expectedThinkType == "" {
				assert.Nil(t, result.Thinking)
				assert.Nil(t, result.OutputConfig)

			} else {
				require.NotNil(t, result.Thinking)
				assert.Equal(t, tt.expectedThinkType, result.Thinking.Type)

				if tt.expectedThinkType == "enabled" {
					assert.Equal(t, tt.expectedBudget, result.Thinking.BudgetTokens)
				}
				if tt.expectedThinkType == "adaptive" {
					require.NotNil(t, result.OutputConfig)
					assert.Equal(t, tt.expectedEffort, result.OutputConfig.Effort)
				}
			}

			assert.Equal(t, tt.expectedMaxTokens, result.MaxTokens)

			if tt.expectNilTemp {
				assert.Nil(t, result.Temperature)
			}
			if tt.expectedTemp != nil {
				assert.Equal(t, tt.expectedTemp, result.Temperature)
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
			maxOutputTokens:   new(1000),
			expectedMaxTokens: 1000,
		},
		{
			name:              "empty effort",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: ""},
			maxOutputTokens:   new(1000),
			expectedMaxTokens: 1000,
		},
		{
			name:              "legacy model - low effort bumps max tokens",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: "low"},
			maxOutputTokens:   new(1000),
			expectedThinkType: "enabled",
			expectedBudget:    5000,
			expectedMaxTokens: 6024,
			expectNilTemp:     true,
		},
		{
			name:              "legacy model - high effort with sufficient tokens",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: "high"},
			maxOutputTokens:   new(25000),
			expectedThinkType: "enabled",
			expectedBudget:    20000,
			expectedMaxTokens: 25000,
			expectNilTemp:     true,
		},
		{
			name:              "legacy model - removes temperature",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: "medium"},
			maxOutputTokens:   new(15000),
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
			maxOutputTokens:   new(4096),
			expectedThinkType: "adaptive",
			expectedEffort:    "high",
			expectedMaxTokens: 4096,
			expectNilTemp:     true,
		},
		{
			name:              "4.6 model - does not bump max_tokens",
			model:             "claude-opus-4-6",
			reasoning:         &core.Reasoning{Effort: "high"},
			maxOutputTokens:   new(1000),
			expectedThinkType: "adaptive",
			expectedEffort:    "high",
			expectedMaxTokens: 1000,
			expectNilTemp:     true,
		},
		{
			name:              "4.6 model - removes temperature",
			model:             "claude-opus-4-6",
			reasoning:         &core.Reasoning{Effort: "medium"},
			maxOutputTokens:   new(4096),
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
			maxOutputTokens:   new(4096),
			expectedThinkType: "adaptive",
			expectedEffort:    "low",
			expectedMaxTokens: 4096,
			expectNilTemp:     true,
		},
		{
			name:              "fable 5 - adaptive thinking with high effort",
			model:             "claude-fable-5",
			reasoning:         &core.Reasoning{Effort: "high"},
			maxOutputTokens:   new(4096),
			expectedThinkType: "adaptive",
			expectedEffort:    "high",
			expectedMaxTokens: 4096,
			expectNilTemp:     true,
		},
		{
			name:              "opus 4.8 - adaptive thinking with xhigh effort",
			model:             "claude-opus-4-8",
			reasoning:         &core.Reasoning{Effort: "xhigh"},
			maxOutputTokens:   new(4096),
			expectedThinkType: "adaptive",
			expectedEffort:    "xhigh",
			expectedMaxTokens: 4096,
			expectNilTemp:     true,
		},
		{
			name:              "opus 4.8 - adaptive thinking with max effort",
			model:             "claude-opus-4-8-20260301",
			reasoning:         &core.Reasoning{Effort: "max"},
			maxOutputTokens:   new(4096),
			expectedThinkType: "adaptive",
			expectedEffort:    "max",
			expectedMaxTokens: 4096,
			expectNilTemp:     true,
		},
		{
			name:              "opus 4.7 - adaptive thinking with high effort",
			model:             "claude-opus-4-7",
			reasoning:         &core.Reasoning{Effort: "high"},
			maxOutputTokens:   new(4096),
			expectedThinkType: "adaptive",
			expectedEffort:    "high",
			expectedMaxTokens: 4096,
			expectNilTemp:     true,
		},
		{
			name:              "legacy model - xhigh effort caps at high budget",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: "xhigh"},
			maxOutputTokens:   new(25000),
			expectedThinkType: "enabled",
			expectedBudget:    20000,
			expectedMaxTokens: 25000,
			expectNilTemp:     true,
		},
		{
			name:              "legacy model - max effort caps at high budget",
			model:             "claude-3-5-sonnet-20241022",
			reasoning:         &core.Reasoning{Effort: "max"},
			maxOutputTokens:   new(25000),
			expectedThinkType: "enabled",
			expectedBudget:    20000,
			expectedMaxTokens: 25000,
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
			require.NoError(t, err)

			if tt.expectedThinkType == "" {
				assert.Nil(t, result.Thinking)
				assert.Nil(t, result.OutputConfig)

			} else {
				require.NotNil(t, result.Thinking)
				assert.Equal(t, tt.expectedThinkType, result.Thinking.Type)

				if tt.expectedThinkType == "enabled" {
					assert.Equal(t, tt.expectedBudget, result.Thinking.BudgetTokens)
				}
				if tt.expectedThinkType == "adaptive" {
					require.NotNil(t, result.OutputConfig)
					assert.Equal(t, tt.expectedEffort, result.OutputConfig.Effort)
				}
			}

			assert.Equal(t, tt.expectedMaxTokens, result.MaxTokens)

			if tt.expectNilTemp {
				assert.Nil(t, result.Temperature)
			}
		})
	}
}

func TestIsAdaptiveThinkingModel(t *testing.T) {
	tests := []struct {
		model    string
		expected bool
	}{
		{"claude-fable-5", true},
		{"claude-fable-5-20260601", true},
		{"claude-fable-5-1", true},
		{"claude-mythos-5", true},
		{"claude-mythos-5-1-20260901", true},
		{"claude-opus-5", true},
		{"claude-opus-5-20260601", true},
		{"claude-sonnet-5", true},
		{"claude-sonnet-5-20260601", true},
		// Haiku 4.5 predates adaptive thinking and stays on manual budgets.
		{"claude-haiku-4-5-20251001", false},
		{"claude-opus-4-8", true},
		{"claude-opus-4-8-20260301", true},
		{"claude-opus-4-7", true},
		{"claude-opus-4-7-20260101", true},
		// Only the prefixes in adaptiveThinkingPrefixes are adaptive; a
		// hypothetical Sonnet 4.8 must not be assumed adaptive until added.
		{"claude-sonnet-4-8", false},
		{"claude-sonnet-4-8-20260301", false},
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
			got := isAdaptiveThinkingModel(tt.model)
			assert.Equal(t, tt.expected, got)
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
	require.NoError(t, err)
	require.Len(t, result.Messages, 1)

	blocks, ok := result.Messages[0].Content.([]anthropicContentBlock)
	require.True(t, ok, "message content type = %T, want []anthropicContentBlock", result.Messages[0].Content)
	require.Len(t, blocks, 2)
	require.Equal(t, "text", blocks[0].Type)
	require.Equal(t, "Describe the image.", blocks[0].Text, "unexpected first block: %+v", blocks[0])
	require.Equal(t, "image", blocks[1].Type)
	require.NotNil(t, blocks[1].Source)
	require.Equal(t, "image/png", blocks[1].Source.MediaType)
	require.Equal(t, "ZmFrZQ==", blocks[1].Source.Data, "unexpected second block: %+v", blocks[1])
}

func TestConvertToAnthropicRequest_PreservesCacheControlOnContentBlocks(t *testing.T) {
	req := &core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{
				Role: "user",
				Content: []core.ContentPart{
					{
						Type: "text",
						Text: "Reusable prefix.",
						ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
							"cache_control": json.RawMessage(`{"type":"ephemeral"}`),
						}),
					},
					{
						Type: "image_url",
						ImageURL: &core.ImageURLContent{
							URL: "data:image/png;base64,ZmFrZQ==",
						},
						ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
							"cache_control": json.RawMessage(`{"type":"ephemeral"}`),
						}),
					},
				},
			},
		},
	}

	result, err := convertToAnthropicRequest(req)
	require.NoError(t, err)

	blocks, ok := result.Messages[0].Content.([]anthropicContentBlock)
	require.True(t, ok, "message content type = %T, want []anthropicContentBlock", result.Messages[0].Content)
	require.Len(t, blocks, 2)

	for i, block := range blocks {
		require.Equal(t, `{"type":"ephemeral"}`, string(block.CacheControl), "blocks[%d]", i)
	}

	body, err := json.Marshal(result)
	require.NoError(t, err)
	got := strings.Count(string(body), `"cache_control":{"type":"ephemeral"}`)
	require.Equal(t, 2, got, "cache_control blocks in %s", body)
}

func TestConvertToAnthropicRequest_PreservesCacheControlOnSystemBlocks(t *testing.T) {
	req := &core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{
			{
				Role: "system",
				Content: []core.ContentPart{
					{
						Type: "text",
						Text: "Reusable system prefix.",
						ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
							"cache_control": json.RawMessage(`{"type":"ephemeral"}`),
						}),
					},
				},
			},
			{Role: "user", Content: "hello"},
		},
	}

	result, err := convertToAnthropicRequest(req)
	require.NoError(t, err)

	blocks, ok := result.System.([]anthropicContentBlock)
	require.True(t, ok, "System type = %T, want []anthropicContentBlock", result.System)
	require.Len(t, blocks, 1)
	require.Equal(t, "text", blocks[0].Type)
	require.Equal(t, "Reusable system prefix.", blocks[0].Text, "unexpected system block: %+v", blocks[0])
	require.Equal(t, `{"type":"ephemeral"}`, string(blocks[0].CacheControl), "System[0].CacheControl = %s, want ephemeral cache_control", blocks[0].CacheControl)
}

func TestConvertToAnthropicRequest_PreservesCacheControlOnRequestToolsAndToolHistory(t *testing.T) {
	cacheExtra := core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
		"cache_control": json.RawMessage(`{"type":"ephemeral"}`),
	})
	req := &core.ChatRequest{
		Model:       "claude-sonnet-4-5-20250929",
		ExtraFields: cacheExtra,
		Tools: []map[string]any{{
			"type":          "function",
			"function":      map[string]any{"name": "lookup", "parameters": map[string]any{"type": "object"}},
			"cache_control": map[string]any{"type": "ephemeral"},
		}},
		Messages: []core.Message{
			{Role: "assistant", ToolCalls: []core.ToolCall{{
				ID: "tool-1", Type: "function", Function: core.FunctionCall{Name: "lookup", Arguments: `{}`}, ExtraFields: cacheExtra,
			}}},
			{Role: "tool", ToolCallID: "tool-1", Content: "result", ExtraFields: cacheExtra},
		},
	}

	result, err := convertToAnthropicRequest(req)
	require.NoError(t, err)

	want := `{"type":"ephemeral"}`
	got := string(result.CacheControl)
	assert.Equal(t, want, got)
	got = string(result.Tools[0].CacheControl)
	assert.Equal(t, want, got)

	assistantBlocks := result.Messages[0].Content.([]anthropicContentBlock)
	got = string(assistantBlocks[0].CacheControl)
	assert.Equal(t, want, got)

	toolBlocks := result.Messages[1].Content.([]anthropicContentBlock)
	got = string(toolBlocks[0].CacheControl)
	assert.Equal(t, want, got)
}

func TestConvertToAnthropicRequest_PreservesFunctionLevelToolCallCacheControl(t *testing.T) {
	cacheExtra := core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
		"cache_control": json.RawMessage(`{"type":"ephemeral"}`),
	})
	req := &core.ChatRequest{
		Model: "claude-sonnet-4-5-20250929",
		Messages: []core.Message{{Role: "assistant", ToolCalls: []core.ToolCall{{
			ID:   "tool-1",
			Type: "function",
			Function: core.FunctionCall{
				Name:        "lookup",
				Arguments:   `{}`,
				ExtraFields: cacheExtra,
			},
		}}}},
	}

	result, err := convertToAnthropicRequest(req)
	require.NoError(t, err)

	blocks := result.Messages[0].Content.([]anthropicContentBlock)
	got := string(blocks[0].CacheControl)
	require.Equal(t, `{"type":"ephemeral"}`, got)
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
	require.NoError(t, err)
	require.Equal(t, "first system\n\nsecond system", result.System)
}

func TestConvertToAnthropicRequest_RejectsNilRequest(t *testing.T) {
	_, err := convertToAnthropicRequest(nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "anthropic chat request is required")
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
	require.NoError(t, err)

	blocks, ok := result.Messages[0].Content.([]anthropicContentBlock)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].Source, "unexpected image block: %#v", result.Messages[0].Content)
	require.Equal(t, "base64", blocks[0].Source.Type)
	require.Equal(t, "image/png", blocks[0].Source.MediaType)
	require.Equal(t, "ZmFrZQ==", blocks[0].Source.Data, "unexpected image source: %+v", blocks[0].Source)
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
	require.Error(t, err)
	require.Contains(t, err.Error(), "input_audio")
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
	require.NoError(t, err)
	require.Len(t, result.Messages, 1)

	blocks, ok := result.Messages[0].Content.([]anthropicContentBlock)
	require.True(t, ok, "message content type = %T, want []anthropicContentBlock", result.Messages[0].Content)
	require.Len(t, blocks, 1)
	require.Equal(t, "image", blocks[0].Type)
	require.NotNil(t, blocks[0].Source, "unexpected image block: %+v", blocks[0])
	require.Equal(t, "url", blocks[0].Source.Type)
	require.Equal(t, "https://example.com/image.png", blocks[0].Source.URL, "unexpected image source: %+v", blocks[0].Source)
	require.Empty(t, blocks[0].Source.Data)
	require.Empty(t, blocks[0].Source.MediaType, "url source must omit media_type: %+v", blocks[0].Source)
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
	require.NoError(t, err)

	blocks, ok := result.Messages[0].Content.([]anthropicContentBlock)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].Source, "unexpected image block: %#v", result.Messages[0].Content)
	require.Equal(t, "url", blocks[0].Source.Type)
	require.Equal(t, "https://example.com/image.png", blocks[0].Source.URL, "unexpected image source: %+v", blocks[0].Source)
	require.Empty(t, blocks[0].Source.MediaType, "url source must omit media_type: %+v", blocks[0].Source)
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
	require.NoError(t, err)

	blocks, ok := result.Messages[0].Content.([]anthropicContentBlock)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].Source, "unexpected image block: %#v", result.Messages[0].Content)
	require.Equal(t, "url", blocks[0].Source.Type)
	require.Equal(t, "https://example.com/image.svg", blocks[0].Source.URL, "unexpected image source: %+v", blocks[0].Source)
	require.Empty(t, blocks[0].Source.MediaType, "url source must omit media_type: %+v", blocks[0].Source)
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
			require.Error(t, err)
			require.Contains(t, err.Error(), "anthropic chat image_url must be a data: URL or http/https URL")
		})
	}
}

func TestConvertResponsesRequestToAnthropic_RejectsInvalidInputItems(t *testing.T) {
	tests := []struct {
		name  string
		input []any
	}{
		{
			name: "non-object item",
			input: []any{
				"bad-item",
			},
		},
		{
			name: "missing role",
			input: []any{
				map[string]any{
					"content": []any{
						map[string]any{
							"type": "input_text",
							"text": "hello",
						},
					},
				},
			},
		},
		{
			name: "invalid content",
			input: []any{
				map[string]any{
					"role": "user",
					"content": []any{
						map[string]any{
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
			require.Error(t, err)
			require.Contains(t, err.Error(), "invalid responses input item")
		})
	}
}

func TestConvertResponsesRequestToAnthropic_RejectsUnsupportedInputType(t *testing.T) {
	_, err := convertResponsesRequestToAnthropic(&core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: 123,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid responses input: unsupported type")
}

func TestConvertResponsesRequestToAnthropic_TrimsRoleBeforeAppend(t *testing.T) {
	req, err := convertResponsesRequestToAnthropic(&core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: []any{
			map[string]any{
				"role":    "  user  ",
				"content": "hello",
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, req.Messages, 1)
	require.Equal(t, "user", req.Messages[0].Role)
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
	require.NoError(t, err)
	require.Equal(t, "instruction system\n\ninput system", req.System)
}

func TestConvertResponsesRequestToAnthropic_RejectsNilRequest(t *testing.T) {
	_, err := convertResponsesRequestToAnthropic(nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "anthropic responses request is required")
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
	require.NoError(t, err)
	require.Equal(t, "be concise", req.System)
	require.Len(t, req.Messages, 1)
	require.Equal(t, "user", req.Messages[0].Role)
	require.Equal(t, "hello", req.Messages[0].Content)
}

func TestConvertResponsesRequestToAnthropic_PreservesMultimodalImageInput(t *testing.T) {
	req, err := convertResponsesRequestToAnthropic(&core.ResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "input_text",
						"text": "Describe the image.",
					},
					map[string]any{
						"type": "input_image",
						"image_url": map[string]any{
							"url": "data:image/png;base64,ZmFrZQ==",
						},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, req.Messages, 1)

	blocks, ok := req.Messages[0].Content.([]anthropicContentBlock)
	require.True(t, ok, "Messages[0].Content = %#v, want []anthropicContentBlock", req.Messages[0].Content)
	require.Len(t, blocks, 2)
	require.Equal(t, "text", blocks[0].Type)
	require.Equal(t, "Describe the image.", blocks[0].Text, "unexpected text block: %+v", blocks[0])
	require.Equal(t, "image", blocks[1].Type)
	require.NotNil(t, blocks[1].Source, "unexpected image block: %+v", blocks[1])
	require.Equal(t, "base64", blocks[1].Source.Type)
	require.Equal(t, "image/png", blocks[1].Source.MediaType)
	require.Equal(t, "ZmFrZQ==", blocks[1].Source.Data, "unexpected image source: %+v", blocks[1].Source)
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
	require.Error(t, err)
	require.Contains(t, err.Error(), "tool message is missing tool_call_id")
}

func TestEmbeddings_ReturnsUnsupportedError(t *testing.T) {
	p := &Provider{}
	_, err := p.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "text-embedding-3-small",
		Input: "hello",
	})
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, 400, gatewayErr.HTTPStatusCode())
	assert.Contains(t, err.Error(), "anthropic does not support embeddings")
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
	require.NoError(t, err)
	require.Len(t, result.Messages, 1)

	blocks, ok := result.Messages[0].Content.([]anthropicContentBlock)
	require.True(t, ok, "message content type = %T, want []anthropicContentBlock", result.Messages[0].Content)
	require.Len(t, blocks, 2)

	for i, block := range blocks {
		assert.Equal(t, "text", block.Type, "blocks[%d]", i)
	}
	assert.Equal(t, "First part.", blocks[0].Text)
	assert.Equal(t, "Second part.", blocks[1].Text)
}

func TestPassthrough(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusBadRequest, `{"error":{"message":"bad request"}}`)

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
	require.NoError(t, err)

	defer func() {
		_ = resp.Body.Close()
	}()

	sent := capture.Last(t)
	assert.Equal(t, "/messages", sent.Path)
	assert.Empty(t, sent.Query)
	assert.Equal(t, "test-api-key", sent.Header.Get("x-api-key"))
	assert.Equal(t, "2024-10-22", sent.Header.Get("anthropic-version"))
	assert.Equal(t, `{"model":"claude-sonnet-4-5"}`, string(sent.Body))
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, `{"error":{"message":"bad request"}}`, string(body))
}

func TestSetHeadersOAuthToken(t *testing.T) {
	tests := []struct {
		name       string
		key        string
		wantAPIKey string
		wantAuth   string
		wantBeta   string
	}{
		{
			name:       "api key uses x-api-key",
			key:        "sk-ant-api03-abc",
			wantAPIKey: "sk-ant-api03-abc",
		},
		{
			name:     "oauth token uses bearer and oauth beta",
			key:      "sk-ant-oat01-abc",
			wantAuth: "Bearer sk-ant-oat01-abc",
			wantBeta: oauthBetaFlag,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Provider{keys: providers.NewKeyring(tt.key)}
			req := httptest.NewRequest(http.MethodPost, "/messages", nil)
			p.setHeaders(req)
			got := req.Header.Get("x-api-key")
			assert.Equal(t, tt.wantAPIKey, got)
			got = req.Header.Get("Authorization")
			assert.Equal(t, tt.wantAuth, got)
			got = req.Header.Get(anthropicBetaHeader)
			assert.Equal(t, tt.wantBeta, got)
			got = req.Header.Get("anthropic-version")
			assert.Equal(t, anthropicAPIVersion, got)
		})
	}
}

func TestPassthroughOAuthToken(t *testing.T) {
	tests := []struct {
		name       string
		clientBeta string
		wantBeta   []string
	}{
		{
			name:     "no client beta keeps provider oauth beta",
			wantBeta: []string{oauthBetaFlag},
		},
		{
			name:       "client beta merged with oauth flag",
			clientBeta: "claude-code-20250219,interleaved-thinking-2025-05-14",
			wantBeta:   []string{"claude-code-20250219,interleaved-thinking-2025-05-14", oauthBetaFlag},
		},
		{
			name:       "client beta already containing oauth flag unchanged",
			clientBeta: "claude-code-20250219," + oauthBetaFlag,
			wantBeta:   []string{"claude-code-20250219," + oauthBetaFlag},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, `{}`)

			provider := NewWithHTTPClient("sk-ant-oat01-abc", server.Client(), llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			headers := http.Header{"Content-Type": {"application/json"}}
			if tt.clientBeta != "" {
				headers.Set(anthropicBetaHeader, tt.clientBeta)
			}
			resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
				Method:   http.MethodPost,
				Endpoint: "messages",
				Body:     io.NopCloser(strings.NewReader(`{"model":"claude-sonnet-5"}`)),
				Headers:  headers,
			})
			require.NoError(t, err)

			defer func() {
				_ = resp.Body.Close()
			}()

			sent := capture.Last(t).Header
			assert.Equal(t, "Bearer sk-ant-oat01-abc", sent.Get("Authorization"))
			assert.Empty(t, sent.Get("x-api-key"))
			assert.Equal(t, tt.wantBeta, sent.Values(anthropicBetaHeader))
		})
	}
}

// A keyring mixing OAuth tokens and API keys must keep each request
// self-consistent: the oauth beta merge and the auth header always describe
// the credential actually dispatched.
func TestPassthroughMixedKeyringConsistency(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{}`)

	p := &Provider{
		keys:                 providers.NewKeyring("sk-ant-oat01-a", "sk-ant-api03-b"),
		batchResultEndpoints: make(map[string]map[string]string),
	}
	cfg := llmclient.DefaultConfig("anthropic", server.URL)
	p.client = llmclient.NewWithHTTPClient(server.Client(), cfg, p.setHeaders)

	for range 4 {
		headers := http.Header{"Content-Type": {"application/json"}}
		headers.Set(anthropicBetaHeader, "claude-code-20250219")
		resp, err := p.Passthrough(context.Background(), &core.PassthroughRequest{
			Method:   http.MethodPost,
			Endpoint: "messages",
			Body:     io.NopCloser(strings.NewReader(`{"model":"claude-sonnet-5"}`)),
			Headers:  headers,
		})
		require.NoError(t, err)

		_ = resp.Body.Close()
	}

	sawOAuth, sawAPIKey := false, false
	for i, sent := range capture.All() {
		auth := sent.Header.Get("Authorization")
		apiKey := sent.Header.Get("x-api-key")
		beta := strings.Join(sent.Header.Values(anthropicBetaHeader), ",")
		switch {
		case auth != "":
			sawOAuth = true
			assert.Empty(t, apiKey, "request %d: both Authorization and x-api-key set", i)
			assert.Contains(t, beta, oauthBetaFlag, "request %d: OAuth credential without oauth beta", i)
		case apiKey != "":
			sawAPIKey = true
			assert.NotContains(t, beta, oauthBetaFlag, "request %d: API key with oauth beta", i)
		default:
			assert.Fail(t, "no credential sent", "request %d", i)
		}
	}
	require.True(t, sawOAuth)
	require.True(t, sawAPIKey)
}

func TestResolveDefaultMaxTokens(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want int
	}{
		{name: "unset returns fallback", env: "", want: fallbackMaxTokens},
		{name: "valid integer is honoured", env: "16384", want: 16384},
		{name: "whitespace trimmed", env: "  8192  ", want: 8192},
		{name: "zero falls back", env: "0", want: fallbackMaxTokens},
		{name: "negative falls back", env: "-1", want: fallbackMaxTokens},
		{name: "non-numeric falls back", env: "lots", want: fallbackMaxTokens},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(defaultMaxTokensEnvVar, tt.env)
			got := resolveDefaultMaxTokens()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestConvertToAnthropicRequest_HonoursDefaultMaxTokensEnv(t *testing.T) {
	t.Setenv(defaultMaxTokensEnvVar, "32768")
	req := &core.ChatRequest{
		Model: "claude-sonnet-4-6",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
	}
	got, err := convertToAnthropicRequest(req)
	require.NoError(t, err)
	assert.Equal(t, 32768, got.MaxTokens)
}

func TestConvertToAnthropicRequestSystemRoleMessages(t *testing.T) {
	cacheMarker := core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
		"cache_control": json.RawMessage(`{"type":"ephemeral"}`),
	})
	messages := []core.Message{
		{Role: "system", Content: "leading instructions"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
		{Role: "system", Content: []core.ContentPart{
			{Type: "text", Text: "mid-conversation reminder", ExtraFields: cacheMarker},
		}},
		{Role: "user", Content: "continue"},
	}

	t.Run("supported model keeps mid-conversation system in place", func(t *testing.T) {
		out, err := convertToAnthropicRequest(&core.ChatRequest{
			Model:    "claude-fable-5",
			Messages: messages,
		})
		require.NoError(t, err)
		require.Equal(t, "leading instructions", out.System)
		require.Len(t, out.Messages, 4)
		require.Equal(t, "system", out.Messages[2].Role)

		blocks, ok := out.Messages[2].Content.([]anthropicContentBlock)
		require.True(t, ok)
		require.Len(t, blocks, 1, "Messages[2].Content = %#v, want one text block", out.Messages[2].Content)
		require.Equal(t, "mid-conversation reminder", blocks[0].Text)
		require.Equal(t, `{"type":"ephemeral"}`, string(blocks[0].CacheControl), "system block cache_control = %q, want ephemeral marker", blocks[0].CacheControl)
	})

	t.Run("legacy model hoists mid-conversation system into the system prompt", func(t *testing.T) {
		out, err := convertToAnthropicRequest(&core.ChatRequest{
			Model:    "claude-sonnet-4-5-20250929",
			Messages: messages,
		})
		require.NoError(t, err)
		require.Len(t, out.Messages, 3)

		blocks, ok := out.System.([]anthropicContentBlock)
		require.True(t, ok)
		require.Len(t, blocks, 2)
		require.Equal(t, "mid-conversation reminder", blocks[1].Text)
		require.Equal(t, `{"type":"ephemeral"}`, string(blocks[1].CacheControl), "hoisted block = %+v, want reminder with cache_control", blocks[1])
	})
}

func TestSupportsSystemRoleMessages(t *testing.T) {
	for model, want := range map[string]bool{
		"claude-fable-5":             true,
		"claude-mythos-5":            true,
		"claude-opus-4-8-20260301":   true,
		"claude-opus-5-20260115":     true,
		"claude-sonnet-5-20250929":   true,
		"claude-sonnet-4-5-20250929": false,
		"claude-opus-4-6":            false,
		"claude-haiku-4-5-20251001":  false,
		"claude-3-5-haiku-20241022":  false,
	} {
		got := supportsSystemRoleMessages(model)
		assert.Equal(t, want, got)
	}
}

// TestMessagesCacheBreakpointsSurviveTranslation pins the end-to-end invariant
// that broke prompt caching for Claude Code sessions: a /v1/messages request
// whose moving cache_control breakpoint rides on an interleaved system-role
// message must reach the provider with all breakpoints intact and in place.
func TestMessagesCacheBreakpointsSurviveTranslation(t *testing.T) {
	decoded, err := anthropicapi.DecodeMessagesRequest([]byte(`{
		"model": "claude-fable-5",
		"max_tokens": 100,
		"system": [
			{"type":"text","text":"base prompt"},
			{"type":"text","text":"stable context","cache_control":{"type":"ephemeral"}}
		],
		"messages": [
			{"role":"user","content":[{"type":"text","text":"hi"}]},
			{"role":"assistant","content":[{"type":"text","text":"hello"}]},
			{"role":"system","content":[{"type":"text","text":"reminder","cache_control":{"type":"ephemeral"}}]}
		]
	}`))
	require.NoError(t, err)

	chat, err := anthropicapi.ToChatRequest(decoded)
	require.NoError(t, err)

	out, err := convertToAnthropicRequest(chat)
	require.NoError(t, err)

	wire, err := json.Marshal(out)
	require.NoError(t, err)
	got := strings.Count(string(wire), "cache_control")
	require.Equal(t, 2, got, "cache_control markers in %s", wire)

	last := out.Messages[len(out.Messages)-1]
	require.Equal(t, "system", last.Role)

	blocks, ok := last.Content.([]anthropicContentBlock)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	require.NotEmpty(t, blocks[0].CacheControl, "trailing system message = %#v, want one block with cache_control", last.Content)
}

func TestRejectsSamplingParameters(t *testing.T) {
	for model, want := range map[string]bool{
		"claude-fable-5":             true,
		"claude-fable-5-1":           true,
		"claude-mythos-5-1":          true,
		"claude-opus-5":              true,
		"claude-sonnet-5-20260601":   true,
		"claude-opus-4-8":            true,
		"claude-opus-4-7-20260101":   true,
		"claude-opus-4-6":            false,
		"claude-sonnet-4-6":          false,
		"claude-sonnet-4-5-20250929": false,
		"claude-haiku-4-5-20251001":  false,
		"claude-opus-4-75":           false,
		"":                           false,
	} {
		got := rejectsSamplingParameters(model)
		assert.Equal(t, want, got)
	}
}

func TestConvertToAnthropicRequestDropsConflictingSamplingParameter(t *testing.T) {
	ptr := func(v float64) *float64 { return &v }
	tests := []struct {
		name        string
		model       string
		temperature *float64
		topP        *float64
		wantTemp    *float64
		wantTopP    *float64
	}{
		{
			name:        "both sent keeps temperature only",
			model:       "claude-haiku-4-5-20251001",
			temperature: ptr(0.5),
			topP:        ptr(0.9),
			wantTemp:    ptr(0.5),
		},
		{
			name:        "temperature alone is forwarded",
			model:       "claude-haiku-4-5-20251001",
			temperature: ptr(0.5),
			wantTemp:    ptr(0.5),
		},
		{
			name:     "top_p alone is forwarded",
			model:    "claude-haiku-4-5-20251001",
			topP:     ptr(0.9),
			wantTopP: ptr(0.9),
		},
		{
			name:        "models rejecting sampling lose both",
			model:       "claude-opus-4-8",
			temperature: ptr(0.5),
			topP:        ptr(0.9),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := convertToAnthropicRequest(&core.ChatRequest{
				Model:       tt.model,
				Messages:    []core.Message{{Role: "user", Content: "hi"}},
				Temperature: tt.temperature,
				TopP:        tt.topP,
			})
			require.NoError(t, err)
			assert.Equal(t, tt.wantTemp, out.Temperature)
			assert.Equal(t, tt.wantTopP, out.TopP)
		})
	}
}

func TestRejectsForcedToolChoice(t *testing.T) {
	for model, want := range map[string]bool{
		"claude-fable-5-1":          true,
		"claude-fable-5-1-20260901": true,
		"claude-mythos-5-1":         true,
		"claude-fable-5":            false,
		"claude-fable-5-20260601":   false,
		"claude-fable-5-10":         false,
		"claude-opus-5":             false,
		"claude-sonnet-4-6":         false,
	} {
		got := rejectsForcedToolChoice(model)
		assert.Equal(t, want, got)
	}
}

func TestConvertToAnthropicRequest_DropsSamplingForModelsThatRejectIt(t *testing.T) {
	temp := 0.2
	topP := 0.9
	tests := []struct {
		name     string
		model    string
		wantKept bool
	}{
		{name: "fable 5.1 drops temperature and top_p", model: "claude-fable-5-1"},
		{name: "opus 4.7 drops temperature and top_p", model: "claude-opus-4-7"},
		// Models that still accept sampling parameters keep temperature;
		// top_p goes because Anthropic refuses the two together.
		{name: "sonnet 4.6 keeps temperature", model: "claude-sonnet-4-6", wantKept: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := convertToAnthropicRequest(&core.ChatRequest{
				Model:       tt.model,
				Temperature: &temp,
				TopP:        &topP,
				Messages:    []core.Message{{Role: "user", Content: "Hello"}},
			})
			require.NoError(t, err)

			if tt.wantKept {
				require.NotNil(t, out.Temperature)
				require.Equal(t, temp, *out.Temperature)
				require.Nil(t, out.TopP)

				return
			}
			require.Nil(t, out.Temperature)
			require.Nil(t, out.TopP)
		})
	}
}

func TestConvertToAnthropicRequest_RelaxesForcedToolChoice(t *testing.T) {
	tools := []map[string]any{{
		"type": "function",
		"function": map[string]any{
			"name":       "get_weather",
			"parameters": map[string]any{"type": "object"},
		},
	}}
	parallelOff := false
	tests := []struct {
		name            string
		model           string
		toolChoice      any
		system          string
		parallel        *bool
		wantType        string
		wantName        string
		wantInstruction string
	}{
		{
			name:            "required becomes auto with a generic instruction",
			model:           "claude-fable-5-1",
			toolChoice:      "required",
			wantType:        "auto",
			wantInstruction: "You must respond by calling one of the provided tools.",
		},
		{
			name:            "named function becomes auto with a named instruction",
			model:           "claude-mythos-5-1",
			toolChoice:      map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}},
			wantType:        "auto",
			wantInstruction: `You must respond by calling the tool named "get_weather".`,
		},
		{
			name:            "instruction is appended after an existing system prompt",
			model:           "claude-fable-5-1",
			toolChoice:      "required",
			system:          "You are terse.",
			wantType:        "auto",
			wantInstruction: "You are terse.\n\nYou must respond by calling one of the provided tools.",
		},
		{
			name:            "parallel_tool_calls=false survives the downgrade",
			model:           "claude-fable-5-1",
			toolChoice:      "required",
			parallel:        &parallelOff,
			wantType:        "auto",
			wantInstruction: "You must respond by calling one of the provided tools.",
		},
		{
			name:       "auto is forwarded untouched",
			model:      "claude-fable-5-1",
			toolChoice: "auto",
			wantType:   "auto",
		},
		{
			name:       "fable 5 keeps forced tool use",
			model:      "claude-fable-5",
			toolChoice: map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}},
			wantType:   "tool",
			wantName:   "get_weather",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messages := []core.Message{}
			if tt.system != "" {
				messages = append(messages, core.Message{Role: "system", Content: tt.system})
			}
			messages = append(messages, core.Message{Role: "user", Content: "Weather in Warsaw?"})
			out, err := convertToAnthropicRequest(&core.ChatRequest{
				Model:             tt.model,
				Tools:             tools,
				ToolChoice:        tt.toolChoice,
				ParallelToolCalls: tt.parallel,
				Messages:          messages,
			})
			require.NoError(t, err)
			require.NotNil(t, out.ToolChoice)
			assert.Equal(t, tt.wantType, out.ToolChoice.Type)
			assert.Equal(t, tt.wantName, out.ToolChoice.Name)
			if tt.parallel != nil {
				require.NotNil(t, out.ToolChoice.DisableParallelToolUse)
				assert.True(t, *out.ToolChoice.DisableParallelToolUse)
			}
			gotSystem, _ := out.System.(string)
			assert.Equal(t, tt.wantInstruction, gotSystem)
		})
	}
}

func TestConvertToAnthropicRequest_AdaptiveThinkingForDatedFableAndMythos(t *testing.T) {
	for _, model := range []string{"claude-fable-5-1-20260901", "claude-mythos-5-1-20260901"} {
		t.Run("chat "+model, func(t *testing.T) {
			out, err := convertToAnthropicRequest(&core.ChatRequest{
				Model:     model,
				Reasoning: &core.Reasoning{Effort: "high"},
				Messages:  []core.Message{{Role: "user", Content: "Hello"}},
			})
			require.NoError(t, err)

			assertAdaptiveHighEffort(t, out)
		})
		t.Run("responses "+model, func(t *testing.T) {
			out, err := convertResponsesRequestToAnthropic(&core.ResponsesRequest{
				Model:     model,
				Input:     "Hello",
				Reasoning: &core.Reasoning{Effort: "high"},
			})
			require.NoError(t, err)

			assertAdaptiveHighEffort(t, out)
		})
	}
}

func assertAdaptiveHighEffort(t *testing.T, out *anthropicRequest) {
	t.Helper()
	require.NotNil(t, out.Thinking)
	require.Equal(t, "adaptive", out.Thinking.Type)
	require.Equal(t, 0, out.Thinking.BudgetTokens)
	require.NotNil(t, out.OutputConfig)
	require.Equal(t, "high", out.OutputConfig.Effort)
}

// chatStreamDeltas converts an Anthropic SSE stream and returns the delta
// object of every emitted chat chunk.
func chatStreamDeltas(t *testing.T, anthropicSSE string) []map[string]any {
	t.Helper()
	conv := newStreamConverter(io.NopCloser(strings.NewReader(anthropicSSE)), "claude-sonnet-4-5")
	defer conv.Close() //nolint:errcheck

	out, err := io.ReadAll(conv)
	require.NoError(t, err)

	deltas := []map[string]any{}
	for line := range strings.SplitSeq(string(out), "\n") {
		data, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta map[string]any `json:"delta"`
			} `json:"choices"`
		}
		err := json.Unmarshal([]byte(data), &chunk)
		require.NoError(t, err, "unmarshal chunk %q: %v", data, err)

		for _, choice := range chunk.Choices {
			deltas = append(deltas, choice.Delta)
		}
	}
	return deltas
}

// lastExtraContent returns the last extra_content value seen on a delta, which
// is the authoritative cumulative value for a client that keeps only the most
// recent one. Decoding through map[string]any sorts the members, so the
// expected values below are in key order rather than wire order.
func lastExtraContent(deltas []map[string]any) string {
	for _, delta := range slices.Backward(deltas) {
		if extra, ok := delta[core.ExtraContentField]; ok {
			encoded, _ := json.Marshal(extra)
			return string(encoded)
		}
	}
	return ""
}

// A streamed thinking block carries its signature in a signature_delta that
// arrives after the thinking text. Dropping it leaves the client with a
// thinking block Anthropic will refuse on the next turn, so the converter must
// surface it as replay state.
func TestStreamChatCompletion_ThinkingSignatureSurfaced(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_sig","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me "}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"think."}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-1"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Done."}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}

event: message_stop
data: {"type":"message_stop"}
`
	deltas := chatStreamDeltas(t, sse)
	want := `{"anthropic":{"thinking_blocks":[{"signature":"sig-1","thinking":"Let me think.","type":"thinking"}]}}`
	got := lastExtraContent(deltas)
	require.Equal(t, want, got)

	// The signature must not arrive after the text has been streamed: a client
	// closing the thinking block on the first text delta would drop it.
	extraAt, textAt := -1, -1
	for i, delta := range deltas {
		if _, ok := delta[core.ExtraContentField]; ok && extraAt < 0 {
			extraAt = i
		}
		if _, ok := delta["content"]; ok && textAt < 0 {
			textAt = i
		}
	}
	assert.GreaterOrEqual(t, extraAt, 0)
	assert.GreaterOrEqual(t, textAt, 0)
	assert.LessOrEqual(t, extraAt, textAt)
}

// A redacted thinking block arrives whole on content_block_start and has no
// readable text, so nothing but extra_content can carry it.
func TestStreamChatCompletion_RedactedThinkingSurfaced(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_red","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"opaque"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}

event: message_stop
data: {"type":"message_stop"}
`
	want := `{"anthropic":{"thinking_blocks":[{"data":"opaque","type":"redacted_thinking"}]}}`
	got := lastExtraContent(chatStreamDeltas(t, sse))
	require.Equal(t, want, got)
}

// A stream without thinking must stay byte-identical to what it was before:
// no empty extra_content member on any delta.
func TestStreamChatCompletion_NoThinkingNoExtraContent(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_plain","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"stop_reason":null,"usage":{"input_tokens":4,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}
`
	got := lastExtraContent(chatStreamDeltas(t, sse))
	require.Empty(t, got)
}

// responsesStreamEvents converts an Anthropic SSE stream to the Responses
// dialect and returns the decoded events.
func responsesStreamEvents(t *testing.T, anthropicSSE string) []map[string]any {
	t.Helper()
	conv := newResponsesStreamConverter(io.NopCloser(strings.NewReader(anthropicSSE)), "claude-sonnet-4-5")
	defer conv.Close() //nolint:errcheck

	out, err := io.ReadAll(conv)
	require.NoError(t, err)

	events := []map[string]any{}
	for line := range strings.SplitSeq(string(out), "\n") {
		data, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var event map[string]any
		err := json.Unmarshal([]byte(data), &event)
		require.NoError(t, err, "unmarshal event %q: %v", data, err)

		events = append(events, event)
	}
	return events
}

const thinkingResponsesSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_rs","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me "}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"think."}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-1"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Done."}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}

event: message_stop
data: {"type":"message_stop"}
`

// A streamed Responses turn has to expose the same reasoning a non-streamed
// one does, replay state included; otherwise a thinking conversation cannot be
// continued on this dialect.
func TestStreamResponses_ThinkingBecomesReasoningItem(t *testing.T) {
	events := responsesStreamEvents(t, thinkingResponsesSSE)

	var deltas []string
	reasoningAdded, messageAdded := -1, -1
	for i, event := range events {
		switch event["type"] {
		case "response.reasoning_text.delta":
			deltas = append(deltas, event["delta"].(string))
		case "response.output_item.added":
			item := event["item"].(map[string]any)
			if item["type"] == "reasoning" && reasoningAdded < 0 {
				reasoningAdded = i
			}
			if item["type"] == "message" && messageAdded < 0 {
				messageAdded = i
			}
		}
	}
	assert.Equal(t, "Let me think.", strings.Join(deltas, ""))
	require.GreaterOrEqual(t, reasoningAdded, 0)

	if messageAdded >= 0 {
		assert.LessOrEqual(t, reasoningAdded, messageAdded, "reasoning must claim the first output slot")
	}

	final := events[len(events)-1]
	output := final["response"].(map[string]any)["output"].([]any)
	reasoning := output[0].(map[string]any)
	require.Equal(t, "reasoning", reasoning["type"])

	extra, _ := json.Marshal(reasoning["extra_content"])
	want := `{"anthropic":{"thinking_blocks":[{"signature":"sig-1","thinking":"Let me think.","type":"thinking"}]}}`
	assert.Equal(t, want, string(extra))
}

// A stream with no thinking must gain no reasoning item.
func TestStreamResponses_NoThinkingNoReasoningItem(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_plain","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"stop_reason":null,"usage":{"input_tokens":4,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}
`
	for _, event := range responsesStreamEvents(t, sse) {
		assert.False(t, strings.HasPrefix(event["type"].(string), "response.reasoning"), "unexpected reasoning event %v", event["type"])

		if added, ok := event["item"].(map[string]any); ok {
			assert.NotEqual(t, "reasoning", added["type"], "a stream without thinking must not produce a reasoning item")
		}
	}
}

// A redacted thinking block has no readable text, so its reasoning item exists
// only to carry the opaque payload the next turn must replay. It still has to
// be a well-formed item: opened, closed, and present in the terminal output.
func TestStreamResponses_RedactedThinkingBecomesReasoningItem(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_red","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"opaque"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Done."}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}

event: message_stop
data: {"type":"message_stop"}
`
	events := responsesStreamEvents(t, sse)

	added, done := false, false
	for _, event := range events {
		item, ok := event["item"].(map[string]any)
		if !ok || item["type"] != "reasoning" {
			continue
		}
		switch event["type"] {
		case "response.output_item.added":
			added = true
		case "response.output_item.done":
			done = true
		}
	}
	assert.True(t, added)
	assert.True(t, done)

	final := events[len(events)-1]
	output := final["response"].(map[string]any)["output"].([]any)
	reasoning := output[0].(map[string]any)
	require.Equal(t, "reasoning", reasoning["type"])

	extra, _ := json.Marshal(reasoning["extra_content"])
	want := `{"anthropic":{"thinking_blocks":[{"data":"opaque","type":"redacted_thinking"}]}}`
	assert.Equal(t, want, string(extra))

	// The message still follows it, and the redacted item contributes no text.
	assert.Equal(t, "message", output[1].(map[string]any)["type"], "final output[1] = %v, want the assistant message", output[1])
}

// A tool_use-only Anthropic turn has no text, so the Responses output must be
// the function_call alone rather than an empty message item in front of it.
func TestConvertAnthropicResponseToResponses_ToolUseOnlyHasNoEmptyMessage(t *testing.T) {
	resp := &anthropicResponse{
		ID:    "msg_tool",
		Type:  "message",
		Role:  "assistant",
		Model: "claude-sonnet-4-5",
		Content: []anthropicContent{{
			Type:  "tool_use",
			ID:    "toolu_1",
			Name:  "lookup_weather",
			Input: json.RawMessage(`{"city":"Warsaw"}`),
		}},
		StopReason: "tool_use",
	}
	result := convertAnthropicResponseToResponses(resp, "claude-sonnet-4-5")
	require.Len(t, result.Output, 1)
	require.Equal(t, "function_call", result.Output[0].Type)
}

// interleavedThinkingSSE is a single message with two thinking blocks: with
// interleaved thinking the model can think again after it has written text.
const interleavedThinkingSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_two","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"First."}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-1"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Checking."}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"thinking_delta","thinking":"Second."}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"signature_delta","signature":"sig-2"}}

event: content_block_stop
data: {"type":"content_block_stop","index":2}

event: content_block_start
data: {"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup_weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Warsaw\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":3}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":12}}

event: message_stop
data: {"type":"message_stop"}
`

// Every thinking block of the turn must reach the client in order, each one
// published as it closes and the last publication holding the whole turn.
func TestStreamChatCompletion_TwoThinkingBlocks(t *testing.T) {
	deltas := chatStreamDeltas(t, interleavedThinkingSSE)

	var published []string
	for _, delta := range deltas {
		if raw, ok := delta[core.ExtraContentField]; ok {
			encoded, _ := json.Marshal(raw)
			published = append(published, string(encoded))
		}
	}
	want := []string{
		`{"anthropic":{"thinking_blocks":[{"signature":"sig-1","thinking":"First.","type":"thinking"}]}}`,
		`{"anthropic":{"thinking_blocks":[{"signature":"sig-1","thinking":"First.","type":"thinking"},{"signature":"sig-2","thinking":"Second.","type":"thinking"}]}}`,
	}
	require.Equal(t, want, published, "extra_content publications")
}

// A Responses stream has one reasoning item, and its output_item.done cannot
// be taken back. Thinking that arrives after text therefore adds no delta to a
// closed item, but its signature still has to reach the terminal output: the
// SDK builds the next turn from response.output, and Anthropic rejects the
// turn if any block of it lacks its signature.
func TestStreamResponses_ThinkingAfterTextKeepsStreamValid(t *testing.T) {
	events := responsesStreamEvents(t, interleavedThinkingSSE)

	reasoningDone := false
	var lateDeltas []string
	for _, event := range events {
		switch event["type"] {
		case "response.output_item.done":
			if event["item"].(map[string]any)["type"] == "reasoning" {
				reasoningDone = true
			}
		case "response.reasoning_text.delta":
			if reasoningDone {
				lateDeltas = append(lateDeltas, event["delta"].(string))
			}
		}
	}
	require.True(t, reasoningDone)
	assert.Empty(t, lateDeltas)

	final := events[len(events)-1]
	output := final["response"].(map[string]any)["output"].([]any)
	reasoning := output[0].(map[string]any)
	require.Equal(t, "reasoning", reasoning["type"])

	extra, _ := json.Marshal(reasoning["extra_content"])
	want := `{"anthropic":{"thinking_blocks":[{"signature":"sig-1","thinking":"First.","type":"thinking"},{"signature":"sig-2","thinking":"Second.","type":"thinking"}]}}`
	assert.Equal(t, want, string(extra))
	got := output[len(output)-1].(map[string]any)["type"]
	assert.Equal(t, "function_call", got)
}

// A signature is the last delta of a thinking block, so a stream cut between
// it and content_block_stop still holds a block Anthropic will accept back.
// The incomplete terminal output must carry it: the client continues from
// response.output, and losing the signature there loses the turn.
func TestStreamResponses_InterruptedAfterSignatureKeepsReplayState(t *testing.T) {
	sse := `event: message_start
data: {"type":"message_start","message":{"id":"msg_cut","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think."}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-1"}}
`
	events := responsesStreamEvents(t, sse)
	final := events[len(events)-1]
	require.Equal(t, "response.incomplete", final["type"])

	output := final["response"].(map[string]any)["output"].([]any)
	reasoning := output[0].(map[string]any)
	require.Equal(t, "reasoning", reasoning["type"])

	extra, _ := json.Marshal(reasoning["extra_content"])
	want := `{"anthropic":{"thinking_blocks":[{"signature":"sig-1","thinking":"Let me think.","type":"thinking"}]}}`
	assert.Equal(t, want, string(extra))
}

// TestStreamResponses_NormalizedTextStream pins the streamed text lifecycle to
// the shape OpenAI emits: sequence_number on every event, response.in_progress
// after response.created, and the content part opened and closed around the
// item-addressed text deltas.
func TestStreamResponses_NormalizedTextStream(t *testing.T) {
	stream := `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}

`
	converter := newResponsesStreamConverter(io.NopCloser(strings.NewReader(stream)), "claude-sonnet-4-5-20250929")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	events := parseTestSSEEvents(t, string(raw))

	want := []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
		"[DONE]",
	}
	got := make([]string, 0, len(events))
	next := 0
	for _, event := range events {
		if event.Done {
			got = append(got, "[DONE]")
			continue
		}
		got = append(got, event.Name)
		seq, ok := event.Payload["sequence_number"].(float64)
		require.True(t, ok)
		require.Equal(t, next, int(seq), "event %s sequence_number", event.Name)

		next++
	}
	require.Equal(t, want, got, "event order")

	item, _ := events[2].Payload["item"].(map[string]any)
	itemID, _ := item["id"].(string)
	require.NotEmpty(t, itemID, "output_item.added item has no id: %v", item)

	for _, event := range events[3:8] {
		require.Equal(t, itemID, event.Payload["item_id"])
		require.Equal(t, float64(0), event.Payload["output_index"])
		require.Equal(t, float64(0), event.Payload["content_index"], "%s is not addressed to item %q part 0: %v", event.Name, itemID, event.Payload)
	}
	require.Equal(t, "Hello world", events[6].Payload["text"])

	part, _ := events[7].Payload["part"].(map[string]any)
	require.Equal(t, "output_text", part["type"])
	require.Equal(t, "Hello world", part["text"], "content_part.done part = %#v, want full output_text", part)

	inProgress, _ := events[1].Payload["response"].(map[string]any)
	require.Equal(t, "in_progress", inProgress["status"])
	output, ok := inProgress["output"].([]any)
	require.True(t, ok)
	require.Empty(t, output)
}

// TestStreamResponses_NormalizedThinkingToolStream keeps the sequence numbers
// contiguous across a thinking block and a tool call, a turn with no message
// item and therefore no content part.
func TestStreamResponses_NormalizedThinkingToolStream(t *testing.T) {
	stream := `event: message_start
data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me check"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-1"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup_weather","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Warsaw\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}

event: message_stop
data: {"type":"message_stop"}

`
	converter := newResponsesStreamConverter(io.NopCloser(strings.NewReader(stream)), "claude-sonnet-4-5-20250929")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	events := parseTestSSEEvents(t, string(raw))
	require.GreaterOrEqual(t, len(events), 3)
	require.Equal(t, "response.created", events[0].Name)
	require.Equal(t, "response.in_progress", events[1].Name, "stream must open with response.created and response.in_progress, got %v", events)

	next := 0
	for _, event := range events {
		if event.Done {
			continue
		}
		require.False(t, strings.HasPrefix(event.Name, "response.content_part."))
		require.False(t, strings.HasPrefix(event.Name, "response.output_text."), "unexpected %s on a turn without a message item", event.Name)

		seq, ok := event.Payload["sequence_number"].(float64)
		require.True(t, ok)
		require.Equal(t, next, int(seq), "event %s sequence_number", event.Name)

		next++
	}
	last := events[len(events)-2]
	require.Equal(t, "response.completed", last.Name)
}

// TestStreamResponses_CutBeforeMessageStartStillOpens covers an upstream body
// that ends before message_start: the stream must still open with
// response.created and response.in_progress before response.incomplete, so
// stream helpers that snapshot the created response can finish cleanly.
func TestStreamResponses_CutBeforeMessageStartStillOpens(t *testing.T) {
	converter := newResponsesStreamConverter(io.NopCloser(strings.NewReader("")), "claude-sonnet-4-5-20250929")
	raw, err := io.ReadAll(converter)
	require.NoError(t, err)

	events := parseTestSSEEvents(t, string(raw))
	want := []string{"response.created", "response.in_progress", "response.incomplete", "[DONE]"}
	got := make([]string, 0, len(events))
	for i, event := range events {
		if event.Done {
			got = append(got, "[DONE]")
			continue
		}
		got = append(got, event.Name)
		seq, ok := event.Payload["sequence_number"].(float64)
		require.True(t, ok)
		require.Equal(t, i, int(seq), "event %s sequence_number", event.Name)
	}
	require.Equal(t, want, got, "event order")
}
