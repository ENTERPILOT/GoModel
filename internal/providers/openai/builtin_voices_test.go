package openai

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// redirectTransport sends every request to target, so a provider configured
// for api.openai.com reaches the fake upstream instead of the internet.
type redirectTransport struct{ target *url.URL }

func (t redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme, req.URL.Host = t.target.Scheme, t.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

func voiceList(ids ...string) []core.AudioVoice {
	voices := make([]core.AudioVoice, 0, len(ids))
	for _, id := range ids {
		voices = append(voices, core.AudioVoice{ID: id, Name: id})
	}
	return voices
}

func TestListVoices_NativeOpenAIServesBuiltinVoices(t *testing.T) {
	all := voiceList("alloy", "ash", "ballad", "coral", "echo", "fable", "nova", "onyx", "sage", "shimmer", "verse", "marin", "cedar")
	legacy := voiceList("alloy", "ash", "coral", "echo", "fable", "nova", "onyx", "sage", "shimmer")
	tests := []struct {
		name    string
		baseURL string
		model   string
		want    []core.AudioVoice
	}{
		{name: "gpt-4o-mini-tts", model: "gpt-4o-mini-tts", want: all},
		{name: "tts-1", model: "tts-1", want: legacy},
		{name: "tts-1-hd", model: "tts-1-hd", want: legacy},
		{name: "tts-1-hd snapshot", model: "tts-1-hd-1106", want: legacy},
		{name: "other model gets every voice", model: "gpt-realtime", want: all},
		{name: "explicit OpenAI base URL", baseURL: "https://API.openai.com/v1/", model: "tts-1", want: legacy},
		{name: "regional OpenAI host", baseURL: "https://eu.api.openai.com/v1", model: "gpt-4o-mini-tts", want: all},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusNotFound, `{"error":{"message":"Invalid URL"}}`)
			target, err := url.Parse(server.URL)
			require.NoError(t, err)
			opts := providertest.Options(llmclient.Hooks{})
			opts.HTTPClient = &http.Client{Transport: redirectTransport{target: target}}
			provider := New(providers.ProviderConfig{APIKey: "sk-test", BaseURL: tt.baseURL}, opts).(*Provider)

			resp, err := provider.ListVoices(context.Background(), &core.AudioVoicesRequest{Model: tt.model})
			require.NoError(t, err)
			assert.Equal(t, tt.want, resp.Voices)
			assert.Zero(t, capture.Count(), "OpenAI has no voice listing; no upstream request is expected")
		})
	}
}

// TestListVoices_SetBaseURLOverrideForwards checks a base URL override away
// from OpenAI is honored at call time.
func TestListVoices_SetBaseURLOverrideForwards(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"voices":["alba"]}`)
	provider := New(providers.ProviderConfig{APIKey: "sk-local"}, providertest.Options(llmclient.Hooks{})).(*Provider)
	provider.SetBaseURL(server.URL + "/v1")

	resp, err := provider.ListVoices(context.Background(), &core.AudioVoicesRequest{Model: "tts-1"})
	require.NoError(t, err)
	assert.Equal(t, voiceList("alba"), resp.Voices)
	assert.Equal(t, "/v1/audio/voices", capture.Last(t).Path)
}

func TestIsOpenAIHost(t *testing.T) {
	tests := []struct {
		baseURL string
		want    bool
	}{
		{"https://api.openai.com/v1", true},
		{"https://api.openai.com:443/v1", true},
		{"https://eu.api.openai.com/v1", true},
		{"http://localhost:8880/v1", false},
		{"https://myresource.openai.azure.com/openai", false},
		{"https://openrouter.ai/api/v1", false},
		{"https://api.openai.com.example.com/v1", false},
		{"https://notapi.openai.com/v1", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.baseURL, func(t *testing.T) {
			assert.Equal(t, tt.want, isOpenAIHost(tt.baseURL))
		})
	}
}
