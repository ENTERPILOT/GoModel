package audiocpp

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
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"voices":["alba","cosette","marius"]}`)
	// A trailing /v1 is trimmed: the client is rooted at the server root.
	provider := newTestProvider("proxy-key", server.URL+"/v1", server.Client(), llmclient.Hooks{})

	resp, err := provider.ListVoices(context.Background(), &core.AudioVoicesRequest{Model: "pocket-tts"})
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, http.MethodGet, req.Method)
	assert.Equal(t, "/v1/audio/voices", req.Path)
	assert.Equal(t, "pocket-tts", req.Query.Get("model"))
	assert.Equal(t, "Bearer proxy-key", req.Header.Get("Authorization"))
	assert.Equal(t, []core.AudioVoice{
		{ID: "alba", Name: "alba"},
		{ID: "cosette", Name: "cosette"},
		{ID: "marius", Name: "marius"},
	}, resp.Voices)
}

func TestListVoices_Errors(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		model      string
		wantStatus int
		wantType   core.ErrorType
	}{
		{name: "missing model", status: http.StatusOK, body: `{"voices":[]}`, model: " ", wantStatus: http.StatusBadRequest, wantType: core.ErrorTypeInvalidRequest},
		{name: "upstream 5xx", status: http.StatusServiceUnavailable, body: `{"error":{"message":"loading"}}`, model: "pocket-tts", wantStatus: http.StatusServiceUnavailable, wantType: core.ErrorTypeProvider},
		{name: "unrecognized body", status: http.StatusOK, body: `{"ok":true}`, model: "pocket-tts", wantStatus: http.StatusBadGateway, wantType: core.ErrorTypeProvider},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, _ := providertest.JSONServer(t, tt.status, tt.body)
			provider := newTestProvider("", server.URL, server.Client(), llmclient.Hooks{})

			_, err := provider.ListVoices(context.Background(), &core.AudioVoicesRequest{Model: tt.model})
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, tt.wantType, gatewayErr.Type)
			assert.Equal(t, tt.wantStatus, gatewayErr.HTTPStatusCode())
		})
	}
}
