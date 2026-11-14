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
	"github.com/enterpilot/gomodel/internal/providers"
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

// With several keys configured, every page of one listing must use the same
// key: a next_page_token belongs to the workspace that issued it.
func TestListVoices_KeepsOneKeyAcrossPages(t *testing.T) {
	server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/v2/voices": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("next_page_token") == "" {
				_, _ = io.WriteString(w, `{"voices":[{"voice_id":"v1","name":"Rachel"}],"has_more":true,"next_page_token":"page-2"}`)
				return
			}
			_, _ = io.WriteString(w, `{"voices":[{"voice_id":"v2","name":"Adam"}],"has_more":false}`)
		},
	})
	opts := providertest.Options(llmclient.Hooks{})
	opts.HTTPClient = server.Client()
	opts.Keys = providers.NewKeyring("xi-key-a", "xi-key-b")
	provider := New(providers.ProviderConfig{BaseURL: server.URL}, opts).(*Provider)

	// Two listings, so the rotation starts once on each key.
	for range 2 {
		_, err := provider.ListVoices(context.Background(), &core.AudioVoicesRequest{Model: "eleven_multilingual_v2"})
		require.NoError(t, err)
	}

	requests := capture.All()
	require.Len(t, requests, 4)
	assert.Equal(t, requests[0].Header.Get("xi-api-key"), requests[1].Header.Get("xi-api-key"))
	assert.Equal(t, requests[2].Header.Get("xi-api-key"), requests[3].Header.Get("xi-api-key"))
	assert.NotEqual(t, requests[0].Header.Get("xi-api-key"), requests[2].Header.Get("xi-api-key"), "rotation still advances between listings")
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
