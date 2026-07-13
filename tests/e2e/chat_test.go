//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestChatCompletion(t *testing.T) {
	t.Run("basic request", func(t *testing.T) {
		payload := defaultChatReq("Hello, how are you?")

		resp := sendChatRequest(t, payload)
		defer closeBody(resp)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		var chatResp core.ChatResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&chatResp))

		require.NotEmpty(t, chatResp.ID)
		assert.Equal(t, "chat.completion", chatResp.Object)
		assert.Equal(t, "gpt-4", chatResp.Model)
		require.Len(t, chatResp.Choices, 1)
		assert.Equal(t, "assistant", chatResp.Choices[0].Message.Role)
		assert.Contains(t, chatResp.Choices[0].Message.Content, "Hello, how are you?")
		assert.Equal(t, "stop", chatResp.Choices[0].FinishReason)
	})

	t.Run("conversation history", func(t *testing.T) {
		mockServer.ResetRequests()

		payload := core.ChatRequest{
			Model: "gpt-4",
			Messages: []core.Message{
				{Role: "system", Content: "You are a helpful assistant."},
				{Role: "user", Content: "What is 2+2?"},
				{Role: "assistant", Content: "4"},
				{Role: "user", Content: "And what is 3+3?"},
			},
		}

		resp := sendChatRequest(t, payload)
		defer closeBody(resp)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		var chatResp core.ChatResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&chatResp))
		require.Len(t, chatResp.Choices, 1)
		assert.Contains(t, chatResp.Choices[0].Message.Content, "And what is 3+3?")

		upstream := requireRecordedChatRequest(t)
		require.Len(t, upstream.Messages, 4)
		assert.Equal(t, "system", upstream.Messages[0].Role)
		assert.Equal(t, "You are a helpful assistant.", upstream.Messages[0].Content)
		assert.Equal(t, "user", upstream.Messages[1].Role)
		assert.Equal(t, "What is 2+2?", upstream.Messages[1].Content)
		assert.Equal(t, "assistant", upstream.Messages[2].Role)
		assert.Equal(t, "4", upstream.Messages[2].Content)
		assert.Equal(t, "user", upstream.Messages[3].Role)
		assert.Equal(t, "And what is 3+3?", upstream.Messages[3].Content)
	})

	t.Run("empty messages", func(t *testing.T) {
		payload := core.ChatRequest{Model: "gpt-4", Messages: []core.Message{}}

		resp := sendChatRequest(t, payload)
		defer closeBody(resp)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		var chatResp core.ChatResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&chatResp))
		require.Len(t, chatResp.Choices, 1)
		assert.Equal(t, "assistant", chatResp.Choices[0].Message.Role)
		assert.Contains(t, chatResp.Choices[0].Message.Content, "How can I help you today?")
	})

	t.Run("multimodal content array", func(t *testing.T) {
		mockServer.ResetRequests()

		payload := core.ChatRequest{
			Model: "gpt-4",
			Messages: []core.Message{
				{
					Role: "user",
					Content: []core.ContentPart{
						{Type: "text", Text: "What is in this image?"},
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

		resp := sendChatRequest(t, payload)
		defer closeBody(resp)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		var chatResp core.ChatResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&chatResp))
		require.Len(t, chatResp.Choices, 1)
		assert.Contains(t, chatResp.Choices[0].Message.Content, "What is in this image?")

		recorded := mockServer.Requests()
		require.Len(t, recorded, 1)
		require.Equal(t, "/chat/completions", recorded[0].Path)

		var upstreamReq core.ChatRequest
		require.NoError(t, json.Unmarshal(recorded[0].Body, &upstreamReq))
		require.Len(t, upstreamReq.Messages, 1)

		parts, ok := upstreamReq.Messages[0].Content.([]core.ContentPart)
		require.True(t, ok, "expected upstream content to preserve multimodal array")
		require.Len(t, parts, 2)
		require.Equal(t, "image_url", parts[1].Type)
		require.NotNil(t, parts[1].ImageURL)
		assert.Equal(t, "https://example.com/image.png", parts[1].ImageURL.URL)
	})

	t.Run("function calling preserves tools and tool_choice", func(t *testing.T) {
		mockServer.ResetRequests()

		parallelToolCalls := false
		payload := core.ChatRequest{
			Model: "gpt-4",
			Messages: []core.Message{
				{Role: "user", Content: "What's the weather in Warsaw?"},
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

		resp := sendChatRequest(t, payload)
		defer closeBody(resp)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		var chatResp core.ChatResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&chatResp))

		require.Len(t, chatResp.Choices, 1)
		assert.Equal(t, "tool_calls", chatResp.Choices[0].FinishReason)
		require.Len(t, chatResp.Choices[0].Message.ToolCalls, 1)
		assert.Equal(t, "lookup_weather", chatResp.Choices[0].Message.ToolCalls[0].Function.Name)

		requests := mockServer.Requests()
		require.NotEmpty(t, requests)

		var upstream core.ChatRequest
		require.NoError(t, json.Unmarshal(requests[len(requests)-1].Body, &upstream))
		require.Len(t, upstream.Tools, 1)
		require.NotNil(t, upstream.ToolChoice)
		require.NotNil(t, upstream.ParallelToolCalls)
		assert.False(t, *upstream.ParallelToolCalls)
	})

	t.Run("tool result messages preserve tool_call_id", func(t *testing.T) {
		mockServer.ResetRequests()

		payload := core.ChatRequest{
			Model: "gpt-4",
			Messages: []core.Message{
				{Role: "user", Content: "What's the weather in Warsaw?"},
				{
					Role: "assistant",
					ToolCalls: []core.ToolCall{
						{
							ID:   "call_mock_123",
							Type: "function",
							Function: core.FunctionCall{
								Name:      "lookup_weather",
								Arguments: `{"city":"Warsaw"}`,
							},
						},
					},
				},
				{Role: "tool", ToolCallID: "call_mock_123", Content: `{"temperature_c":21}`},
			},
		}

		resp := sendChatRequest(t, payload)
		defer closeBody(resp)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		requests := mockServer.Requests()
		require.NotEmpty(t, requests)

		var upstream core.ChatRequest
		require.NoError(t, json.Unmarshal(requests[len(requests)-1].Body, &upstream))
		require.Len(t, upstream.Messages, 3)
		assert.Equal(t, "call_mock_123", upstream.Messages[2].ToolCallID)
		require.Len(t, upstream.Messages[1].ToolCalls, 1)
		assert.Equal(t, "call_mock_123", upstream.Messages[1].ToolCalls[0].ID)
	})
}

func TestChatCompletionParameters(t *testing.T) {
	tests := []struct {
		name           string
		modify         func(*core.ChatRequest)
		assertUpstream func(t *testing.T, upstream core.ChatRequest)
	}{
		{
			name: "with temperature",
			modify: func(r *core.ChatRequest) {
				temp := 0.7
				r.Temperature = &temp
			},
			assertUpstream: func(t *testing.T, upstream core.ChatRequest) {
				t.Helper()
				require.NotNil(t, upstream.Temperature)
				assert.InDelta(t, 0.7, *upstream.Temperature, 0.0001)
			},
		},
		{
			name: "with max_tokens",
			modify: func(r *core.ChatRequest) {
				maxTokens := 100
				r.MaxTokens = &maxTokens
			},
			assertUpstream: func(t *testing.T, upstream core.ChatRequest) {
				t.Helper()
				require.NotNil(t, upstream.MaxTokens)
				assert.Equal(t, 100, *upstream.MaxTokens)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockServer.ResetRequests()

			payload := defaultChatReq("Hello")
			tt.modify(&payload)

			resp := sendChatRequest(t, payload)
			defer closeBody(resp)

			require.Equal(t, http.StatusOK, resp.StatusCode)

			var chatResp core.ChatResponse
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&chatResp))
			require.Len(t, chatResp.Choices, 1)
			assert.Contains(t, chatResp.Choices[0].Message.Content, "Hello")

			upstream := requireRecordedChatRequest(t)
			assert.Equal(t, "gpt-4", upstream.Model)
			tt.assertUpstream(t, upstream)
		})
	}
}

func TestChatCompletionStreaming(t *testing.T) {
	t.Run("basic streaming", func(t *testing.T) {
		payload := defaultChatReq("Count from 1 to 5")
		payload.Stream = true

		resp := sendChatRequest(t, payload)
		defer closeBody(resp)

		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

		chunks := readStreamingResponse(t, resp.Body)
		require.Greater(t, len(chunks), 0)
		assert.True(t, chunks[len(chunks)-1].Done, "Last chunk should be [DONE]")
	})

	t.Run("streaming content", func(t *testing.T) {
		payload := defaultChatReq("Hello")
		payload.Stream = true

		resp := sendChatRequest(t, payload)
		defer closeBody(resp)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		chunks := readStreamingResponse(t, resp.Body)
		content := extractStreamContent(chunks)
		assert.Contains(t, content, "Hello")
	})

	t.Run("streaming tool calls", func(t *testing.T) {
		parallelToolCalls := false
		payload := core.ChatRequest{
			Model:  "gpt-4",
			Stream: true,
			Messages: []core.Message{
				{Role: "user", Content: "What's the weather in Warsaw?"},
			},
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
			ToolChoice:        map[string]any{"type": "function", "function": map[string]any{"name": "lookup_weather"}},
			ParallelToolCalls: &parallelToolCalls,
		}

		resp := sendChatRequest(t, payload)
		defer closeBody(resp)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		chunks := readStreamingResponse(t, resp.Body)
		require.NotEmpty(t, chunks)

		foundToolCall := false
		foundFinishReason := false
		for _, chunk := range chunks {
			if chunk.Done || len(chunk.Choices) == 0 {
				continue
			}
			delta, _ := chunk.Choices[0]["delta"].(map[string]any)
			if toolCalls, ok := delta["tool_calls"].([]any); ok && len(toolCalls) == 1 {
				toolCall, _ := toolCalls[0].(map[string]any)
				function, _ := toolCall["function"].(map[string]any)
				if toolCall["id"] == "call_mock_123" && toolCall["type"] == "function" && function["name"] == "lookup_weather" && function["arguments"] == `{"city":"Warsaw"}` {
					foundToolCall = true
				}
			}
			if chunk.Choices[0]["finish_reason"] == "tool_calls" {
				foundFinishReason = true
			}
		}

		assert.True(t, foundToolCall, "expected streamed tool_call delta")
		assert.True(t, foundFinishReason, "expected final tool_calls finish_reason")
		assert.True(t, chunks[len(chunks)-1].Done, "Last chunk should be [DONE]")
	})
}

func TestChatCompletionErrors(t *testing.T) {
	t.Run("invalid JSON", func(t *testing.T) {
		resp, err := http.Post(gatewayURL+chatCompletionsPath, "application/json",
			strings.NewReader(`{"model": "gpt-4", "messages": invalid}`))
		require.NoError(t, err)
		defer closeBody(resp)

		requireErrorResponse(t, resp, http.StatusBadRequest, core.ErrorTypeInvalidRequest, "invalid request body")
	})

	t.Run("missing model", func(t *testing.T) {
		resp := sendRawChatRequest(t, map[string]any{
			"messages": []map[string]string{{"role": "user", "content": "Hello"}},
		})
		defer closeBody(resp)

		requireErrorResponse(t, resp, http.StatusBadRequest, core.ErrorTypeInvalidRequest, "model is required")
	})

	t.Run("unsupported model", func(t *testing.T) {
		// Gateway validates models against registry before routing
		resp := sendRawChatRequest(t, map[string]any{
			"model":    "invalid-model-xyz",
			"messages": []map[string]string{{"role": "user", "content": "Hello"}},
		})
		defer closeBody(resp)

		requireErrorResponse(t, resp, http.StatusNotFound, core.ErrorTypeNotFound, "unsupported model")
	})
}

func TestHealthAndModels(t *testing.T) {
	t.Run("health endpoint", func(t *testing.T) {
		resp, err := http.Get(gatewayURL + healthPath)
		require.NoError(t, err)
		defer closeBody(resp)

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var health map[string]string
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&health))
		assert.Equal(t, "ok", health["status"])
	})

	t.Run("list models", func(t *testing.T) {
		resp, err := http.Get(gatewayURL + modelsPath)
		require.NoError(t, err)
		defer closeBody(resp)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		var modelsResp core.ModelsResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&modelsResp))

		assert.Equal(t, "list", modelsResp.Object)
		assert.Greater(t, len(modelsResp.Data), 0)
	})
}

func TestChatCompletionConcurrency(t *testing.T) {
	const numRequests = 10

	type result struct {
		statusCode int
		err        error
	}
	results := make(chan result, numRequests)

	for i := range numRequests {
		go func(idx int) {
			payload := defaultChatReq("Hello " + string(rune('A'+idx)))

			resp, err := sendJSONRequestNoT(gatewayURL+chatCompletionsPath, payload)
			if err != nil {
				results <- result{err: err}
				return
			}
			statusCode := resp.StatusCode
			closeBody(resp)
			results <- result{statusCode: statusCode}
		}(i)
	}

	// Collect all results in the main goroutine before asserting
	var errors []error
	successCount := 0
	for range numRequests {
		select {
		case r := <-results:
			if r.err != nil {
				errors = append(errors, r.err)
			} else if r.statusCode == http.StatusOK {
				successCount++
			}
		case <-time.After(30 * time.Second):
			t.Fatal("Timeout waiting for concurrent requests")
		}
	}

	// Perform all assertions in the main goroutine
	require.Empty(t, errors, "Expected no request errors")
	assert.Equal(t, numRequests, successCount)
}

func TestChatCompletionTimeout(t *testing.T) {
	t.Run("client timeout fires while upstream is slow", func(t *testing.T) {
		const (
			upstreamDelay = 2 * time.Second
			clientTimeout = 150 * time.Millisecond
		)

		mockServer.SetResponseDelay(upstreamDelay)
		t.Cleanup(func() { mockServer.SetResponseDelay(0) })

		client := &http.Client{Timeout: clientTimeout}
		body, err := json.Marshal(defaultChatReq("Slow request"))
		require.NoError(t, err)

		start := time.Now()
		resp, err := client.Post(gatewayURL+chatCompletionsPath, "application/json", strings.NewReader(string(body)))
		elapsed := time.Since(start)

		// Expected: client-side timeout fires before the upstream returns.
		if resp != nil {
			closeBody(resp)
		}
		require.Error(t, err, "expected client timeout while upstream is delayed by %s", upstreamDelay)
		assert.True(t,
			errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "Client.Timeout"),
			"expected deadline-exceeded / Client.Timeout error, got: %v", err)
		assert.Less(t, elapsed, upstreamDelay,
			"client timeout (%s) should fire before upstream delay (%s) completes", clientTimeout, upstreamDelay)
	})

	t.Run("fast request succeeds when upstream is responsive", func(t *testing.T) {
		// Sanity check that the delay was cleared and the gateway round-trip is fast.
		client := &http.Client{Timeout: 10 * time.Second}
		body, err := json.Marshal(defaultChatReq("Quick test"))
		require.NoError(t, err)

		start := time.Now()
		resp, err := client.Post(gatewayURL+chatCompletionsPath, "application/json", strings.NewReader(string(body)))
		elapsed := time.Since(start)

		require.NoError(t, err)
		defer closeBody(resp)

		assert.Less(t, elapsed, 5*time.Second)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}
