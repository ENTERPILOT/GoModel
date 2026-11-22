package minimax

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type flashTestTransport struct {
	target   *url.URL
	base     http.RoundTripper
	upstream string
}

func (t *flashTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.upstream = req.URL.String()
	local := req.Clone(req.Context())
	local.URL.Scheme = t.target.Scheme
	local.URL.Host = t.target.Host
	return t.base.RoundTrip(local)
}

func TestFlashRequests(t *testing.T) {
	for _, baseURL := range []string{"", "https://api.minimaxi.com/v1", "https://custom.example/v1"} {
		for _, streaming := range []bool{false, true} {
			for _, effort := range []string{"", "low", "medium", "high", "xhigh", "max"} {
				t.Run(baseURL+"/"+effort+"/"+map[bool]string{false: "chat", true: "stream"}[streaming], func(t *testing.T) {
					server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
					if streaming {
						server, capture = providertest.SSEServer(t, providertest.ChatChunkSSE)
					}
					target, err := url.Parse(server.URL)
					require.NoError(t, err)
					transport := &flashTestTransport{target: target, base: server.Client().Transport}
					provider := newTestProvider("fixture-key", baseURL, &http.Client{Transport: transport}, llmclient.Hooks{})
					var req core.ChatRequest
					require.NoError(t, json.Unmarshal([]byte(`{"model":"MiniMax-M3.1-Flash","temperature":0,"messages":[{"role":"user","content":[{"type":"text","text":"Describe"},{"type":"image_url","image_url":{"url":"https://example.com/image.png"}},{"type":"video_url","video_url":{"url":"mm_file://clip","fps":2}}]}]}`), &req))
					if effort != "" {
						req.Reasoning = &core.Reasoning{Effort: effort}
					}
					if streaming {
						body, err := provider.StreamChatCompletion(context.Background(), &req)
						require.NoError(t, err)
						_, err = io.ReadAll(body)
						require.NoError(t, err)
						require.NoError(t, body.Close())
					} else {
						_, err := provider.ChatCompletion(context.Background(), &req)
						require.NoError(t, err)
					}
					endpoint := baseURL
					if endpoint == "" {
						endpoint = defaultBaseURL
					}
					assert.Equal(t, endpoint+"/chat/completions", transport.upstream)
					sent := capture.Last(t).JSON(t)
					assert.Equal(t, "MiniMax-M3.1-Flash", sent["model"])
					assert.Equal(t, true, sent["reasoning_split"])
					assert.Equal(t, float64(0), sent["temperature"])
					assert.NotContains(t, sent, "reasoning")
					assert.NotContains(t, sent, "thinking")
					assert.NotContains(t, sent, "service_tier")
					if effort == "" {
						assert.NotContains(t, sent, "reasoning_effort")
					} else {
						assert.Equal(t, effort, sent["reasoning_effort"])
					}
					original, err := json.Marshal(req.Messages)
					require.NoError(t, err)
					actual, err := json.Marshal(sent["messages"])
					require.NoError(t, err)
					assert.JSONEq(t, string(original), string(actual))
					assert.Nil(t, req.ExtraFields.Lookup("reasoning_split"), "caller request must remain unchanged")
				})
			}
		}
	}
}

func TestFlashResponsesEffort(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
	provider := newTestProvider("fixture-key", server.URL, server.Client(), llmclient.Hooks{})
	var req core.ResponsesRequest
	require.NoError(t, json.Unmarshal([]byte(`{"model":"MiniMax-M3.1-Flash","input":"Hello","reasoning":{"effort":"max"}}`), &req))
	_, err := provider.Responses(context.Background(), &req)
	require.NoError(t, err)
	sent := capture.Last(t).JSON(t)
	assert.Equal(t, "MiniMax-M3.1-Flash", sent["model"])
	assert.Equal(t, "max", sent["reasoning_effort"])
	assert.Equal(t, true, sent["reasoning_split"])
	assert.NotContains(t, sent, "reasoning")
}

func TestCurrentAndSavedModelRequests(t *testing.T) {
	for _, model := range []string{"MiniMax-M3", "MiniMax-M2.7", "custom-saved-model"} {
		t.Run(model, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)
			provider := newTestProvider("fixture-key", server.URL, server.Client(), llmclient.Hooks{})
			var req core.ChatRequest
			require.NoError(t, json.Unmarshal([]byte(`{"messages":[{"role":"user","content":"Hello"}],"thinking":{"type":"disabled"},"reasoning_split":false,"reasoning_effort":"low"}`), &req))
			req.Model = model
			_, err := provider.ChatCompletion(context.Background(), &req)
			require.NoError(t, err)
			sent := capture.Last(t).JSON(t)
			assert.Equal(t, model, sent["model"])
			assert.Equal(t, false, sent["reasoning_split"])
			assert.Equal(t, "low", sent["reasoning_effort"])
			assert.Equal(t, map[string]any{"type": "disabled"}, sent["thinking"])
		})
	}
}
