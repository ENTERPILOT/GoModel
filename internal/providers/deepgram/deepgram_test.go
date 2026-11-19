package deepgram

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

// modelsBody follows the live GET /v1/models response: one entry per model and
// language, with pre-recorded and streaming entries listed separately.
const modelsBody = `{
	"stt": [
		{"name":"general","canonical_name":"nova-3-general","architecture":"nova-3","languages":["en","en-US"],"batch":true,"streaming":false},
		{"name":"general","canonical_name":"nova-3-general","architecture":"nova-3","languages":["en","en-US"],"batch":false,"streaming":true},
		{"name":"general","canonical_name":"nova-3-general","architecture":"nova-3","languages":["de"],"batch":true,"streaming":false},
		{"name":"medical","canonical_name":"nova-3-medical","architecture":"nova-3","languages":["en"],"batch":true,"streaming":true},
		{"name":"atc","canonical_name":"nova-2-atc","architecture":"nova-2","languages":["en"],"batch":false,"streaming":true},
		{"name":"large","canonical_name":"whisper-large","architecture":"whisper","languages":["en"],"batch":true,"streaming":false}
	],
	"tts": [
		{"name":"thalia","canonical_name":"aura-2-thalia-en","architecture":"aura-2","languages":["en","en-US"]},
		{"name":"agathe","canonical_name":"aura-2-agathe-fr","architecture":"aura-2","languages":["fr","fr-FR"]},
		{"name":"asteria","canonical_name":"aura-asteria-en","architecture":"aura","languages":["en","en-US"]}
	],
	"languages": {}
}`

func TestListModels_ListsBatchTranscriptionAndVoiceModels(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, modelsBody)

	provider := newTestProvider("dg_test", "https://unused.example", server.Client())
	provider.SetBaseURL(server.URL)
	resp, err := provider.ListModels(context.Background())
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, http.MethodGet, req.Method)
	assert.Equal(t, "/models", req.Path)
	assert.Equal(t, "Token dg_test", req.Header.Get("Authorization"))

	modes := make(map[string][]string, len(resp.Data))
	for _, model := range resp.Data {
		require.NotNil(t, model.Metadata)
		assert.NotContains(t, modes, model.ID, "model listed twice")
		modes[model.ID] = model.Metadata.Modes
		assert.Equal(t, "deepgram", model.OwnedBy)
	}
	assert.Equal(t, map[string][]string{
		// "nova-3" is the id Deepgram documents for "nova-3-general".
		"nova-3-general": {"audio_transcription"},
		"nova-3":         {"audio_transcription"},
		"nova-3-medical": {"audio_transcription"},
		"whisper-large":  {"audio_transcription"},
		// Each voice model, plus its family, which takes the voice per request.
		"aura-2-thalia-en": {"audio_speech"},
		"aura-2-agathe-fr": {"audio_speech"},
		"aura-2":           {"audio_speech"},
		"aura-asteria-en":  {"audio_speech"},
		"aura":             {"audio_speech"},
	}, modes, "the streaming-only nova-2-atc is not a pre-recorded model")
}

func TestListModels_ReturnsUpstreamError(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusServiceUnavailable, `unavailable`)

	provider := newTestProvider("key", server.URL, server.Client())
	_, err := provider.ListModels(context.Background())
	require.Error(t, err)
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
	server, capture := providertest.JSONServer(t, http.StatusAccepted, `{"accepted":true}`)

	provider := newTestProvider("dg_test", server.URL, server.Client())
	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodGet,
		Endpoint: "projects",
		Headers:  http.Header{},
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	req := capture.Last(t)
	assert.Equal(t, "/projects", req.Path)
	assert.Equal(t, "Token dg_test", req.Header.Get("Authorization"))
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
}

// The bodies are the error shapes Deepgram documents; the first is what the
// live API answers an invalid key with.
func TestRefineError_ReadsDeepgramErrorShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"legacy shape", `{"err_code":"INVALID_AUTH","err_msg":"Invalid credentials.","request_id":"uuid"}`, "Invalid credentials."},
		{"modern shape adds the details", `{"category":"INVALID_JSON","message":"Invalid JSON submitted.","details":"Json deserialize error: missing field","request_id":"uuid"}`, "Invalid JSON submitted. Json deserialize error: missing field"},
		{"message alone keeps the generic-parser message", `{"message":"something else"}`, "something else"},
		{"not JSON falls back to raw body", `UUID parsing failed`, "UUID parsing failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := core.ParseProviderError("deepgram", http.StatusBadRequest, []byte(tt.body), nil)
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, refineError(original), &gatewayErr)
			assert.Equal(t, tt.want, gatewayErr.Message)
			assert.Equal(t, http.StatusBadRequest, gatewayErr.StatusCode)
		})
	}
}
