//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gomodel/internal/core"
)

func TestResponses(t *testing.T) {
	t.Run("basic string input", func(t *testing.T) {
		payload := core.ResponsesRequest{
			Model: "gpt-4.1",
			Input: "What is the capital of France?",
		}

		resp := sendResponsesRequest(t, payload)
		defer closeBody(resp)

		require.Equal(t, http.StatusOK, resp.StatusCode, "Expected 200 OK for basic request")

		var respBody core.ResponsesResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&respBody))

		assert.NotEmpty(t, respBody.ID)
		assert.Equal(t, "response", respBody.Object)
		assert.Equal(t, "gpt-4.1", respBody.Model)
		assert.Equal(t, "completed", respBody.Status)
		assert.NotEmpty(t, respBody.Output)

		if len(respBody.Output) > 0 {
			assert.Equal(t, "message", respBody.Output[0].Type)
			assert.Equal(t, "assistant", respBody.Output[0].Role)
		}
	})

	t.Run("with instructions", func(t *testing.T) {
		payload := core.ResponsesRequest{
			Model:        "gpt-4.1",
			Input:        "Tell me about Go programming",
			Instructions: "You are a helpful programming assistant.",
		}

		resp := sendResponsesRequest(t, payload)
		defer closeBody(resp)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		var respBody core.ResponsesResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&respBody))
		assert.Equal(t, "completed", respBody.Status)
	})

	t.Run("array input conversation", func(t *testing.T) {
		payload := core.ResponsesRequest{
			Model: "gpt-4.1",
			Input: []map[string]interface{}{
				{"role": "user", "content": "What is 2 + 2?"},
				{"role": "assistant", "content": "2 + 2 equals 4."},
				{"role": "user", "content": "And what is 3 + 3?"},
			},
		}

		resp := sendResponsesRequest(t, payload)
		defer closeBody(resp)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		var respBody core.ResponsesResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&respBody))
		assert.Equal(t, "completed", respBody.Status)
	})
}

func TestResponsesParameters(t *testing.T) {
	tests := []struct {
		name           string
		modify         func(*core.ResponsesRequest)
		assertUpstream func(t *testing.T, upstream core.ResponsesRequest)
	}{
		{
			name: "with temperature",
			modify: func(r *core.ResponsesRequest) {
				temp := 0.5
				r.Temperature = &temp
			},
			assertUpstream: func(t *testing.T, upstream core.ResponsesRequest) {
				t.Helper()
				require.NotNil(t, upstream.Temperature)
				assert.InDelta(t, 0.5, *upstream.Temperature, 0.0001)
			},
		},
		{
			name: "with max_output_tokens",
			modify: func(r *core.ResponsesRequest) {
				maxTokens := 100
				r.MaxOutputTokens = &maxTokens
			},
			assertUpstream: func(t *testing.T, upstream core.ResponsesRequest) {
				t.Helper()
				require.NotNil(t, upstream.MaxOutputTokens)
				assert.Equal(t, 100, *upstream.MaxOutputTokens)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockServer.ResetRequests()

			payload := core.ResponsesRequest{
				Model: "gpt-4.1",
				Input: "Hello",
			}
			tt.modify(&payload)

			resp := sendResponsesRequest(t, payload)
			defer closeBody(resp)

			require.Equal(t, http.StatusOK, resp.StatusCode)

			var respBody core.ResponsesResponse
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&respBody))
			assert.Equal(t, "completed", respBody.Status)

			upstream := requireRecordedResponsesRequest(t)
			assert.Equal(t, "gpt-4.1", upstream.Model)
			tt.assertUpstream(t, upstream)
		})
	}
}

func TestResponsesStreaming(t *testing.T) {
	t.Run("basic streaming", func(t *testing.T) {
		payload := core.ResponsesRequest{
			Model:  "gpt-4.1",
			Input:  "Count from 1 to 5",
			Stream: true,
		}

		resp := sendResponsesRequest(t, payload)
		defer closeBody(resp)

		require.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

		events := readResponsesStream(t, resp.Body)
		require.Greater(t, len(events), 0)
		assert.True(t, hasDoneEvent(events), "Should receive done event")
	})

	t.Run("streaming does not inject stream_options", func(t *testing.T) {
		// Regression test for GOM-43: streaming Responses API must not include
		// stream_options.include_usage, which is a Chat Completions-only parameter.
		// The Responses API returns usage in the response.completed event by default.
		mockServer.ResetRequests()

		payload := core.ResponsesRequest{
			Model:  "gpt-4.1",
			Input:  "Hello",
			Stream: true,
		}

		resp := sendResponsesRequest(t, payload)
		defer closeBody(resp)

		require.Equal(t, http.StatusOK, resp.StatusCode, "Streaming responses should succeed without stream_options")
		assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

		events := readResponsesStream(t, resp.Body)
		require.Greater(t, len(events), 0, "Should receive at least one SSE event")
		assert.True(t, hasDoneEvent(events), "Should receive done event")

		recorded := requireRecordedRequest(t, "/responses")
		var upstreamRaw map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(recorded.Body, &upstreamRaw))
		assert.NotContains(t, upstreamRaw, "stream_options")

		var upstream core.ResponsesRequest
		require.NoError(t, json.Unmarshal(recorded.Body, &upstream))
		assert.True(t, upstream.Stream)
	})

	t.Run("streaming content", func(t *testing.T) {
		payload := core.ResponsesRequest{
			Model:  "gpt-4.1",
			Input:  "Hello",
			Stream: true,
		}

		resp := sendResponsesRequest(t, payload)
		defer closeBody(resp)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		events := readResponsesStream(t, resp.Body)
		content := extractResponsesStreamContent(events)
		assert.NotEmpty(t, content)
	})
}

func TestResponsesTools(t *testing.T) {
	tests := []struct {
		name  string
		tools []map[string]interface{}
	}{
		{
			name: "file_search tool",
			tools: []map[string]interface{}{
				{"type": "file_search", "vector_store_ids": []string{"vs_test"}},
			},
		},
		{
			name: "web_search tool",
			tools: []map[string]interface{}{
				{"type": "web_search_preview"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockServer.ResetRequests()

			payload := core.ResponsesRequest{
				Model: "gpt-4.1",
				Input: "Search for information",
				Tools: tt.tools,
			}

			resp := sendResponsesRequest(t, payload)
			defer closeBody(resp)

			require.Equal(t, http.StatusOK, resp.StatusCode)

			var respBody core.ResponsesResponse
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&respBody))
			assert.Equal(t, "completed", respBody.Status)

			upstream := requireRecordedResponsesRequest(t)
			require.Len(t, upstream.Tools, 1)
			assert.Equal(t, tt.tools[0]["type"], upstream.Tools[0]["type"])
			assert.Equal(t, "Search for information", upstream.Input)
		})
	}
}

func TestResponsesErrors(t *testing.T) {
	t.Run("invalid JSON", func(t *testing.T) {
		resp, err := http.Post(gatewayURL+responsesPath, "application/json",
			strings.NewReader(`{"model": "gpt-4.1", "input": invalid}`))
		require.NoError(t, err)
		defer closeBody(resp)

		requireErrorResponse(t, resp, http.StatusBadRequest, core.ErrorTypeInvalidRequest, "invalid request body")
	})

	t.Run("missing model", func(t *testing.T) {
		resp := sendRawResponsesRequest(t, map[string]interface{}{"input": "Hello"})
		defer closeBody(resp)

		requireErrorResponse(t, resp, http.StatusBadRequest, core.ErrorTypeInvalidRequest, "model is required")
	})

	t.Run("empty input", func(t *testing.T) {
		payload := core.ResponsesRequest{Model: "gpt-4.1", Input: ""}

		resp := sendResponsesRequest(t, payload)
		defer closeBody(resp)

		require.Equal(t, http.StatusOK, resp.StatusCode)

		var respBody core.ResponsesResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&respBody))
		assert.Equal(t, "completed", respBody.Status)
	})

	t.Run("invalid model", func(t *testing.T) {
		payload := core.ResponsesRequest{Model: "invalid-model-xyz", Input: "Hello"}

		resp := sendResponsesRequest(t, payload)
		defer closeBody(resp)

		requireErrorResponse(t, resp, http.StatusBadRequest, core.ErrorTypeInvalidRequest, "unsupported model")
	})
}

func TestResponsesUsage(t *testing.T) {
	payload := core.ResponsesRequest{
		Model: "gpt-4.1",
		Input: "Hello, how are you?",
	}

	resp := sendResponsesRequest(t, payload)
	defer closeBody(resp)

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var respBody core.ResponsesResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&respBody))

	if respBody.Usage != nil {
		assert.Greater(t, respBody.Usage.InputTokens, 0)
		assert.Greater(t, respBody.Usage.OutputTokens, 0)
		assert.Equal(t, respBody.Usage.InputTokens+respBody.Usage.OutputTokens, respBody.Usage.TotalTokens)
	}
}

func TestResponsesMultimodal(t *testing.T) {
	mockServer.ResetRequests()

	payload := core.ResponsesRequest{
		Model: "gpt-4.1",
		Input: []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "input_text", "text": "What's in this image?"},
					{"type": "input_image", "image_url": map[string]string{"url": "https://example.com/image.jpg"}},
				},
			},
		},
	}

	resp := sendResponsesRequest(t, payload)
	defer closeBody(resp)

	require.Equal(t, http.StatusOK, resp.StatusCode)

	var respBody core.ResponsesResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&respBody))
	assert.Equal(t, "completed", respBody.Status)

	recorded := requireRecordedRequest(t, "/responses")
	var upstreamRaw struct {
		Input []struct {
			Role    string `json:"role"`
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text,omitempty"`
				ImageURL *struct {
					URL string `json:"url"`
				} `json:"image_url,omitempty"`
			} `json:"content"`
		} `json:"input"`
	}
	require.NoError(t, json.Unmarshal(recorded.Body, &upstreamRaw))
	require.Len(t, upstreamRaw.Input, 1)
	assert.Equal(t, "user", upstreamRaw.Input[0].Role)
	require.Len(t, upstreamRaw.Input[0].Content, 2)
	assert.Equal(t, "input_text", upstreamRaw.Input[0].Content[0].Type)
	assert.Equal(t, "What's in this image?", upstreamRaw.Input[0].Content[0].Text)
	assert.Equal(t, "input_image", upstreamRaw.Input[0].Content[1].Type)
	require.NotNil(t, upstreamRaw.Input[0].Content[1].ImageURL)
	assert.Equal(t, "https://example.com/image.jpg", upstreamRaw.Input[0].Content[1].ImageURL.URL)
}

func TestResponsesConcurrency(t *testing.T) {
	const numRequests = 5

	type result struct {
		statusCode int
		err        error
	}
	results := make(chan result, numRequests)

	for i := 0; i < numRequests; i++ {
		go func(idx int) {
			payload := core.ResponsesRequest{
				Model: "gpt-4.1",
				Input: "Quick test " + string(rune('A'+idx)),
			}

			resp, err := sendJSONRequestNoT(gatewayURL+responsesPath, payload)
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
	for i := 0; i < numRequests; i++ {
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
