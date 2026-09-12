package elevenlabs

import (
	"context"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetBaseURL_ChangesRequestTarget(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `[]`)

	provider := NewWithHTTPClient("elk_test", "https://unused.example", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)
	_, err := provider.ListModels(context.Background())
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, http.MethodGet, req.Method)
	assert.Equal(t, "/v1/models", req.Path)
	assert.Equal(t, "elk_test", req.Header.Get("xi-api-key"))
}

func TestListModels_FiltersToTextToSpeechAndAddsScribe(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `[
		{"model_id":"eleven_multilingual_v2","name":"Eleven Multilingual v2","can_do_text_to_speech":true,"languages":[{"language_id":"en"},{"language_id":"es"}]},
		{"model_id":"eleven_english_sts_v2","name":"Eleven English STS v2","can_do_text_to_speech":false}
	]`)

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)

	byID := make(map[string]core.Model, len(resp.Data))
	for _, model := range resp.Data {
		byID[model.ID] = model
	}
	assert.NotContains(t, byID, "eleven_english_sts_v2")
	assert.Contains(t, byID, "scribe_v1")

	tts, ok := byID["eleven_multilingual_v2"]
	require.True(t, ok)
	require.NotNil(t, tts.Metadata)
	assert.Equal(t, []string{"audio_speech"}, tts.Metadata.Modes)
	assert.True(t, tts.Metadata.Capabilities["multilingual"], "tts model capabilities = %+v, want multilingual", tts.Metadata.Capabilities)

	scribe, ok := byID["scribe_v2"]
	require.True(t, ok)
	require.NotNil(t, scribe.Metadata)
	assert.Equal(t, []string{"audio_transcription"}, scribe.Metadata.Modes)
}

func TestListModels_FallsBackToStaticModelsOnFirstFetchFailure(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusServiceUnavailable, `unavailable`)

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	require.Len(t, resp.Data, len(staticTranscriptionModels), "want only the static transcription models")

	ids := make([]string, 0, len(resp.Data))
	for _, m := range resp.Data {
		ids = append(ids, m.ID)
	}
	assert.Contains(t, ids, "scribe_v2")
}

func TestListModels_PropagatesErrorOnceCatalogHasSucceededOnce(t *testing.T) {
	fail := false
	server, _ := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"model_id":"eleven_multilingual_v2","name":"Eleven Multilingual v2","can_do_text_to_speech":true}]`))
	})

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	_, err := provider.ListModels(context.Background())
	require.NoError(t, err)

	fail = true
	_, err = provider.ListModels(context.Background())
	require.Error(t, err)
}

func TestUnsupportedCapabilities_ReturnInvalidRequestErrors(t *testing.T) {
	provider := NewWithHTTPClient("key", "", nil, llmclient.Hooks{})

	tests := []struct {
		name string
		call func() error
		want string
	}{
		{"chat", func() error { _, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{}); return err }, "does not support chat"},
		{"chat stream", func() error {
			_, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{})
			return err
		}, "does not support chat"},
		{"responses", func() error { _, err := provider.Responses(context.Background(), &core.ResponsesRequest{}); return err }, "does not support the responses"},
		{"responses stream", func() error {
			_, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{})
			return err
		}, "does not support the responses"},
		{"embeddings", func() error {
			_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{})
			return err
		}, "does not support embeddings"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestPassthrough_ForwardsOpaqueRequest(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusAccepted, `{"accepted":true}`)

	provider := NewWithHTTPClient("elk_test", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodGet,
		Endpoint: "voices",
		Headers:  http.Header{},
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	req := capture.Last(t)
	assert.Equal(t, "/voices", req.Path)
	assert.Equal(t, "elk_test", req.Header.Get("xi-api-key"))
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
}
