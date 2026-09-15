package xai

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAPIKey = "test-api-key"

// newTestProvider builds a provider pointed at baseURL with the default client.
func newTestProvider(baseURL string) *Provider {
	provider := NewWithHTTPClient(testAPIKey, nil, llmclient.Hooks{})
	provider.SetBaseURL(baseURL)
	return provider
}

// statusServer answers every request with status and body, so a table can
// drive success and error cases through the same recorder.
func statusServer(t *testing.T, status int, body string) (string, *providertest.Capture) {
	t.Helper()
	server, capture := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
	return server.URL, capture
}

// blockUntilCancelled never answers, so a cancelled context is the only way out.
func blockUntilCancelled(w http.ResponseWriter, r *http.Request) {
	<-r.Context().Done()
	w.WriteHeader(http.StatusRequestTimeout)
}

// customHeaderRoundTripper is a RoundTripper that injects a custom header
type customHeaderRoundTripper struct {
	transport http.RoundTripper
	headerKey string
	headerVal string
}

func (c *customHeaderRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set(c.headerKey, c.headerVal)
	return c.transport.RoundTrip(req)
}

func TestNewWithHTTPClient_UsesCustomClient(t *testing.T) {
	const customHeaderKey = "X-Custom-Test-Header"
	const customHeaderVal = "custom-test-value"

	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	customClient := &http.Client{
		Transport: &customHeaderRoundTripper{
			transport: http.DefaultTransport,
			headerKey: customHeaderKey,
			headerVal: customHeaderVal,
		},
	}
	provider := NewWithHTTPClient(testAPIKey, customClient, llmclient.Hooks{})
	assert.Equal(t, testAPIKey, provider.keys.Primary())
	provider.SetBaseURL(server.URL)

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "grok-2",
		Messages: []core.Message{{Role: "user", Content: "Hello"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "chatcmpl-test", resp.ID)
	assert.Equal(t, customHeaderVal, capture.Last(t).Header.Get(customHeaderKey))
}

func TestChatCompletion_ForwardsXGrokConvIDFromSnapshot(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := newTestProvider(server.URL)

	ctx := core.WithRequestSnapshot(context.Background(), core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		map[string][]string{"x-grok-conv-id": {"client-conv-123"}},
		"application/json",
		nil,
		false,
		"req-123",
		nil,
	))
	_, err := provider.ChatCompletion(ctx, &core.ChatRequest{
		Model:    "grok-2",
		Messages: []core.Message{{Role: "user", Content: "Hello"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "client-conv-123", capture.Last(t).Header.Get(grokConvIDHeader))
}

func TestChatCompletion_GeneratesStableXGrokConvIDWhenMissing(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := newTestProvider(server.URL)

	initialMessages := []core.Message{
		{Role: "system", Content: "Reply with the requested marker only."},
		{Role: "user", Content: "Use this fixed reference text for the cache anchor."},
	}
	req1 := &core.ChatRequest{Model: "grok-2", Messages: initialMessages}
	req2 := &core.ChatRequest{
		Model: "grok-2",
		Messages: append(append([]core.Message{}, initialMessages...),
			core.Message{Role: "assistant", Content: "marker one"},
			core.Message{Role: "user", Content: "Now reply with marker two."},
		),
	}
	_, err := provider.ChatCompletion(context.Background(), req1)
	require.NoError(t, err)
	_, err = provider.ChatCompletion(context.Background(), req2)
	require.NoError(t, err)

	requests := capture.All()
	require.Len(t, requests, 2)
	first := requests[0].Header.Get(grokConvIDHeader)
	assert.True(t, strings.HasPrefix(first, "gomodel-"), "generated x-grok-conv-id = %q, want gomodel-*", first)
	assert.Equal(t, first, requests[1].Header.Get(grokConvIDHeader))
}

func TestStreamChatCompletion_ForwardsXGrokConvIDFromSnapshot(t *testing.T) {
	server, capture := providertest.SSEServer(t, "data: [DONE]\n\n")
	provider := newTestProvider(server.URL)

	ctx := core.WithRequestSnapshot(context.Background(), core.NewRequestSnapshot(
		http.MethodPost,
		"/v1/chat/completions",
		nil,
		nil,
		map[string][]string{"X-Grok-Conv-Id": {"stream-conv-123"}},
		"application/json",
		nil,
		false,
		"req-123",
		nil,
	))
	body, err := provider.StreamChatCompletion(ctx, &core.ChatRequest{
		Model:    "grok-2",
		Messages: []core.Message{{Role: "user", Content: "Hello"}},
	})
	require.NoError(t, err)
	defer func() { _ = body.Close() }()
	assert.Equal(t, "stream-conv-123", capture.Last(t).Header.Get(grokConvIDHeader))
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
				"model": "grok-2",
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
			checkResponse: func(t *testing.T, resp *core.ChatResponse) {
				assert.Equal(t, "chatcmpl-123", resp.ID)
				assert.Equal(t, "grok-2", resp.Model)
				require.Len(t, resp.Choices, 1)
				assert.Equal(t, "Hello! How can I help you today?", resp.Choices[0].Message.Content)
				assert.Equal(t, 10, resp.Usage.PromptTokens)
				assert.Equal(t, 20, resp.Usage.CompletionTokens)
				assert.Equal(t, 30, resp.Usage.TotalTokens)
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
			baseURL, capture := statusServer(t, tt.statusCode, tt.responseBody)
			provider := newTestProvider(baseURL)

			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:    "grok-2",
				Messages: []core.Message{{Role: "user", Content: "Hello"}},
			})

			req := capture.Last(t)
			assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
			assert.Equal(t, "Bearer "+testAPIKey, req.Header.Get("Authorization"))
			assert.Equal(t, "grok-2", req.JSON(t)["model"])

			if tt.expectedError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			tt.checkResponse(t, resp)
		})
	}
}

func TestResponsesDropsMetadata(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]string
		stream   bool
	}{
		{name: "non-streaming drops metadata", metadata: map[string]string{"team": "alpha"}},
		{name: "streaming drops metadata", metadata: map[string]string{"team": "alpha"}, stream: true},
		{name: "no metadata is a no-op", metadata: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, `{"id":"resp_1","object":"response","status":"completed"}`)
			provider := newTestProvider(server.URL)

			req := &core.ResponsesRequest{Model: "grok-4.3", Metadata: tt.metadata}
			var err error
			if tt.stream {
				var stream io.ReadCloser
				stream, err = provider.StreamResponses(context.Background(), req)
				if stream != nil {
					_ = stream.Close()
				}
			} else {
				_, err = provider.Responses(context.Background(), req)
			}
			require.NoError(t, err)
			assert.NotContains(t, capture.Last(t).JSON(t), "metadata")
			if len(tt.metadata) > 0 {
				assert.NotNil(t, req.Metadata, "caller request was mutated; metadata must survive for the client echo")
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
			responseBody: `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"grok-2","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"grok-2","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":null}]}

data: [DONE]
`,
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
			baseURL, capture := statusServer(t, tt.statusCode, tt.responseBody)
			provider := newTestProvider(baseURL)

			body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
				Model:    "grok-2",
				Messages: []core.Message{{Role: "user", Content: "Hello"}},
			})

			req := capture.Last(t)
			assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
			assert.Equal(t, "Bearer "+testAPIKey, req.Header.Get("Authorization"))
			assert.Equal(t, true, req.JSON(t)["stream"])

			if tt.expectedError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, body)
			defer func() { _ = body.Close() }()

			respBody, err := io.ReadAll(body)
			require.NoError(t, err)
			assert.Equal(t, tt.responseBody, string(respBody))
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
						"id": "grok-2",
						"object": "model",
						"created": 1687882411,
						"owned_by": "xai"
					},
					{
						"id": "grok-2-mini",
						"object": "model",
						"created": 1687882410,
						"owned_by": "xai"
					}
				]
			}`,
			checkResponse: func(t *testing.T, resp *core.ModelsResponse) {
				assert.Equal(t, "list", resp.Object)
				require.Len(t, resp.Data, 2)
				assert.Equal(t, "grok-2", resp.Data[0].ID)
				assert.Equal(t, "xai", resp.Data[0].OwnedBy)
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
			baseURL, capture := statusServer(t, tt.statusCode, tt.responseBody)
			provider := newTestProvider(baseURL)

			resp, err := provider.ListModels(context.Background())

			req := capture.Last(t)
			assert.Equal(t, http.MethodGet, req.Method)
			assert.Equal(t, "/models", req.Path)
			assert.Equal(t, "Bearer "+testAPIKey, req.Header.Get("Authorization"))

			if tt.expectedError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			tt.checkResponse(t, resp)
		})
	}
}

func TestChatCompletionWithContext(t *testing.T) {
	server, _ := providertest.Server(t, blockUntilCancelled)
	provider := newTestProvider(server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := provider.ChatCompletion(ctx, &core.ChatRequest{
		Model:    "grok-2",
		Messages: []core.Message{{Role: "user", Content: "Hello"}},
	})
	assert.Error(t, err)
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
				"model": "grok-2",
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
			checkResponse: func(t *testing.T, resp *core.ResponsesResponse) {
				assert.Equal(t, "resp_123", resp.ID)
				assert.Equal(t, "response", resp.Object)
				assert.Equal(t, "grok-2", resp.Model)
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
			baseURL, capture := statusServer(t, tt.statusCode, tt.responseBody)
			provider := newTestProvider(baseURL)

			resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
				Model: "grok-2",
				Input: "Hello",
			})

			req := capture.Last(t)
			assert.Equal(t, "/responses", req.Path)
			assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
			assert.Equal(t, "Bearer "+testAPIKey, req.Header.Get("Authorization"))
			assert.Equal(t, "grok-2", req.JSON(t)["model"])

			if tt.expectedError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			tt.checkResponse(t, resp)
		})
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
data: {"type":"response.created","response":{"id":"resp_123","object":"response","status":"in_progress","model":"grok-2"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hello"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"!"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_123","object":"response","status":"completed","model":"grok-2"}}
`,
			checkStream: func(t *testing.T, body io.ReadCloser) {
				require.NotNil(t, body)
				defer func() { _ = body.Close() }()

				respBody, err := io.ReadAll(body)
				require.NoError(t, err)
				responseStr := string(respBody)
				assert.Contains(t, responseStr, "response.created")
				assert.Contains(t, responseStr, "response.output_text.delta")
				assert.Contains(t, responseStr, "[DONE]")
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
			baseURL, capture := statusServer(t, tt.statusCode, tt.responseBody)
			provider := newTestProvider(baseURL)

			body, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
				Model: "grok-2",
				Input: "Hello",
			})

			req := capture.Last(t)
			assert.Equal(t, "/responses", req.Path)
			assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
			assert.Equal(t, "Bearer "+testAPIKey, req.Header.Get("Authorization"))
			assert.Equal(t, true, req.JSON(t)["stream"])

			if tt.expectedError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			tt.checkStream(t, body)
		})
	}
}

func TestResponsesWithContext(t *testing.T) {
	server, _ := providertest.Server(t, blockUntilCancelled)
	provider := newTestProvider(server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := provider.Responses(ctx, &core.ResponsesRequest{Model: "grok-2", Input: "Hello"})
	assert.Error(t, err)
}

func TestChatCompletion_MapsReasoningToXAIReasoningEffort(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := newTestProvider(server.URL)

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:     "grok-4.5",
		Messages:  []core.Message{{Role: "user", Content: "hi"}},
		Reasoning: &core.Reasoning{Effort: "medium"},
	})
	require.NoError(t, err)

	sent := capture.Last(t).JSON(t)
	assert.NotContains(t, sent, "reasoning")
	assert.Equal(t, "medium", sent["reasoning_effort"])
}

func TestChatCompletion_OmitsReasoningEffortWhenReasoningAbsent(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := newTestProvider(server.URL)

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "grok-4.5",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)

	sent := capture.Last(t).JSON(t)
	assert.NotContains(t, sent, "reasoning_effort")
	assert.NotContains(t, sent, "reasoning")
}

func TestNormalizeReasoningEffort(t *testing.T) {
	tests := []struct {
		model  string
		effort string
		want   string
	}{
		{"grok-4.5", "low", "low"},
		{"grok-4.5", "medium", "medium"},
		{"grok-4.5", "high", "high"},
		{"grok-4.5", "none", "none"},
		{"grok-4.5", " High ", "high"},
		{"grok-4.5", "xhigh", "high"},
		{"grok-4.5", "max", "high"},
		{"grok-4.20-multi-agent-0309", "xhigh", "xhigh"},
		{"grok-4.20-multi-agent-0309", "max", "xhigh"},
		{"grok-4.20-multi-agent-0309", "low", "low"},
		{"grok-4.6", "xhigh", "xhigh"},
		{"grok-4.6", "max", "xhigh"},
		{"grok-4.6", "high", "high"},
		{"grok-4.6-latest", "xhigh", "xhigh"},
		{"grok-4.7", "xhigh", "xhigh"},
		{"grok-4.10", "xhigh", "xhigh"},
		{"grok-4.foo", "xhigh", "high"},
		{"grok-4.20-0309-reasoning", "xhigh", "high"},
		{"grok-4.20-0309-reasoning", "max", "high"},
		{"grok-4.3", "xhigh", "high"},
		{"grok-4.5", "custom-level", "custom-level"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, normalizeReasoningEffort(tt.model, tt.effort), "normalizeReasoningEffort(%q, %q)", tt.model, tt.effort)
	}
}

func TestChatCompletion_DropsReasoningEffortForModelsThatRejectIt(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		effort     string
		wantEffort string // "" means the field must be absent
	}{
		{name: "non-reasoning variant", model: "grok-4.20-0309-non-reasoning", effort: "medium"},
		{name: "reasoning variant keeps it", model: "grok-4.20-0309-reasoning", effort: "medium", wantEffort: "medium"},
		{name: "grok-build has a fixed effort", model: "grok-build-0.1", effort: "high"},
		{name: "grok-3 rejects it", model: "grok-3", effort: "high"},
		{name: "grok-3-mini takes it", model: "grok-3-mini", effort: "high", wantEffort: "high"},
		{name: "grok-2 rejects it", model: "grok-2-1212", effort: "low"},
		{name: "namespaced id", model: "xai/grok-4.20-0309-non-reasoning", effort: "low"},
		{name: "unknown models keep it", model: "grok-99-new", effort: "low", wantEffort: "low"},
		{name: "grok-4.5 keeps it", model: "grok-4.5", effort: "low", wantEffort: "low"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
			provider := newTestProvider(server.URL)

			_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:     tt.model,
				Messages:  []core.Message{{Role: "user", Content: "hi"}},
				Reasoning: &core.Reasoning{Effort: tt.effort},
			})
			require.NoError(t, err)

			sent := capture.Last(t).JSON(t)
			assert.NotContains(t, sent, "reasoning")
			if tt.wantEffort == "" {
				assert.NotContains(t, sent, "reasoning_effort")
				return
			}
			assert.Equal(t, tt.wantEffort, sent["reasoning_effort"])
		})
	}
}

func TestChatCompletion_DropsEmptyReasoningObject(t *testing.T) {
	for _, effort := range []string{"", " \t "} {
		t.Run("effort="+effort, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
			provider := newTestProvider(server.URL)

			_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:     "grok-4.5",
				Messages:  []core.Message{{Role: "user", Content: "hi"}},
				Reasoning: &core.Reasoning{Effort: effort},
			})
			require.NoError(t, err)

			sent := capture.Last(t).JSON(t)
			assert.NotContains(t, sent, "reasoning")
			assert.NotContains(t, sent, "reasoning_effort")
		})
	}
}
