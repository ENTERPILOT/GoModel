package speechmatics

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

func TestRegistration_DefaultsToTheEU1BatchEndpoint(t *testing.T) {
	assert.Equal(t, "speechmatics", Registration.Type)
	assert.Equal(t, "https://eu1.asr.api.speechmatics.com/v2", Registration.Discovery.DefaultBaseURL)
}

func TestListModels_ReturnsTheBatchModelsAsTranscriptionModels(t *testing.T) {
	api := newFakeBatchAPI(t, &fakeBatchAPI{})

	resp, err := newTestProvider(api).ListModels(context.Background())
	require.NoError(t, err)

	ids := make([]string, 0, len(resp.Data))
	for _, model := range resp.Data {
		ids = append(ids, model.ID)
		assert.Equal(t, "speechmatics", model.OwnedBy)
		require.NotNil(t, model.Metadata)
		assert.Equal(t, []string{"audio_transcription"}, model.Metadata.Modes, model.ID)
		assert.NotEmpty(t, model.Metadata.Categories, model.ID)
	}
	assert.Equal(t, []string{"enhanced", "standard", "melia-1", "oak-1"}, ids)
	assert.Zero(t, api.capture.Count(), "Speechmatics has no model listing to call")
}

func TestSetBaseURL_ChangesRequestTarget(t *testing.T) {
	api := newFakeBatchAPI(t, &fakeBatchAPI{transcripts: map[string]string{"json-v2": helloTranscript}})
	provider := newTestProvider(api)
	provider.SetBaseURL("http://127.0.0.1:1")

	_, err := provider.CreateTranscription(context.Background(), transcriptionRequest())
	require.Error(t, err)
	assert.Equal(t, 0, api.capture.Count())

	provider.SetBaseURL(api.url)
	_, err = provider.CreateTranscription(context.Background(), transcriptionRequest())
	require.NoError(t, err)
	assert.Equal(t, 3, api.capture.Count())
}

func TestUnsupportedCapabilities_ReturnInvalidRequestErrors(t *testing.T) {
	provider := New(providers.ProviderConfig{APIKey: "key"}, providers.ProviderOptions{}).(*Provider)

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
		{"speech", func() error {
			_, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{})
			return err
		}, "does not support text-to-speech"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, tt.call(), &gatewayErr)
			assert.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
			assert.Contains(t, gatewayErr.Message, tt.want)
		})
	}
}

func TestPassthrough_ForwardsOpaqueRequest(t *testing.T) {
	api := newFakeBatchAPI(t, &fakeBatchAPI{})
	var started llmclient.RequestInfo
	opts := providertest.Options(llmclient.Hooks{OnRequestStart: func(ctx context.Context, info llmclient.RequestInfo) context.Context {
		started = info
		return ctx
	}})
	opts.HTTPClient = api.client
	provider := New(providers.ProviderConfig{APIKey: "sm_test", BaseURL: api.url}, opts).(*Provider)

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:    http.MethodGet,
		Endpoint:  "jobs/job1",
		Headers:   http.Header{},
		Model:     "enhanced",
		Operation: string(core.OperationAudioTranscriptions),
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, "enhanced", started.Model)
	assert.Equal(t, string(core.OperationAudioTranscriptions), started.Operation)
	req := api.capture.Last(t)
	assert.Equal(t, "/jobs/job1", req.Path)
	assert.Equal(t, "Bearer sm_test", req.Header.Get("Authorization"))
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
