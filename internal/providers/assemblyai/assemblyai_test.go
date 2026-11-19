package assemblyai

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

func TestRegistration_DefaultsToTheUSEndpoint(t *testing.T) {
	assert.Equal(t, "assemblyai", Registration.Type)
	assert.Equal(t, "https://api.assemblyai.com", Registration.Discovery.DefaultBaseURL)
}

func TestListModels_ReturnsTheFixedSpeechModels(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{}`)
	provider := newTestProvider("key", server.URL, server.Client())

	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)

	ids := make([]string, 0, len(resp.Data))
	for _, model := range resp.Data {
		ids = append(ids, model.ID)
		assert.Equal(t, "assemblyai", model.OwnedBy)
		require.NotNil(t, model.Metadata)
		assert.Equal(t, []string{"audio_transcription"}, model.Metadata.Modes)
		assert.Equal(t, core.CategoriesForModes([]string{"audio_transcription"}), model.Metadata.Categories)
	}
	assert.Equal(t, []string{"assemblyai-stt-3-7", "universal-2"}, ids)
	assert.Zero(t, capture.Count(), "AssemblyAI has no model listing endpoint to call")

	resp.Data[0].ID = "changed"
	again, err := provider.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "assemblyai-stt-3-7", again.Data[0].ID, "callers get their own slice")
}

func TestSetBaseURL_ChangesRequestTarget(t *testing.T) {
	url, client, capture := (&fakeAssemblyAI{}).start(t)

	provider := newTestProvider("key", "https://unused.example", client)
	provider.SetBaseURL(url)
	_, err := provider.CreateTranscription(context.Background(), transcriptionRequest())
	require.NoError(t, err)

	assert.Equal(t, "/v2/upload", capture.All()[0].Path)
}

func TestUnsupportedCapabilities_ReturnInvalidRequestErrors(t *testing.T) {
	provider := New(providers.ProviderConfig{APIKey: "key"}, providers.ProviderOptions{})

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

			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
			assert.Contains(t, gatewayErr.Message, tt.want)
		})
	}
}

func TestPassthrough_ForwardsOpaqueRequest(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"transcripts":[]}`)

	provider := newTestProvider("aai_test", server.URL, server.Client())
	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodGet,
		Endpoint: "v2/transcript",
		Headers:  http.Header{},
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	req := capture.Last(t)
	assert.Equal(t, "/v2/transcript", req.Path)
	assert.Equal(t, "aai_test", req.Header.Get("Authorization"))
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	_, err = provider.Passthrough(context.Background(), nil)
	require.Error(t, err)
}
