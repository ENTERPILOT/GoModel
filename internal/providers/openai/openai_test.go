package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAPIKey = "test-api-key"

// responsesReplyJSON is a completed Responses API reply with one text output.
const responsesReplyJSON = `{
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
		"content": [{"type": "output_text", "text": "Hello!"}]
	}]
}`

// assertAuthorized checks that the recorded upstream request carried the test
// API key as a bearer token.
func assertAuthorized(t *testing.T, req providertest.Recorded) {
	t.Helper()
	assert.Equal(t, "Bearer "+testAPIKey, req.Header.Get("Authorization"))
}

// markMutated is a request mutator that tags every upstream request with a
// header and a query parameter so tests can see the mutator ran.
func markMutated(req *llmclient.Request) {
	if req.Headers == nil {
		req.Headers = make(http.Header)
	}
	req.Headers.Set("X-Test-Mutated", "yes")

	endpoint, err := url.Parse(req.Endpoint)
	if err != nil {
		panic(err)
	}
	query := endpoint.Query()
	query.Set("mutated", "1")
	endpoint.RawQuery = query.Encode()
	req.Endpoint = endpoint.String()
}

func TestNew(t *testing.T) {
	provider := NewWithHTTPClient(testAPIKey, nil, llmclient.Hooks{})
	assert.Equal(t, testAPIKey, provider.keys.Primary())
	assert.NotNil(t, provider.client, "nil http client should fall back to a default")
}

func TestNilRequests_ReturnInvalidRequestError(t *testing.T) {
	provider := NewWithHTTPClient(testAPIKey, nil, llmclient.Hooks{})

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
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, tt.call(), &gatewayErr)
			assert.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
		})
	}
}

func TestCompatibleProvider_FileHelpersApplyRequestMutator(t *testing.T) {
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
			server, capture := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
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
			})

			provider := NewCompatibleProviderWithHTTPClient(testAPIKey, server.Client(), llmclient.Hooks{}, CompatibleProviderConfig{
				ProviderName: "test",
				BaseURL:      server.URL,
			})
			provider.SetRequestMutator(markMutated)
			require.NoError(t, tt.call(provider))

			req := capture.Last(t)
			assert.Equal(t, "yes", req.Header.Get("X-Test-Mutated"))
			assert.Equal(t, "1", req.Query.Get("mutated"))
		})
	}
}

func TestCompatibleProvider_GetBatchResultsAppliesRequestMutator(t *testing.T) {
	server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/batches/batch_1": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"batch_1","status":"completed","output_file_id":"file_1","endpoint":"/v1/chat/completions"}`))
		},
		"/files/file_1/content": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/jsonl")
			_, _ = w.Write([]byte(`{"custom_id":"ok-1","response":{"status_code":200,"url":"/v1/chat/completions","body":{"id":"resp-1","model":"gpt-4o-mini"}}}`))
		},
	})

	provider := NewCompatibleProviderWithHTTPClient(testAPIKey, server.Client(), llmclient.Hooks{}, CompatibleProviderConfig{
		ProviderName: "test",
		BaseURL:      server.URL,
	})
	provider.SetRequestMutator(markMutated)

	_, err := provider.GetBatchResults(context.Background(), "batch_1")
	require.NoError(t, err)

	requests := capture.All()
	require.Len(t, requests, 2)
	for _, req := range requests {
		assert.Equal(t, "1", req.Query.Get("mutated"), "%s not mutated", req.Path)
		assert.Equal(t, "yes", req.Header.Get("X-Test-Mutated"), "%s not mutated", req.Path)
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
			checkResponse: func(t *testing.T, resp *core.ChatResponse) {
				assert.Equal(t, "chatcmpl-123", resp.ID)
				assert.Equal(t, "gpt-4o", resp.Model)
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
			server, capture := providertest.JSONServer(t, tt.statusCode, tt.responseBody)
			provider := NewWithHTTPClient(testAPIKey, nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:    "gpt-4o",
				Messages: []core.Message{{Role: "user", Content: "Hello"}},
			})

			req := capture.Last(t)
			assertAuthorized(t, req)
			assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
			assert.Equal(t, "gpt-4o", req.JSON(t)["model"])

			if tt.expectedError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			tt.checkResponse(t, resp)
		})
	}
}

func TestChatCompletion_PreservesMultimodalContent(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := NewWithHTTPClient(testAPIKey, server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gpt-4o-mini",
		Messages: []core.Message{
			{
				Role: "user",
				Content: []core.ContentPart{
					{Type: "text", Text: "Describe the image."},
					{
						Type:     "image_url",
						ImageURL: &core.ImageURLContent{URL: "https://example.com/image.png"},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, providertest.Reply, resp.Choices[0].Message.Content)

	messages, ok := capture.Last(t).JSON(t)["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 1)
	message, ok := messages[0].(map[string]any)
	require.True(t, ok, "message type = %T", messages[0])
	content, ok := message["content"].([]any)
	require.True(t, ok, "content type = %T, want array", message["content"])
	require.Len(t, content, 2)
	second, ok := content[1].(map[string]any)
	require.True(t, ok, "second part type = %T", content[1])
	assert.Equal(t, "image_url", second["type"])
}

func TestChatCompletion_PreservesUnknownTopLevelFields(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := NewWithHTTPClient(testAPIKey, server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "gpt-5-mini",
		Messages: []core.Message{{Role: "user", Content: "Return JSON."}},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"response_format": json.RawMessage(`{
				"type":"json_schema",
				"json_schema":{
					"name":"math_response",
					"schema":{"type":"object","properties":{"answer":{"type":"string"}}}
				}
			}`),
		}),
	})
	require.NoError(t, err)
	assert.Equal(t, providertest.Reply, resp.Choices[0].Message.Content)

	sent := capture.Last(t).JSON(t)
	responseFormat, ok := sent["response_format"].(map[string]any)
	require.True(t, ok, "response_format = %#v, want object", sent["response_format"])
	assert.Equal(t, "json_schema", responseFormat["type"])
}

func TestChatCompletion_PreservesUnknownNestedFields(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := NewWithHTTPClient(testAPIKey, server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gpt-5-mini",
		Messages: []core.Message{
			{
				Role:        "user",
				Content:     []core.ContentPart{{Type: "text", Text: "hello", ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"cache_control": json.RawMessage(`{"type":"ephemeral"}`)})}},
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"name": json.RawMessage(`"alice"`)}),
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, providertest.Reply, resp.Choices[0].Message.Content)

	messages, ok := capture.Last(t).JSON(t)["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 1)
	message, ok := messages[0].(map[string]any)
	require.True(t, ok, "messages[0] = %#v, want object", messages[0])
	assert.Equal(t, "alice", message["name"])
	content, ok := message["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 1)
	part, ok := content[0].(map[string]any)
	require.True(t, ok, "messages[0].content[0] = %#v, want object", content[0])
	_, ok = part["cache_control"].(map[string]any)
	assert.True(t, ok, "messages[0].content[0].cache_control = %#v, want object", part["cache_control"])
}

func TestChatCompletion_PreservesUnknownTopLevelFieldsForOSeries(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := NewWithHTTPClient(testAPIKey, server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	maxTokens := 128
	temperature := 0.7
	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:       "o3-mini",
		Temperature: &temperature,
		MaxTokens:   &maxTokens,
		Messages:    []core.Message{{Role: "user", Content: "Return JSON."}},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"response_format": json.RawMessage(`{
				"type":"json_schema",
				"json_schema":{
					"name":"math_response",
					"schema":{"type":"object","properties":{"answer":{"type":"string"}}}
				}
			}`),
		}),
	})
	require.NoError(t, err)
	assert.Equal(t, providertest.Reply, resp.Choices[0].Message.Content)

	sent := capture.Last(t).JSON(t)
	assert.NotContains(t, sent, "temperature", "temperature should be removed for o-series models")
	assert.Equal(t, float64(128), sent["max_completion_tokens"])
	responseFormat, ok := sent["response_format"].(map[string]any)
	require.True(t, ok, "response_format = %#v, want object", sent["response_format"])
	assert.Equal(t, "json_schema", responseFormat["type"])
}

func TestChatCompletion_OSeriesMarshalErrorReturnsInvalidRequest(t *testing.T) {
	provider := NewWithHTTPClient(testAPIKey, http.DefaultClient, llmclient.Hooks{})

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "o3-mini",
		Messages: []core.Message{{Role: "user", Content: "hello"}},
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"x_invalid": json.RawMessage(`{`),
		}),
	})
	var gwErr *core.GatewayError
	require.ErrorAs(t, err, &gwErr)
	assert.Equal(t, core.ErrorTypeInvalidRequest, gwErr.Type)
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
			server, capture := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.responseBody))
			})
			provider := NewWithHTTPClient(testAPIKey, nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
				Model:    "gpt-4o",
				Messages: []core.Message{{Role: "user", Content: "Hello"}},
			})

			req := capture.Last(t)
			assertAuthorized(t, req)
			assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
			stream, _ := req.JSON(t)["stream"].(bool)
			assert.True(t, stream)

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
					{"id": "gpt-4o", "object": "model", "created": 1687882411, "owned_by": "openai"},
					{"id": "gpt-4", "object": "model", "created": 1687882410, "owned_by": "openai"}
				]
			}`,
			checkResponse: func(t *testing.T, resp *core.ModelsResponse) {
				assert.Equal(t, "list", resp.Object)
				require.Len(t, resp.Data, 2)
				assert.Equal(t, "gpt-4o", resp.Data[0].ID)
				assert.Equal(t, "openai", resp.Data[0].OwnedBy)
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
			server, capture := providertest.JSONServer(t, tt.statusCode, tt.responseBody)
			provider := NewWithHTTPClient(testAPIKey, nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			resp, err := provider.ListModels(context.Background())

			req := capture.Last(t)
			assertAuthorized(t, req)
			assert.Equal(t, http.MethodGet, req.Method)
			assert.Equal(t, "/models", req.Path)

			if tt.expectedError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			tt.checkResponse(t, resp)
		})
	}
}

func TestCancelledContextReturnsError(t *testing.T) {
	server, _ := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		w.WriteHeader(http.StatusRequestTimeout)
	})
	provider := NewWithHTTPClient(testAPIKey, nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := provider.ChatCompletion(ctx, &core.ChatRequest{
		Model:    "gpt-4o",
		Messages: []core.Message{{Role: "user", Content: "Hello"}},
	})
	assert.Error(t, err, "ChatCompletion")

	_, err = provider.Responses(ctx, &core.ResponsesRequest{Model: "gpt-4o", Input: "Hello"})
	assert.Error(t, err, "Responses")
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
			checkResponse: func(t *testing.T, resp *core.ResponsesResponse) {
				assert.Equal(t, "resp_123", resp.ID)
				assert.Equal(t, "response", resp.Object)
				assert.Equal(t, "gpt-4o", resp.Model)
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
				require.Len(t, resp.Output, 1)
				require.Len(t, resp.Output[0].Content, 1)
				annotations := resp.Output[0].Content[0].Annotations
				require.Len(t, annotations, 1)

				var annotation map[string]any
				require.NoError(t, json.Unmarshal(annotations[0], &annotation))
				assert.Equal(t, "url_citation", annotation["type"])
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
			server, capture := providertest.JSONServer(t, tt.statusCode, tt.responseBody)
			provider := NewWithHTTPClient(testAPIKey, nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{Model: "gpt-4o", Input: "Hello"})

			req := capture.Last(t)
			assertAuthorized(t, req)
			assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
			assert.Equal(t, "/responses", req.Path)
			assert.Equal(t, "gpt-4o", req.JSON(t)["model"])

			if tt.expectedError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			tt.checkResponse(t, resp)
		})
	}
}

func TestResponsesUtilitiesForwardResponseContext(t *testing.T) {
	server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/responses/input_tokens": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"response.input_tokens","input_tokens":10}`))
		},
		"/responses/compact": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"cmp_1","object":"response.compaction","output":[]}`))
		},
	})
	provider := NewWithHTTPClient(testAPIKey, server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	maxOutputTokens := 128
	parallelToolCalls := true
	temperature := 0.2
	topP := 0.8
	topLogprobs := 3
	store := false
	req := &core.ResponsesRequest{
		Model:                "gpt-4o",
		Provider:             "openai_primary",
		Input:                "hello",
		Instructions:         "be brief",
		Tools:                []map[string]any{{"type": "function", "name": "lookup"}},
		ToolChoice:           "auto",
		ParallelToolCalls:    &parallelToolCalls,
		Temperature:          &temperature,
		TopP:                 &topP,
		TopLogprobs:          &topLogprobs,
		MaxOutputTokens:      &maxOutputTokens,
		Stream:               true,
		StreamOptions:        &core.StreamOptions{IncludeUsage: true},
		Metadata:             map[string]string{"team": "alpha"},
		Reasoning:            &core.Reasoning{Effort: "low"},
		Text:                 map[string]any{"format": map[string]any{"type": "text"}},
		Include:              []string{"reasoning.encrypted_content"},
		Truncation:           "auto",
		Store:                &store,
		PreviousResponseID:   "resp_previous",
		Conversation:         &core.ResponsesConversationRef{ID: "conv_123"},
		Prompt:               map[string]any{"id": "pmpt_123"},
		PromptCacheRetention: "24h",
		ContextManagement:    map[string]any{"truncation": "auto"},
		User:                 "tenant-123",
		ServiceTier:          "flex",
		SafetyIdentifier:     "safe_123",
		ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
			"custom": json.RawMessage(`"value"`),
		}),
	}
	_, err := provider.CountResponseInputTokens(context.Background(), req)
	require.NoError(t, err)
	_, err = provider.CompactResponse(context.Background(), req)
	require.NoError(t, err)

	requests := capture.All()
	require.Len(t, requests, 2)
	assert.Equal(t, "/responses/input_tokens", requests[0].Path)
	assert.Equal(t, "/responses/compact", requests[1].Path)

	kept := []string{
		"tools", "tool_choice", "parallel_tool_calls", "temperature", "top_p", "top_logprobs",
		"max_output_tokens", "metadata", "reasoning", "text", "include", "truncation", "store",
		"previous_response_id", "conversation", "prompt", "prompt_cache_retention",
		"context_management", "user", "service_tier", "safety_identifier", "custom",
	}
	filtered := []string{"provider", "stream", "stream_options"}
	for _, sent := range requests {
		body := sent.JSON(t)
		assert.Equal(t, "gpt-4o", body["model"], sent.Path)
		assert.Equal(t, "hello", body["input"], sent.Path)
		assert.Equal(t, "be brief", body["instructions"], sent.Path)
		for _, field := range kept {
			assert.Contains(t, body, field, "%s body missing %q", sent.Path, field)
		}
		for _, field := range filtered {
			assert.NotContains(t, body, field, "%s body includes filtered field %q", sent.Path, field)
		}
	}
}

func TestResponsesWithArrayInput(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, responsesReplyJSON)
	provider := NewWithHTTPClient(testAPIKey, nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "gpt-4o",
		Input: []any{
			map[string]any{"role": "user", "content": "Hello"},
			map[string]any{"role": "assistant", "content": "Hi there!"},
		},
		Instructions: "Be helpful",
	})
	require.NoError(t, err)
	assert.Equal(t, "resp_123", resp.ID)

	input, ok := capture.Last(t).JSON(t)["input"].([]any)
	require.True(t, ok, "input should be forwarded as an array")
	assert.Len(t, input, 2)
}

func TestResponses_PreservesUnknownNestedFields(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, responsesReplyJSON)
	provider := NewWithHTTPClient(testAPIKey, server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "gpt-4o",
		Input: []core.ResponsesInputElement{
			{
				Type:        "message",
				Role:        "user",
				Content:     "Hello",
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"x_trace": json.RawMessage(`{"id":"trace-1"}`)}),
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "resp_123", resp.ID)

	input, ok := capture.Last(t).JSON(t)["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 1)
	first, ok := input[0].(map[string]any)
	require.True(t, ok, "input[0] = %#v, want object", input[0])
	_, ok = first["x_trace"].(map[string]any)
	assert.True(t, ok, "input[0].x_trace = %#v, want object", first["x_trace"])
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
			checkStream: func(t *testing.T, body io.ReadCloser) {
				require.NotNil(t, body)
				defer func() { _ = body.Close() }()

				respBody, err := io.ReadAll(body)
				require.NoError(t, err)
				assert.Contains(t, string(respBody), "response.created")
				assert.Contains(t, string(respBody), "response.output_text.delta")
				assert.Contains(t, string(respBody), "[DONE]")
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
			server, capture := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.responseBody))
			})
			provider := NewWithHTTPClient(testAPIKey, nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			body, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{Model: "gpt-4o", Input: "Hello"})

			req := capture.Last(t)
			assertAuthorized(t, req)
			assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
			assert.Equal(t, "/responses", req.Path)
			stream, _ := req.JSON(t)["stream"].(bool)
			assert.True(t, stream)

			if tt.expectedError {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			tt.checkStream(t, body)
		})
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
			assert.Equal(t, tt.expected, isOSeriesModel(tt.model))
		})
	}
}

func TestIsReasoningChatModel(t *testing.T) {
	tests := []struct {
		model    string
		expected bool
	}{
		{"o3-mini", true},
		{"o4-mini", true},
		{"gpt-5", true},
		{"gpt-5-mini", true},
		{"gpt-5-nano", true},
		{"gpt-5-chat-latest", true},
		{"gpt-5.1", true},
		{"gpt-5.6-terra", true},
		{"gpt-5.6", true},
		{"GPT-5.6-Terra", true},
		{"gpt-4o", false},
		{"gpt-4.1", false},
		{"gpt-50", false},
		{"claude-sonnet-4-6", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			assert.Equal(t, tt.expected, isReasoningChatModel(tt.model))
		})
	}
}

// Reasoning models take max_completion_tokens and reject temperature; other
// models receive max_tokens and temperature unchanged. Both the blocking and the
// streaming chat paths apply the same mapping.
func TestChatCompletion_AdaptsTokenParamsByModel(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		stream    bool
		reasoning bool
	}{
		{name: "o-series", model: "o3-mini", reasoning: true},
		{name: "gpt-5 family", model: "gpt-5-mini", reasoning: true},
		{name: "non-reasoning passes max_tokens", model: "gpt-4o"},
		{name: "o-series stream", model: "o4-mini", stream: true, reasoning: true},
		{name: "gpt-5 stream", model: "gpt-5-nano", stream: true, reasoning: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			maxTokens := 1000
			temperature := 0.7
			req := &core.ChatRequest{
				Model:       tt.model,
				Messages:    []core.Message{{Role: "user", Content: "Hello"}},
				MaxTokens:   &maxTokens,
				Temperature: &temperature,
			}

			var sent map[string]any
			if tt.stream {
				srv, capture := providertest.SSEServer(t, providertest.ChatChunkSSE)
				provider := NewWithHTTPClient(testAPIKey, nil, llmclient.Hooks{})
				provider.SetBaseURL(srv.URL)

				body, err := provider.StreamChatCompletion(context.Background(), req)
				require.NoError(t, err)
				defer func() { _ = body.Close() }()
				respBody, err := io.ReadAll(body)
				require.NoError(t, err)
				assert.Equal(t, providertest.ChatChunkSSE, string(respBody))
				sent = capture.Last(t).JSON(t)
				stream, _ := sent["stream"].(bool)
				assert.True(t, stream)
			} else {
				srv, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
				provider := NewWithHTTPClient(testAPIKey, nil, llmclient.Hooks{})
				provider.SetBaseURL(srv.URL)

				resp, err := provider.ChatCompletion(context.Background(), req)
				require.NoError(t, err)
				assert.Equal(t, providertest.Model, resp.Model)
				sent = capture.Last(t).JSON(t)
			}

			if tt.reasoning {
				assert.NotContains(t, sent, "max_tokens")
				assert.NotContains(t, sent, "temperature")
				assert.Equal(t, float64(maxTokens), sent["max_completion_tokens"])
			} else {
				assert.NotContains(t, sent, "max_completion_tokens")
				assert.Equal(t, float64(maxTokens), sent["max_tokens"])
				assert.Equal(t, temperature, sent["temperature"])
			}
		})
	}
}

// Tool definitions, tool_choice and parallel_tool_calls survive the
// reasoning-model adaptation as well as the plain passthrough.
func TestChatCompletion_PreservesToolConfiguration(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		reasoning bool
	}{
		{name: "non-reasoning model", model: "gpt-4o-mini"},
		{name: "reasoning model", model: "o3-mini", reasoning: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, `{
				"id": "chatcmpl-tools",
				"object": "chat.completion",
				"model": "`+tt.model+`",
				"choices": [{"index": 0, "message": {"role": "assistant", "content": "", "tool_calls": [{"id": "call_123", "type": "function", "function": {"name": "lookup_weather", "arguments": "{\"city\":\"Warsaw\"}"}}]}, "finish_reason": "tool_calls"}],
				"usage": {"prompt_tokens": 5, "completion_tokens": 10, "total_tokens": 15}
			}`)
			provider := NewWithHTTPClient(testAPIKey, nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			maxTokens := 256
			parallelToolCalls := false
			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:     tt.model,
				Messages:  []core.Message{{Role: "user", Content: "What's the weather?"}},
				MaxTokens: &maxTokens,
				Tools: []map[string]any{
					{
						"type": "function",
						"function": map[string]any{
							"name":        "lookup_weather",
							"description": "Get the weather for a city.",
							"parameters": map[string]any{
								"type":       "object",
								"properties": map[string]any{"city": map[string]any{"type": "string"}},
								"required":   []string{"city"},
							},
						},
					},
				},
				ToolChoice:        map[string]any{"type": "function", "function": map[string]any{"name": "lookup_weather"}},
				ParallelToolCalls: &parallelToolCalls,
			})
			require.NoError(t, err)
			require.Len(t, resp.Choices, 1)
			assert.Equal(t, "tool_calls", resp.Choices[0].FinishReason)
			require.Len(t, resp.Choices[0].Message.ToolCalls, 1)
			assert.Equal(t, "lookup_weather", resp.Choices[0].Message.ToolCalls[0].Function.Name)

			sent := capture.Last(t).JSON(t)
			tools, ok := sent["tools"].([]any)
			require.True(t, ok)
			assert.Len(t, tools, 1)
			toolChoice, ok := sent["tool_choice"].(map[string]any)
			require.True(t, ok, "tool_choice = %#v, want object", sent["tool_choice"])
			function, ok := toolChoice["function"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, "lookup_weather", function["name"])
			parallel, ok := sent["parallel_tool_calls"].(bool)
			require.True(t, ok, "parallel_tool_calls = %#v, want bool", sent["parallel_tool_calls"])
			assert.False(t, parallel)
			if tt.reasoning {
				assert.NotContains(t, sent, "max_tokens")
				assert.Equal(t, float64(maxTokens), sent["max_completion_tokens"])
			} else {
				assert.NotContains(t, sent, "max_completion_tokens")
				assert.Equal(t, float64(maxTokens), sent["max_tokens"])
			}
		})
	}
}

func TestPassthrough(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusTooManyRequests, `{"error":"rate limited"}`)
	provider := NewWithHTTPClient(testAPIKey, server.Client(), llmclient.Hooks{})
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
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	req := capture.Last(t)
	assert.Equal(t, http.MethodPost, req.Method)
	assert.Equal(t, "/responses", req.Path)
	assert.Equal(t, "bar", req.Query.Get("foo"))
	assertAuthorized(t, req)
	assert.Equal(t, "responses=v1", req.Header.Get("OpenAI-Beta"))
	assert.Equal(t, `{"model":"gpt-5-mini"}`, string(req.Body))

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, `{"error":"rate limited"}`, string(body))
}

func TestChatCompletion_MapsReasoningToReasoningEffort(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		reasoning  *core.Reasoning
		wantEffort string // "" means the field must be absent
	}{
		{name: "gpt-5 family", model: "gpt-5-mini", reasoning: &core.Reasoning{Effort: "low"}, wantEffort: "low"},
		{name: "o-series", model: "o3-mini", reasoning: &core.Reasoning{Effort: "high"}, wantEffort: "high"},
		{name: "custom endpoint model", model: "qwen3-32b", reasoning: &core.Reasoning{Effort: "medium"}, wantEffort: "medium"},
		{name: "non-reasoning gpt-4 drops it", model: "gpt-4.1-mini", reasoning: &core.Reasoning{Effort: "low"}},
		{name: "non-reasoning chatgpt drops it", model: "chatgpt-4o-latest", reasoning: &core.Reasoning{Effort: "low"}},
		{name: "empty effort drops it", model: "gpt-5-mini", reasoning: &core.Reasoning{}},
		{name: "no reasoning", model: "gpt-5-mini"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
			provider := NewWithHTTPClient(testAPIKey, nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:     tt.model,
				Messages:  []core.Message{{Role: "user", Content: "hi"}},
				Reasoning: tt.reasoning,
			})
			require.NoError(t, err)

			sent := capture.Last(t).JSON(t)
			assert.NotContains(t, sent, "reasoning", "nested reasoning must not reach Chat Completions")
			if tt.wantEffort == "" {
				assert.NotContains(t, sent, "reasoning_effort")
			} else {
				assert.Equal(t, tt.wantEffort, sent["reasoning_effort"])
			}
		})
	}
}

func TestNew_AttributesErrorsToInstanceName(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusBadRequest, `{"error":{"message":"bad request","type":"invalid_request_error"}}`)

	provider := New(providers.ProviderConfig{Name: "openai-eu", Type: "openai", APIKey: "k", BaseURL: server.URL},
		providers.ProviderOptions{Name: "openai-eu"})
	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "gpt-4.1-mini",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	var gwErr *core.GatewayError
	require.ErrorAs(t, err, &gwErr)
	assert.Equal(t, "openai-eu", gwErr.Provider)
}
