package groq

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
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
	return New(providers.ProviderConfig{APIKey: testAPIKey, BaseURL: baseURL}, providertest.Options(llmclient.Hooks{})).(*Provider)
}

func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           "groq",
		DefaultBaseURL: "https://api.groq.com/openai/v1",
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			opts := providertest.Options(hooks)
			opts.HTTPClient = client
			return New(providers.ProviderConfig{APIKey: apiKey, BaseURL: baseURL}, opts)
		},
		Embeddings: true,
	})
}

func TestResponses_PreservesOpaqueFieldsThroughChatAdapter(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
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
	assert.Equal(t, string(audio), string(providertest.AudioBytes(t, resp)))
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
