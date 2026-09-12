package ollama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const chatCompletionJSON = `{
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
}`

const chatChunkSSE = `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama3.2","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama3.2","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":null}]}

data: [DONE]
`

// newTestProvider builds a keyless provider pointed at baseURL.
func newTestProvider(baseURL string) *Provider {
	provider := NewWithHTTPClient("", nil, llmclient.Hooks{})
	provider.SetBaseURL(baseURL)
	return provider
}

func TestNew(t *testing.T) {
	tests := []struct {
		name   string
		apiKey string
	}{
		{name: "with api key", apiKey: "test-api-key"},
		// Ollama doesn't require an API key
		{name: "without api key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := NewWithHTTPClient(tt.apiKey, nil, llmclient.Hooks{})
			assert.Equal(t, tt.apiKey, provider.keys.Primary())
			assert.NotNil(t, provider.compat)
			assert.NotNil(t, provider.nativeClient)
		})
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
			name:         "successful request",
			statusCode:   http.StatusOK,
			responseBody: chatCompletionJSON,
			checkResponse: func(t *testing.T, resp *core.ChatResponse) {
				assert.Equal(t, "chatcmpl-123", resp.ID)
				assert.Equal(t, "llama3.2", resp.Model)
				require.Len(t, resp.Choices, 1)
				assert.Equal(t, "Hello! How can I help you today?", resp.Choices[0].Message.Content)
				assert.Equal(t, 10, resp.Usage.PromptTokens)
				assert.Equal(t, 20, resp.Usage.CompletionTokens)
				assert.Equal(t, 30, resp.Usage.TotalTokens)
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
			server, capture := providertest.JSONServer(t, tt.statusCode, tt.responseBody)
			provider := newTestProvider(server.URL)

			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:    "llama3.2",
				Messages: []core.Message{{Role: "user", Content: "Hello"}},
			})

			req := capture.Last(t)
			assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
			assert.Equal(t, "llama3.2", req.JSON(t)["model"])

			if tt.expectedError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			tt.checkResponse(t, resp)
		})
	}
}

func TestChatCompletion_AuthorizationHeader(t *testing.T) {
	tests := []struct {
		name     string
		apiKey   string
		wantAuth string
	}{
		{name: "with api key", apiKey: "test-api-key", wantAuth: "Bearer test-api-key"},
		{name: "without api key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, chatCompletionJSON)
			provider := NewWithHTTPClient(tt.apiKey, nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:    "llama3.2",
				Messages: []core.Message{{Role: "user", Content: "Hello"}},
			})
			require.NoError(t, err)
			assert.Equal(t, tt.wantAuth, capture.Last(t).Header.Get("Authorization"))
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
			name:         "successful streaming request",
			statusCode:   http.StatusOK,
			responseBody: chatChunkSSE,
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
			server, capture := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.responseBody))
			})
			provider := newTestProvider(server.URL)

			body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
				Model:    "llama3.2",
				Messages: []core.Message{{Role: "user", Content: "Hello"}},
			})

			req := capture.Last(t)
			assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
			assert.Equal(t, true, req.JSON(t)["stream"])

			if tt.expectedError {
				require.Error(t, err)
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
			checkResponse: func(t *testing.T, resp *core.ModelsResponse) {
				assert.Equal(t, "list", resp.Object)
				require.Len(t, resp.Data, 2)
				assert.Equal(t, "llama3.2", resp.Data[0].ID)
				assert.Equal(t, "library", resp.Data[0].OwnedBy)
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
			server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
				"/models": func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tt.statusCode)
					_, _ = w.Write([]byte(tt.responseBody))
				},
				// Best-effort capability probe issued per listed model.
				"/api/show": func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write([]byte(`{"capabilities":["completion"]}`))
				},
			})
			provider := newTestProvider(server.URL)

			resp, err := provider.ListModels(context.Background())

			listing := capture.All()[0]
			assert.Equal(t, http.MethodGet, listing.Method)
			assert.Equal(t, "/models", listing.Path)

			if tt.expectedError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			tt.checkResponse(t, resp)
		})
	}
}

// ListModels must stamp modes from native /api/show capabilities (embedding
// models get classified without a remote-registry entry), cache the results so
// repeat listings don't re-probe, and leave models unstamped when the probe
// fails so the ID heuristic can still apply.
func TestListModels_StampsShowCapabilities(t *testing.T) {
	server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/models": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"object":"list","data":[
				{"id":"llama3.2","object":"model","owned_by":"library"},
				{"id":"nomic-embed-text","object":"model","owned_by":"library"},
				{"id":"mystery-model","object":"model","owned_by":"library"}
			]}`))
		},
		"/api/show": func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			switch req.Model {
			case "nomic-embed-text":
				_, _ = w.Write([]byte(`{"capabilities":["embedding"]}`))
			case "llama3.2":
				_, _ = w.Write([]byte(`{"capabilities":["completion","tools"]}`))
			default:
				w.WriteHeader(http.StatusInternalServerError)
			}
		},
	})
	provider := newTestProvider(server.URL)

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)

	byID := map[string]core.Model{}
	for _, m := range resp.Data {
		byID[m.ID] = m
	}

	embed := byID["nomic-embed-text"]
	require.NotNil(t, embed.Metadata)
	assert.Equal(t, []string{"embedding"}, embed.Metadata.Modes)
	assert.Equal(t, []core.ModelCategory{core.CategoryEmbedding}, embed.Metadata.Categories)

	chat := byID["llama3.2"]
	require.NotNil(t, chat.Metadata)
	assert.Equal(t, []string{"chat"}, chat.Metadata.Modes)
	assert.Nil(t, byID["mystery-model"].Metadata)

	// Second listing: successes served from cache, the failure re-probed.
	_, err = provider.ListModels(context.Background())
	require.NoError(t, err)

	showCalls := map[string]int{}
	for _, req := range capture.All() {
		if req.Path == "/api/show" {
			showCalls[req.JSON(t)["model"].(string)]++
		}
	}
	assert.Equal(t, map[string]int{"nomic-embed-text": 1, "llama3.2": 1, "mystery-model": 2}, showCalls, "successful probes must be cached")
}

func TestChatCompletionWithContext(t *testing.T) {
	server, _ := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		// Simulate a slow response
		<-r.Context().Done()
		w.WriteHeader(http.StatusRequestTimeout)
	})
	provider := newTestProvider(server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := provider.ChatCompletion(ctx, &core.ChatRequest{
		Model:    "llama3.2",
		Messages: []core.Message{{Role: "user", Content: "Hello"}},
	})
	require.Error(t, err)
}

func TestResponses(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, chatCompletionJSON)
	provider := newTestProvider(server.URL)

	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "llama3.2",
		Input: "Hello",
	})
	require.NoError(t, err)
	// Ollama converts Responses to chat completions.
	assert.Equal(t, "/chat/completions", capture.Last(t).Path)
	assert.Equal(t, "chatcmpl-123", resp.ID)
	assert.Equal(t, "response", resp.Object)
	assert.Equal(t, "llama3.2", resp.Model)
	assert.Equal(t, "completed", resp.Status)
	require.Len(t, resp.Output, 1)
	require.Len(t, resp.Output[0].Content, 1)
	assert.Equal(t, "Hello! How can I help you today?", resp.Output[0].Content[0].Text)
	require.NotNil(t, resp.Usage)
	assert.Equal(t, 10, resp.Usage.InputTokens)
	assert.Equal(t, 20, resp.Usage.OutputTokens)
}

func TestResponsesWithArrayInput(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, chatCompletionJSON)
	provider := newTestProvider(server.URL)

	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "llama3.2",
		Input: []any{
			map[string]any{"role": "user", "content": "Hello"},
			map[string]any{"role": "assistant", "content": "Hi there!"},
		},
		Instructions: "Be helpful",
	})
	require.NoError(t, err)
	assert.Equal(t, "chatcmpl-123", resp.ID)

	// Input is converted to messages: system message + 2 input messages.
	messages, ok := capture.Last(t).JSON(t)["messages"].([]any)
	require.True(t, ok)
	assert.Len(t, messages, 3)
}

func TestStreamResponses(t *testing.T) {
	server, capture := providertest.SSEServer(t, chatChunkSSE)
	provider := newTestProvider(server.URL)

	body, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "llama3.2",
		Input: "Hello",
	})
	require.NoError(t, err)
	require.NotNil(t, body)
	defer func() { _ = body.Close() }()

	respBody, err := io.ReadAll(body)
	require.NoError(t, err)
	assert.Equal(t, true, capture.Last(t).JSON(t)["stream"])

	responseStr := string(respBody)
	assert.Contains(t, responseStr, "response.created")
	assert.Contains(t, responseStr, "response.output_text.delta")
	assert.Contains(t, responseStr, "[DONE]")
}

func TestResponsesWithContext(t *testing.T) {
	server, _ := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		// Simulate a slow response
		<-r.Context().Done()
		w.WriteHeader(http.StatusRequestTimeout)
	})
	provider := newTestProvider(server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := provider.Responses(ctx, &core.ResponsesRequest{
		Model: "llama3.2",
		Input: "Hello",
	})
	require.Error(t, err)
}

func TestOllamaResponsesStreamConverter(t *testing.T) {
	// Test the stream converter with mock chat completion stream
	mockStream := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama3.2","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama3.2","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}

data: [DONE]
`

	reader := io.NopCloser(strings.NewReader(mockStream))
	converter := providers.NewOpenAIResponsesStreamConverter(reader, "llama3.2", "ollama")

	data, err := io.ReadAll(converter)
	require.NoError(t, err)

	result := string(data)
	assert.Contains(t, result, "response.created")
	assert.Contains(t, result, "response.output_text.delta")
	assert.Contains(t, result, "Hello")
	assert.Contains(t, result, " world")
	assert.Contains(t, result, "response.completed")
	assert.Contains(t, result, "[DONE]")
}

func TestSetBaseURL_TrailingSlash(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"model":"nomic-embed-text","embeddings":[[0.1,0.2]]}`)
	provider := newTestProvider(server.URL + "/v1/")

	_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "nomic-embed-text",
		Input: "hello",
	})
	require.NoError(t, err)
	assert.Equal(t, "/api/embed", capture.Last(t).Path)
}

func TestEmbeddings(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"model": "nomic-embed-text",
		"embeddings": [[0.1, 0.2, 0.3], [0.4, 0.5, 0.6]],
		"prompt_eval_count": 8
	}`)
	provider := newTestProvider(server.URL + "/v1")

	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "nomic-embed-text",
		Input: []string{"hello", "world"},
	})
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, "/api/embed", req.Path)
	assert.Equal(t, http.MethodPost, req.Method)
	var sent ollamaEmbedRequest
	require.NoError(t, json.Unmarshal(req.Body, &sent))
	assert.Equal(t, "nomic-embed-text", sent.Model)

	assert.Equal(t, "list", resp.Object)
	assert.Equal(t, "nomic-embed-text", resp.Model)
	require.Len(t, resp.Data, 2)
	assert.Equal(t, "embedding", resp.Data[0].Object)

	var floats []float64
	require.NoError(t, json.Unmarshal(resp.Data[0].Embedding, &floats))
	assert.Len(t, floats, 3)
	assert.Equal(t, 1, resp.Data[1].Index)
	assert.Equal(t, 8, resp.Usage.PromptTokens)
	assert.Equal(t, 8, resp.Usage.TotalTokens)
}

func TestEmbeddings_ModelFallback(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{
		"model": "",
		"embeddings": [[0.1]],
		"prompt_eval_count": 1
	}`)
	provider := newTestProvider(server.URL + "/v1")

	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "nomic-embed-text",
		Input: "hello",
	})
	require.NoError(t, err)
	assert.Equal(t, "nomic-embed-text", resp.Model)
}

// TestEmbeddings_NoVectorsErrors guards the common misconfiguration where an
// OpenAI-compatible server (e.g. LM Studio) is registered as an "ollama"
// provider. Such servers answer the native /api/embed path with a 200 and an
// error body, which unmarshals into zero embeddings. The adapter must surface
// an error instead of returning an empty, OpenAI-shaped list.
func TestEmbeddings_NoVectorsErrors(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"error":"Unexpected endpoint or method. (POST /api/embed)"}`)
	provider := newTestProvider(server.URL + "/v1")

	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "text-embedding-nomic-embed-text-v1.5",
		Input: "hello world",
	})
	require.Error(t, err)
	require.Nil(t, resp)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusBadGateway, gatewayErr.HTTPStatusCode())
	assert.Contains(t, gatewayErr.Message, `"openai" or "vllm" provider`)
}

// TestEmbeddings_EmptyInputNoError ensures an empty input batch (an empty
// array/slice, including a typed []string{}) is not mistaken for the
// LM-Studio-as-ollama misconfiguration: zero vectors for an empty batch is a
// legitimate result, not a provider error.
func TestEmbeddings_EmptyInputNoError(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"model":"nomic-embed-text","embeddings":[],"prompt_eval_count":0}`)
	provider := newTestProvider(server.URL + "/v1")

	for _, empty := range []any{[]any{}, []string{}} {
		resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
			Model: "nomic-embed-text",
			Input: empty,
		})
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Empty(t, resp.Data)
	}

	// Scalar/nil inputs are NOT empty batches: zero vectors must stay on the
	// loud-error path so a misconfigured OpenAI-compatible endpoint returning a
	// 200 error body for "" / null isn't silently swallowed as an empty list.
	for _, scalar := range []any{"", nil} {
		_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
			Model: "nomic-embed-text",
			Input: scalar,
		})
		assert.Error(t, err, "input %#v", scalar)
	}
}
