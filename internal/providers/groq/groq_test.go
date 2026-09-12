package groq

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

const testAPIKey = "test-api-key"

// newTestProvider points a provider at the given upstream URL.
func newTestProvider(baseURL string) *Provider {
	p := NewWithHTTPClient(testAPIKey, nil, llmclient.Hooks{})
	p.SetBaseURL(baseURL)
	return p
}

func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           "groq",
		DefaultBaseURL: "https://api.groq.com/openai/v1",
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			p := NewWithHTTPClient(apiKey, client, hooks)
			p.SetBaseURL(baseURL)
			return p
		},
		Embeddings: true,
	})
}

const chatCompletionJSON = `{
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
}`

const chatChunkSSE = `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama-3.3-70b-versatile","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama-3.3-70b-versatile","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":null}]}

data: [DONE]
`

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
				assert.Equal(t, "llama-3.3-70b-versatile", resp.Model)
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
			provider := newTestProvider(server.URL)

			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:    "llama-3.3-70b-versatile",
				Messages: []core.Message{{Role: "user", Content: "Hello"}},
			})

			sent := capture.Last(t)
			assert.Equal(t, "application/json", sent.Header.Get("Content-Type"))
			assert.Equal(t, "Bearer "+testAPIKey, sent.Header.Get("Authorization"))
			assert.Equal(t, "llama-3.3-70b-versatile", sent.JSON(t)["model"])

			if tt.expectedError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			tt.checkResponse(t, resp)
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
				_, _ = io.WriteString(w, tt.responseBody)
			})
			provider := newTestProvider(server.URL)

			body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
				Model:    "llama-3.3-70b-versatile",
				Messages: []core.Message{{Role: "user", Content: "Hello"}},
			})

			sent := capture.Last(t)
			assert.Equal(t, "application/json", sent.Header.Get("Content-Type"))
			assert.Equal(t, "Bearer "+testAPIKey, sent.Header.Get("Authorization"))
			stream, _ := sent.JSON(t)["stream"].(bool)
			assert.True(t, stream)

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
					{"id": "llama-3.3-70b-versatile", "object": "model", "created": 1687882411, "owned_by": "groq"},
					{"id": "mixtral-8x7b-32768", "object": "model", "created": 1687882410, "owned_by": "groq"}
				]
			}`,
			checkResponse: func(t *testing.T, resp *core.ModelsResponse) {
				assert.Equal(t, "list", resp.Object)
				require.Len(t, resp.Data, 2)
				assert.Equal(t, "llama-3.3-70b-versatile", resp.Data[0].ID)
				assert.Equal(t, "groq", resp.Data[0].OwnedBy)
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
			provider := newTestProvider(server.URL)

			resp, err := provider.ListModels(context.Background())

			sent := capture.Last(t)
			assert.Equal(t, http.MethodGet, sent.Method)
			assert.Equal(t, "/models", sent.Path)
			assert.Equal(t, "Bearer "+testAPIKey, sent.Header.Get("Authorization"))

			if tt.expectedError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			tt.checkResponse(t, resp)
		})
	}
}

// blockUntilCancelled answers only once the caller gives up, so a cancelled
// context must surface as an error rather than a response.
func blockUntilCancelled(w http.ResponseWriter, r *http.Request) {
	<-r.Context().Done()
	w.WriteHeader(http.StatusRequestTimeout)
}

func TestChatCompletionWithContext(t *testing.T) {
	server, _ := providertest.Server(t, blockUntilCancelled)
	provider := newTestProvider(server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := provider.ChatCompletion(ctx, &core.ChatRequest{
		Model:    "llama-3.3-70b-versatile",
		Messages: []core.Message{{Role: "user", Content: "Hello"}},
	})
	assert.Error(t, err)
}

func TestResponses(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, chatCompletionJSON)
	provider := newTestProvider(server.URL)

	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "llama-3.3-70b-versatile",
		Input: "Hello",
	})
	require.NoError(t, err)
	assert.Equal(t, "/chat/completions", capture.Last(t).Path)
	assert.Equal(t, "chatcmpl-123", resp.ID)
	assert.Equal(t, "response", resp.Object)
	assert.Equal(t, "llama-3.3-70b-versatile", resp.Model)
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
		Model: "llama-3.3-70b-versatile",
		Input: []any{
			map[string]any{"role": "user", "content": "Hello"},
			map[string]any{"role": "assistant", "content": "Hi there!"},
		},
		Instructions: "Be helpful",
	})
	require.NoError(t, err)
	assert.Equal(t, "chatcmpl-123", resp.ID)

	// Instructions become a system message ahead of the two input messages.
	messages, ok := capture.Last(t).JSON(t)["messages"].([]any)
	require.True(t, ok)
	assert.Len(t, messages, 3)
}

func TestResponses_PreservesOpaqueFieldsThroughChatAdapter(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, chatCompletionJSON)
	provider := newTestProvider(server.URL)

	_, err := provider.Responses(context.Background(), &core.ResponsesRequest{
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
	})
	require.NoError(t, err)

	sent := capture.Last(t)
	assert.Equal(t, "/chat/completions", sent.Path)

	var req core.ChatRequest
	require.NoError(t, json.Unmarshal(sent.Body, &req))
	assert.NotNil(t, req.ExtraFields.Lookup("response_format"))
	require.Len(t, req.Messages, 1)
	assert.NotNil(t, req.Messages[0].ExtraFields.Lookup("x_message_hint"))

	parts, ok := req.Messages[0].Content.([]core.ContentPart)
	require.True(t, ok, "Messages[0].Content type = %T, want []core.ContentPart", req.Messages[0].Content)
	assert.NotNil(t, parts[0].ExtraFields.Lookup("cache_control"))
}

func TestStreamResponses(t *testing.T) {
	server, capture := providertest.SSEServer(t, chatChunkSSE)
	provider := newTestProvider(server.URL)

	body, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "llama-3.3-70b-versatile",
		Input: "Hello",
	})
	require.NoError(t, err)
	require.NotNil(t, body)
	defer func() { _ = body.Close() }()

	stream, _ := capture.Last(t).JSON(t)["stream"].(bool)
	assert.True(t, stream)

	respBody, err := io.ReadAll(body)
	require.NoError(t, err)

	responseStr := string(respBody)
	assert.Contains(t, responseStr, "response.created")
	assert.Contains(t, responseStr, "response.output_text.delta")
	assert.Contains(t, responseStr, "[DONE]")
}

func TestResponsesWithContext(t *testing.T) {
	server, _ := providertest.Server(t, blockUntilCancelled)
	provider := newTestProvider(server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := provider.Responses(ctx, &core.ResponsesRequest{
		Model: "llama-3.3-70b-versatile",
		Input: "Hello",
	})
	assert.Error(t, err)
}

func TestGroqResponsesStreamConverter(t *testing.T) {
	mockStream := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama-3.3-70b-versatile","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama-3.3-70b-versatile","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}

data: [DONE]
`

	reader := io.NopCloser(strings.NewReader(mockStream))
	converter := providers.NewOpenAIResponsesStreamConverter(reader, "llama-3.3-70b-versatile", "groq")

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

func TestGroqResponsesStreamConverter_Close(t *testing.T) {
	reader := io.NopCloser(strings.NewReader("data: [DONE]\n"))
	converter := providers.NewOpenAIResponsesStreamConverter(reader, "test-model", "groq")

	require.NoError(t, converter.Close())

	// Subsequent reads should return EOF.
	buf := make([]byte, 100)
	n, err := converter.Read(buf)
	assert.Equal(t, 0, n)
	assert.Equal(t, io.EOF, err)
}

func TestGroqResponsesStreamConverter_EmptyDelta(t *testing.T) {
	mockStream := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama-3.3-70b-versatile","choices":[{"index":0,"delta":{},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama-3.3-70b-versatile","choices":[{"index":0,"delta":{"content":""},"finish_reason":null}]}

data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1677652288,"model":"llama-3.3-70b-versatile","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: [DONE]
`

	reader := io.NopCloser(strings.NewReader(mockStream))
	converter := providers.NewOpenAIResponsesStreamConverter(reader, "llama-3.3-70b-versatile", "groq")

	data, err := io.ReadAll(converter)
	require.NoError(t, err)

	// Empty deltas are not emitted: only the "Hello" delta event remains.
	result := string(data)
	assert.Equal(t, 1, strings.Count(result, "event: response.output_text.delta"))
	assert.Contains(t, result, `"delta":"Hello"`)
}

func TestCreateSpeech(t *testing.T) {
	audio := []byte("fake-mp3-bytes")
	server, capture := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(audio)
	})
	provider := newTestProvider(server.URL)

	resp, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model: "playai-tts",
		Input: "Hello from Groq.",
		Voice: "Fritz-PlayAI",
	})
	require.NoError(t, err)

	sent := capture.Last(t)
	assert.Equal(t, "/audio/speech", sent.Path)
	assert.Equal(t, "Bearer "+testAPIKey, sent.Header.Get("Authorization"))
	body := sent.JSON(t)
	assert.Equal(t, "playai-tts", body["model"])
	assert.Equal(t, "Fritz-PlayAI", body["voice"])

	assert.Equal(t, "audio/mpeg", resp.ContentType)
	assert.Equal(t, string(audio), string(resp.Data))
}

func TestCreateTranscription(t *testing.T) {
	var gotModel, gotFile string
	server, capture := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		gotModel = r.FormValue("model")
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer func() { _ = file.Close() }()
		data, _ := io.ReadAll(file)
		gotFile = string(data)

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"text":"hello from groq"}`)
	})
	provider := newTestProvider(server.URL)

	resp, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:    "whisper-large-v3",
		Filename: "speech.wav",
		File:     []byte("fake-wav-bytes"),
	})
	require.NoError(t, err)
	assert.Equal(t, "/audio/transcriptions", capture.Last(t).Path)
	assert.Equal(t, "whisper-large-v3", gotModel)
	assert.Equal(t, "fake-wav-bytes", gotFile)
	assert.Contains(t, string(resp.Data), "hello from groq")
}

func TestCreateTranscription_UpstreamError(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusBadRequest, `{"error":{"message":"invalid audio"}}`)
	provider := newTestProvider(server.URL)

	_, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:    "whisper-large-v3",
		Filename: "speech.wav",
		File:     []byte("bad"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid audio")
}
