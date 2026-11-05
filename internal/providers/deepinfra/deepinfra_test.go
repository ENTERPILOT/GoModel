package deepinfra

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// DeepInfra forwards chat, embeddings, and Responses upstream unchanged, so
// the shared contract covers that surface; the tests below cover the media,
// batch, and model-listing routes the contract does not.
func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           "deepinfra",
		DefaultBaseURL: "https://api.deepinfra.com/v1/openai",
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			return newTestProvider(apiKey, baseURL, client, hooks)
		},
		Embeddings:      true,
		NativeResponses: true,
	})
}

// newTestProvider builds the provider through its own constructor on a test transport.
func newTestProvider(apiKey, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	opts := providertest.Options(hooks)
	opts.HTTPClient = httpClient
	return New(providers.ProviderConfig{APIKey: apiKey, BaseURL: baseURL}, opts).(*Provider)
}

func TestResponses_TextSettingsRouteThroughChat(t *testing.T) {
	jsonSchema := map[string]any{"format": map[string]any{
		"type":   "json_schema",
		"name":   "capital",
		"schema": map[string]any{"type": "object", "properties": map[string]any{"capital": map[string]any{"type": "string"}}},
	}}
	tests := []struct {
		name               string
		text               any
		wantPath           string
		wantResponseFormat string
	}{
		{name: "no text stays native", wantPath: "/responses"},
		{name: "json_schema format uses chat", text: jsonSchema, wantPath: "/chat/completions", wantResponseFormat: "json_schema"},
		{name: "verbosity uses chat", text: map[string]any{"verbosity": "low"}, wantPath: "/chat/completions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
				"/responses": func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(providertest.ResponsesJSON))
				},
				"/chat/completions": func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(providertest.ChatCompletionJSON))
				},
			})
			provider := newTestProvider("di-key", server.URL, server.Client(), llmclient.Hooks{})

			resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
				Model: providertest.Model, Input: providertest.Prompt, Text: tt.text,
			})
			require.NoError(t, err)
			require.NotNil(t, resp)

			req := capture.Last(t)
			assert.Equal(t, tt.wantPath, req.Path)
			if tt.text != nil {
				assert.NotContains(t, req.JSON(t), "text", "chat requests carry no Responses text member")
			}
			if tt.wantResponseFormat != "" {
				responseFormat, ok := req.JSON(t)["response_format"].(map[string]any)
				require.True(t, ok, "text.format becomes response_format")
				assert.Equal(t, tt.wantResponseFormat, responseFormat["type"])
			}
		})
	}
}

func TestStreamResponses_TextSettingsRouteThroughChat(t *testing.T) {
	server, capture := providertest.SSEServer(t, providertest.ChatChunkSSE)
	provider := newTestProvider("di-key", server.URL, server.Client(), llmclient.Hooks{})

	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: providertest.Model, Input: providertest.Prompt,
		Text: map[string]any{"format": map[string]any{"type": "json_object"}},
	})
	require.NoError(t, err)
	_, _ = io.ReadAll(stream)
	require.NoError(t, stream.Close())

	req := capture.Last(t)
	assert.Equal(t, "/chat/completions", req.Path)
	assert.Equal(t, map[string]any{"type": "json_object"}, req.JSON(t)["response_format"])
}

func TestCreateImage_PostsToImageGenerations(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK,
		`{"created":1792810760,"data":[{"b64_json":"aGVsbG8=","revised_prompt":null,"url":null}]}`)
	provider := newTestProvider("di-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.CreateImage(context.Background(), &core.ImageGenerationRequest{
		Model:  "black-forest-labs/FLUX-1-schnell",
		Prompt: "a red cube",
	})
	require.NoError(t, err)
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "aGVsbG8=", resp.Data[0].B64JSON)

	req := capture.Last(t)
	assert.Equal(t, "/images/generations", req.Path)
	assert.Equal(t, "Bearer di-key", req.Header.Get("Authorization"))
	assert.Equal(t, "black-forest-labs/FLUX-1-schnell", req.JSON(t)["model"])
}

func TestAudio_RoutesToOpenAIAudioEndpoints(t *testing.T) {
	server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/audio/speech": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte("mp3-bytes"))
		},
		"/audio/transcriptions": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text":"hello"}`))
		},
		"/audio/translations": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text":"hello"}`))
		},
	})
	provider := newTestProvider("di-key", server.URL, server.Client(), llmclient.Hooks{})
	ctx := context.Background()

	speechReq := &core.AudioSpeechRequest{Model: "hexgrad/Kokoro-82M", Input: "hi", Voice: "af_bella"}
	speech, err := provider.CreateSpeech(ctx, speechReq)
	require.NoError(t, err)
	assert.Equal(t, []byte("mp3-bytes"), providertest.AudioBytes(t, speech))
	assert.Equal(t, "/audio/speech", capture.Last(t).Path)
	assert.Equal(t, "mp3", capture.Last(t).JSON(t)["response_format"], "OpenAI's default format is sent explicitly")
	assert.Empty(t, speechReq.ResponseFormat, "the caller's request must not be mutated")

	_, err = provider.CreateSpeech(ctx, &core.AudioSpeechRequest{Model: "hexgrad/Kokoro-82M", Input: "hi", Voice: "af_bella", ResponseFormat: "wav"})
	require.NoError(t, err)
	assert.Equal(t, "wav", capture.Last(t).JSON(t)["response_format"])

	transcription := &core.AudioTranscriptionRequest{Model: "openai/whisper-large-v3-turbo", Filename: "a.mp3", File: []byte("audio")}
	_, err = provider.CreateTranscription(ctx, transcription)
	require.NoError(t, err)
	assert.Equal(t, "/audio/transcriptions", capture.Last(t).Path)

	_, err = provider.CreateTranslation(ctx, transcription)
	require.NoError(t, err)
	assert.Equal(t, "/audio/translations", capture.Last(t).Path)
	assert.Equal(t, "Bearer di-key", capture.Last(t).Header.Get("Authorization"))
}

func TestGetBatch_UsesBatchesEndpoint(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK,
		`{"id":"batch_123","object":"batch","endpoint":"/v1/chat/completions","status":"in_progress","input_file_id":"file_1","completion_window":"24h","created_at":1792810941}`)
	provider := newTestProvider("di-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.GetBatch(context.Background(), "batch_123")
	require.NoError(t, err)
	assert.Equal(t, "in_progress", resp.Status)

	req := capture.Last(t)
	assert.Equal(t, http.MethodGet, req.Method)
	assert.Equal(t, "/batches/batch_123", req.Path)
}
