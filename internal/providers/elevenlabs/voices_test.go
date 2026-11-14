package elevenlabs

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

func TestListVoices_FollowsPagination(t *testing.T) {
	server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/v2/voices": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("next_page_token") == "" {
				_, _ = io.WriteString(w, `{"voices":[{"voice_id":"v1","name":"Rachel","category":"premade"}],"has_more":true,"next_page_token":"page-2"}`)
				return
			}
			_, _ = io.WriteString(w, `{"voices":[{"voice_id":"v2","name":"Adam"}],"has_more":false,"next_page_token":null}`)
		},
	})
	provider := newTestProvider("xi-secret", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.ListVoices(context.Background(), &core.AudioVoicesRequest{Model: "eleven_multilingual_v2"})
	require.NoError(t, err)
	assert.Equal(t, []core.AudioVoice{{ID: "v1", Name: "Rachel"}, {ID: "v2", Name: "Adam"}}, resp.Voices)

	requests := capture.All()
	require.Len(t, requests, 2)
	for _, req := range requests {
		assert.Equal(t, http.MethodGet, req.Method)
		assert.Equal(t, "/v2/voices", req.Path)
		assert.Equal(t, "100", req.Query.Get("page_size"))
		assert.Equal(t, "xi-secret", req.Header.Get("xi-api-key"))
	}
	assert.Empty(t, requests[0].Query.Get("next_page_token"))
	assert.Equal(t, "page-2", requests[1].Query.Get("next_page_token"))
}

func TestListVoices_RefinesUpstreamError(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusUnauthorized, `{"detail":{"status":"invalid_api_key","message":"Invalid API key"}}`)
	provider := newTestProvider("bad", server.URL, server.Client(), llmclient.Hooks{})

	_, err := provider.ListVoices(context.Background(), &core.AudioVoicesRequest{Model: "eleven_multilingual_v2"})
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusUnauthorized, gatewayErr.HTTPStatusCode())
	assert.Equal(t, "Invalid API key", gatewayErr.Message)
}
