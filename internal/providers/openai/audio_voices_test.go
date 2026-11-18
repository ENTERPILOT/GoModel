package openai

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

var _ core.AudioVoicesProvider = (*Provider)(nil)

func newVoicesTestProvider(t *testing.T, status int, body string) (*CompatibleProvider, *providertest.Capture) {
	t.Helper()
	server, capture := providertest.JSONServer(t, status, body)
	provider := NewCompatibleProvider(
		"test-key",
		providertest.Options(llmclient.Hooks{}),
		CompatibleProviderConfig{ProviderName: "kokoro", BaseURL: server.URL + "/v1", SetHeaders: setHeaders},
	)
	return provider, capture
}

func TestListVoices_ForwardsToAudioVoices(t *testing.T) {
	provider, capture := newVoicesTestProvider(t, http.StatusOK,
		`{"voices":[{"id":"af_bella","name":"af_bella","overall_grade":"A-"},{"id":"am_adam","name":"am_adam"}],"default_voice":"af_heart"}`)

	resp, err := provider.ListVoices(context.Background(), &core.AudioVoicesRequest{Model: "kokoro"})
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, http.MethodGet, req.Method)
	assert.Equal(t, "/v1/audio/voices", req.Path)
	assert.Equal(t, "kokoro", req.Query.Get("model"))
	assert.Equal(t, "Bearer test-key", req.Header.Get("Authorization"))
	assert.Empty(t, req.Body)
	assert.Equal(t, []core.AudioVoice{{ID: "af_bella", Name: "af_bella"}, {ID: "am_adam", Name: "am_adam"}}, resp.Voices)
}

func TestListVoices_UpstreamErrors(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantType   core.ErrorType
		wantStatus int
		wantMsg    string
	}{
		{
			name:       "404 means the upstream has no voice listing",
			status:     http.StatusNotFound,
			body:       `{"error":{"message":"Invalid URL (GET /v1/audio/voices)","type":"invalid_request_error"}}`,
			wantType:   core.ErrorTypeInvalidRequest,
			wantStatus: http.StatusBadRequest,
			wantMsg:    `model "kokoro" does not support listing voices`,
		},
		{
			name:       "405 means the upstream has no voice listing",
			status:     http.StatusMethodNotAllowed,
			body:       `{"detail":"Method Not Allowed"}`,
			wantType:   core.ErrorTypeInvalidRequest,
			wantStatus: http.StatusBadRequest,
			wantMsg:    `model "kokoro" does not support listing voices`,
		},
		{
			name:       "5xx stays an upstream failure",
			status:     http.StatusInternalServerError,
			body:       `{"error":{"message":"Failed to retrieve voice list","type":"server_error"}}`,
			wantType:   core.ErrorTypeProvider,
			wantStatus: http.StatusInternalServerError,
			wantMsg:    "Failed to retrieve voice list",
		},
		{
			name:       "unrecognized 200 body is a bad gateway",
			status:     http.StatusOK,
			body:       `{"object":"list"}`,
			wantType:   core.ErrorTypeProvider,
			wantStatus: http.StatusBadGateway,
			wantMsg:    "kokoro returned an unrecognized voices response",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, _ := newVoicesTestProvider(t, tt.status, tt.body)

			_, err := provider.ListVoices(context.Background(), &core.AudioVoicesRequest{Model: "kokoro"})
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, tt.wantType, gatewayErr.Type)
			assert.Equal(t, tt.wantStatus, gatewayErr.HTTPStatusCode())
			assert.Contains(t, gatewayErr.Message, tt.wantMsg)
			assert.NotContains(t, gatewayErr.Error(), "test-key")
		})
	}
}

// TestListVoices_OpenAIProviderInheritsForwarding checks a self-hosted server
// registered as type openai reaches its voice listing.
func TestListVoices_OpenAIProviderInheritsForwarding(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"voices":["alba"]}`)
	provider := New(providers.ProviderConfig{APIKey: "sk-local", BaseURL: server.URL + "/v1"}, providertest.Options(llmclient.Hooks{})).(*Provider)

	resp, err := provider.ListVoices(context.Background(), &core.AudioVoicesRequest{Model: "tts-1"})
	require.NoError(t, err)
	assert.Equal(t, []core.AudioVoice{{ID: "alba", Name: "alba"}}, resp.Voices)
	assert.Equal(t, "/v1/audio/voices", capture.Last(t).Path)
}
