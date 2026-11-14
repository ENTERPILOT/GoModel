package minimax

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

func TestListVoices(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"system_voice":[{"voice_id":"English_Graceful_Lady","voice_name":"Graceful Lady","description":["calm"],"created_time":"1970-01-01"}],
		"voice_cloning":[{"voice_id":"clone-1","description":[],"created_time":"2025-08-20"}],
		"voice_generation":null,
		"base_resp":{"status_code":0,"status_msg":"success"}
	}`)
	provider := newTestProvider("minimax-key", server.URL+"/v1", server.Client(), llmclient.Hooks{})

	resp, err := provider.ListVoices(context.Background(), &core.AudioVoicesRequest{Model: "speech-2.8-hd"})
	require.NoError(t, err)
	assert.Equal(t, []core.AudioVoice{
		{ID: "English_Graceful_Lady", Name: "Graceful Lady"},
		{ID: "clone-1", Name: "clone-1"},
	}, resp.Voices)

	req := capture.Last(t)
	assert.Equal(t, http.MethodPost, req.Method)
	assert.Equal(t, "/v1/get_voice", req.Path)
	assert.Equal(t, "Bearer minimax-key", req.Header.Get("Authorization"))
	assert.Equal(t, map[string]any{"voice_type": "all"}, req.JSON(t))
}

func TestListVoices_Errors(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantType   core.ErrorType
	}{
		{name: "base_resp auth failure", status: http.StatusOK, body: `{"base_resp":{"status_code":1004,"status_msg":"login fail"}}`, wantStatus: http.StatusUnauthorized, wantType: core.ErrorTypeAuthentication},
		{name: "http 5xx", status: http.StatusBadGateway, body: `{"error":{"message":"upstream down"}}`, wantStatus: http.StatusBadGateway, wantType: core.ErrorTypeProvider},
		{name: "unparseable body", status: http.StatusOK, body: `not json`, wantStatus: http.StatusBadGateway, wantType: core.ErrorTypeProvider},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, _ := providertest.JSONServer(t, tt.status, tt.body)
			provider := newTestProvider("minimax-key", server.URL+"/v1", server.Client(), llmclient.Hooks{})

			_, err := provider.ListVoices(context.Background(), &core.AudioVoicesRequest{Model: "speech-2.8-hd"})
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, tt.wantType, gatewayErr.Type)
			assert.Equal(t, tt.wantStatus, gatewayErr.HTTPStatusCode())
			assert.NotContains(t, gatewayErr.Error(), "minimax-key")
		})
	}
}
