package openai

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/goccy/go-json"
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

// newNativeVoicesProvider returns a provider configured for OpenAI's own API
// whose requests reach a fake upstream serving handler.
func newNativeVoicesProvider(t *testing.T, baseURL string, handler http.HandlerFunc) (*Provider, *providertest.Capture) {
	t.Helper()
	server, capture := providertest.Server(t, handler)
	target, err := url.Parse(server.URL)
	require.NoError(t, err)
	opts := providertest.Options(llmclient.Hooks{})
	opts.HTTPClient = &http.Client{Transport: redirectTransport{target: target}}
	return New(providers.ProviderConfig{APIKey: "sk-test", BaseURL: baseURL}, opts).(*Provider), capture
}

func voicesHandler(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

const noCustomVoices = `{"object":"list","data":[],"first_id":null,"last_id":null,"has_more":false}`

var (
	allBuiltinVoices    = voiceList("alloy", "ash", "ballad", "coral", "echo", "fable", "nova", "onyx", "sage", "shimmer", "verse", "marin", "cedar")
	legacyBuiltinVoices = voiceList("alloy", "ash", "coral", "echo", "fable", "nova", "onyx", "sage", "shimmer")
)

func TestListVoices_NativeOpenAIServesBuiltinVoices(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		model   string
		want    []core.AudioVoice
	}{
		{name: "gpt-4o-mini-tts", model: "gpt-4o-mini-tts", want: allBuiltinVoices},
		{name: "tts-1", model: "tts-1", want: legacyBuiltinVoices},
		{name: "tts-1-hd", model: "tts-1-hd", want: legacyBuiltinVoices},
		{name: "tts-1-hd snapshot", model: "tts-1-hd-1106", want: legacyBuiltinVoices},
		{name: "gpt-4o-mini-tts snapshot", model: "gpt-4o-mini-tts-2025-12-15", want: allBuiltinVoices},
		{name: "explicit OpenAI base URL", baseURL: "https://API.openai.com/v1/", model: "tts-1", want: legacyBuiltinVoices},
		{name: "regional OpenAI host", baseURL: "https://eu.api.openai.com/v1", model: "gpt-4o-mini-tts", want: allBuiltinVoices},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, _ := newNativeVoicesProvider(t, tt.baseURL, voicesHandler(http.StatusOK, noCustomVoices))

			resp, err := provider.ListVoices(context.Background(), &core.AudioVoicesRequest{Model: tt.model})
			require.NoError(t, err)
			assert.Equal(t, tt.want, resp.Voices)
		})
	}
}

// Custom voices follow the built-in ones. Prompt-generated voices are left
// out: the speech endpoint does not accept them.
func TestListVoices_NativeOpenAIAppendsCustomVoices(t *testing.T) {
	provider, capture := newNativeVoicesProvider(t, "", voicesHandler(http.StatusOK, `{"object":"list","data":[
		{"object":"audio.voice","id":"voice_rec1","name":"Narrator","type":"audio_sample","created_at":1734220800},
		{"object":"audio.voice","id":"voice_prompt1","name":"Generated","type":"prompt","prompt":"a calm voice"},
		{"object":"audio.voice","id":"voice_rec2","name":" "},
		{"object":"audio.voice","id":"","name":"no id"}
	],"first_id":"voice_rec1","last_id":"voice_rec2","has_more":false}`))

	for _, tt := range []struct {
		model    string
		builtins []core.AudioVoice
	}{
		{model: "gpt-4o-mini-tts", builtins: allBuiltinVoices},
		{model: "tts-1", builtins: legacyBuiltinVoices},
	} {
		t.Run(tt.model, func(t *testing.T) {
			resp, err := provider.ListVoices(context.Background(), &core.AudioVoicesRequest{Model: tt.model})
			require.NoError(t, err)
			want := append(append([]core.AudioVoice{}, tt.builtins...),
				core.AudioVoice{ID: "voice_rec1", Name: "Narrator"},
				core.AudioVoice{ID: "voice_rec2", Name: "voice_rec2"})
			assert.Equal(t, want, resp.Voices)

			req := capture.Last(t)
			assert.Equal(t, http.MethodGet, req.Method)
			assert.Equal(t, "/v1/audio/voices", req.Path)
			assert.Equal(t, url.Values{"limit": {"100"}}, req.Query)
			assert.Equal(t, "Bearer sk-test", req.Header.Get("Authorization"))
		})
	}
}

func TestListVoices_NativeOpenAIFollowsCustomVoicePages(t *testing.T) {
	pages := map[string]string{
		"":        `{"data":[{"id":"voice_a","name":"A"}],"last_id":"voice_a","has_more":true}`,
		"voice_a": `{"data":[{"id":"voice_b","name":"B"}],"last_id":"voice_b","has_more":true}`,
		"voice_b": `{"data":[{"id":"voice_c","name":"C"}],"last_id":"voice_c","has_more":false}`,
	}
	provider, capture := newNativeVoicesProvider(t, "", func(w http.ResponseWriter, r *http.Request) {
		voicesHandler(http.StatusOK, pages[r.URL.Query().Get("after")])(w, r)
	})

	resp, err := provider.ListVoices(context.Background(), &core.AudioVoicesRequest{Model: "tts-1"})
	require.NoError(t, err)
	want := append(append([]core.AudioVoice{}, legacyBuiltinVoices...),
		core.AudioVoice{ID: "voice_a", Name: "A"}, core.AudioVoice{ID: "voice_b", Name: "B"}, core.AudioVoice{ID: "voice_c", Name: "C"})
	assert.Equal(t, want, resp.Voices)
	assert.Equal(t, 3, capture.Count())
}

// A listing that never ends stops at the page cap instead of crawling on.
func TestListVoices_NativeOpenAIStopsAtCustomVoicePageCap(t *testing.T) {
	provider, capture := newNativeVoicesProvider(t, "", func(w http.ResponseWriter, r *http.Request) {
		id := "voice_" + r.URL.Query().Get("after") + "x"
		voicesHandler(http.StatusOK, `{"data":[{"id":"`+id+`","name":"n"}],"last_id":"`+id+`","has_more":true}`)(w, r)
	})

	resp, err := provider.ListVoices(context.Background(), &core.AudioVoicesRequest{Model: "tts-1"})
	require.NoError(t, err)
	assert.Len(t, resp.Voices, len(legacyBuiltinVoices)+maxCustomVoicePages)
	assert.Equal(t, maxCustomVoicePages, capture.Count())

	custom, err := provider.customVoices(context.Background(), "tts-1", customVoicesTimeout)
	require.ErrorContains(t, err, "more than 5 pages")
	assert.Len(t, custom, maxCustomVoicePages)
}

// A page that announces more voices without a cursor cannot be followed; it is
// reported instead of passing for the end of the list.
func TestCustomVoices_MorePagesWithoutCursorIsAnError(t *testing.T) {
	provider, capture := newNativeVoicesProvider(t, "", voicesHandler(http.StatusOK,
		`{"data":[{"id":"voice_a","name":"A"}],"last_id":null,"has_more":true}`))

	custom, err := provider.customVoices(context.Background(), "tts-1", customVoicesTimeout)
	require.ErrorIs(t, err, core.ErrUnrecognizedVoices)
	assert.Equal(t, []core.AudioVoice{{ID: "voice_a", Name: "A"}}, custom)
	assert.Equal(t, 1, capture.Count())
}

// The built-in voices do not depend on the custom voice lookup succeeding.
func TestListVoices_NativeOpenAIKeepsBuiltinVoicesWhenCustomListingFails(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "missing permission", status: http.StatusForbidden, body: `{"error":{"message":"Missing scopes: api.voices.read","type":"invalid_request_error"}}`},
		{name: "no such route", status: http.StatusNotFound, body: `{"error":{"message":"Invalid URL"}}`},
		{name: "upstream failure", status: http.StatusInternalServerError, body: `{"error":{"message":"boom","type":"server_error"}}`},
		{name: "unrecognized body", status: http.StatusOK, body: `{"object":"list"}`},
		{name: "not JSON", status: http.StatusOK, body: `<html>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, capture := newNativeVoicesProvider(t, "", voicesHandler(tt.status, tt.body))

			resp, err := provider.ListVoices(context.Background(), &core.AudioVoicesRequest{Model: "gpt-4o-mini-tts"})
			require.NoError(t, err)
			assert.Equal(t, allBuiltinVoices, resp.Voices)
			assert.Positive(t, capture.Count())
		})
	}
}

// A cursor that does not advance would replay the same page until the cap.
func TestCustomVoices_StalledCursorIsAnError(t *testing.T) {
	provider, capture := newNativeVoicesProvider(t, "", voicesHandler(http.StatusOK,
		`{"data":[{"id":"voice_a","name":"A"}],"last_id":"voice_a","has_more":true}`))

	custom, err := provider.customVoices(context.Background(), "tts-1", customVoicesTimeout)
	require.ErrorIs(t, err, core.ErrUnrecognizedVoices)
	assert.Equal(t, []core.AudioVoice{{ID: "voice_a", Name: "A"}}, custom)
	assert.Equal(t, 2, capture.Count())
}

// One key serves every page of a listing; rotation advances between listings.
func TestCustomVoices_PinsOneKeyPerListing(t *testing.T) {
	server, capture := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after") == "" {
			voicesHandler(http.StatusOK, `{"data":[{"id":"voice_a","name":"A"}],"last_id":"voice_a","has_more":true}`)(w, r)
			return
		}
		voicesHandler(http.StatusOK, `{"data":[{"id":"voice_b","name":"B"}],"last_id":"voice_b","has_more":false}`)(w, r)
	})
	target, err := url.Parse(server.URL)
	require.NoError(t, err)
	opts := providertest.Options(llmclient.Hooks{})
	opts.HTTPClient = &http.Client{Transport: redirectTransport{target: target}}
	opts.Keys = providers.NewKeyring("sk-a", "sk-b")
	provider := New(providers.ProviderConfig{}, opts).(*Provider)

	for range 2 {
		_, err := provider.customVoices(context.Background(), "tts-1", customVoicesTimeout)
		require.NoError(t, err)
	}

	requests := capture.All()
	require.Len(t, requests, 4)
	assert.Equal(t, requests[0].Header.Get("Authorization"), requests[1].Header.Get("Authorization"))
	assert.Equal(t, requests[2].Header.Get("Authorization"), requests[3].Header.Get("Authorization"))
	assert.NotEqual(t, requests[0].Header.Get("Authorization"), requests[2].Header.Get("Authorization"), "rotation still advances between listings")
}

// A stalled upstream is cut off by the lookup's own deadline, so it costs the
// caller the custom voices, not the built-in ones.
func TestCustomVoices_StalledUpstreamHitsTheLookupDeadline(t *testing.T) {
	provider, _ := newNativeVoicesProvider(t, "", func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})

	custom, err := provider.customVoices(context.Background(), "tts-1", 50*time.Millisecond)
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Empty(t, custom)
}

// A key restricted from reading voices is a standing condition, not worth a
// warning on every listing.
func TestCustomVoicesFailureLevel(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   slog.Level
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"error":{"message":"Missing scopes: api.voices.read"}}`, want: slog.LevelInfo},
		{name: "forbidden", status: http.StatusForbidden, body: `{"error":{"message":"Missing scopes: api.voices.read"}}`, want: slog.LevelInfo},
		{name: "no such route", status: http.StatusNotFound, body: `{"error":{"message":"Invalid URL"}}`, want: slog.LevelWarn},
		{name: "upstream failure", status: http.StatusInternalServerError, body: `{"error":{"message":"boom"}}`, want: slog.LevelWarn},
		{name: "unrecognized body", status: http.StatusOK, body: `{"object":"list"}`, want: slog.LevelWarn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, _ := newNativeVoicesProvider(t, "", voicesHandler(tt.status, tt.body))

			_, err := provider.customVoices(context.Background(), "tts-1", customVoicesTimeout)
			require.Error(t, err)
			assert.Equal(t, tt.want, customVoicesFailureLevel(err))
		})
	}
}

func TestListVoices_NativeOpenAIKeepsCustomVoicesReadBeforeAFailure(t *testing.T) {
	provider, _ := newNativeVoicesProvider(t, "", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after") == "" {
			voicesHandler(http.StatusOK, `{"data":[{"id":"voice_a","name":"A"}],"last_id":"voice_a","has_more":true}`)(w, r)
			return
		}
		voicesHandler(http.StatusNotFound, `{"error":{"message":"No voice found with id 'voice_a'"}}`)(w, r)
	})

	resp, err := provider.ListVoices(context.Background(), &core.AudioVoicesRequest{Model: "tts-1"})
	require.NoError(t, err)
	assert.Equal(t, append(append([]core.AudioVoice{}, legacyBuiltinVoices...), core.AudioVoice{ID: "voice_a", Name: "A"}), resp.Voices)
}

// Realtime and chat models take other voice sets, so the speech list is not
// offered for them.
func TestListVoices_NativeOpenAIRejectsNonSpeechModels(t *testing.T) {
	for _, model := range []string{"gpt-realtime", "gpt-4o-audio-preview", "gpt-4o"} {
		t.Run(model, func(t *testing.T) {
			provider, capture := newNativeVoicesProvider(t, "", voicesHandler(http.StatusOK, noCustomVoices))

			_, err := provider.ListVoices(context.Background(), &core.AudioVoicesRequest{Model: model})
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, http.StatusBadRequest, gatewayErr.HTTPStatusCode())
			assert.Contains(t, gatewayErr.Message, "does not support listing voices")
			assert.Zero(t, capture.Count())
		})
	}
}

// OpenAI takes a custom voice ID only as an object, while clients that pick a
// voice from the listing send its id as a string.
func TestCreateSpeech_NativeOpenAISendsCustomVoiceIDAsObject(t *testing.T) {
	audio := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("audio-bytes"))
	}
	tests := []struct {
		name string
		body string
		want string // raw JSON of the upstream voice member
	}{
		{name: "custom voice id string", body: `{"model":"tts-1","input":"hi","voice":"voice_1234"}`, want: `{"id":"voice_1234"}`},
		{name: "custom voice object unchanged", body: `{"model":"tts-1","input":"hi","voice":{"id":"voice_1234","future":1}}`, want: `{"id":"voice_1234","future":1}`},
		{name: "built-in voice", body: `{"model":"tts-1","input":"hi","voice":"nova"}`, want: `"nova"`},
		{name: "default voice", body: `{"model":"tts-1","input":"hi"}`, want: `"alloy"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, capture := newNativeVoicesProvider(t, "", audio)
			req, err := core.DecodeAudioSpeechRequest([]byte(tt.body), nil)
			require.NoError(t, err)

			_, err = provider.CreateSpeech(context.Background(), req)
			require.NoError(t, err)

			assert.Equal(t, "/v1/audio/speech", capture.Last(t).Path)
			var sent map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(capture.Last(t).Body, &sent))
			assert.JSONEq(t, tt.want, string(sent["voice"]))
		})
	}

	t.Run("custom base URL keeps the string", func(t *testing.T) {
		server, capture := providertest.Server(t, audio)
		provider := New(providers.ProviderConfig{APIKey: "sk-local", BaseURL: server.URL + "/v1"}, providertest.Options(llmclient.Hooks{})).(*Provider)

		_, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{Model: "kokoro", Input: "hi", Voice: "voice_alba"})
		require.NoError(t, err)
		assert.Equal(t, "/v1/audio/speech", capture.Last(t).Path)
		assert.Equal(t, "voice_alba", capture.Last(t).JSON(t)["voice"])
	})
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

// Without a model the provider serves OpenAI's own voice list as the upstream
// returns it: one page, cursor parameters forwarded, prompt voices included.
func TestListCustomVoices_ForwardsOnePage(t *testing.T) {
	provider, capture := newNativeVoicesProvider(t, "", voicesHandler(http.StatusOK, `{"object":"list","data":[
		{"object":"audio.voice","id":"voice_rec1","name":"Narrator","type":"audio_sample","created_at":1734220800,"future":true},
		{"object":"audio.voice","id":"voice_prompt1","name":"Generated","type":"prompt","prompt":"a calm voice","created_at":1734220801}
	],"first_id":"voice_rec1","last_id":"voice_prompt1","has_more":true}`))

	resp, err := provider.ListCustomVoices(context.Background(), core.AudioCustomVoicesParams{Limit: "2", After: "voice_0"})
	require.NoError(t, err)

	first, last := "voice_rec1", "voice_prompt1"
	assert.Equal(t, &core.AudioCustomVoicesResponse{
		Object: "list",
		Data: []core.AudioCustomVoice{
			{ID: "voice_rec1", Object: "audio.voice", Name: "Narrator", CreatedAt: 1734220800, Type: "audio_sample"},
			{ID: "voice_prompt1", Object: "audio.voice", Name: "Generated", CreatedAt: 1734220801, Type: "prompt", Prompt: "a calm voice"},
		},
		FirstID: &first,
		LastID:  &last,
		HasMore: true,
	}, resp)
	req := capture.Last(t)
	assert.Equal(t, http.MethodGet, req.Method)
	assert.Equal(t, "/v1/audio/voices", req.Path)
	assert.Equal(t, url.Values{"limit": {"2"}, "after": {"voice_0"}}, req.Query)
	assert.Equal(t, 1, capture.Count())
}

func TestListCustomVoices_EmptyOrganization(t *testing.T) {
	provider, capture := newNativeVoicesProvider(t, "", voicesHandler(http.StatusOK, noCustomVoices))

	assert.True(t, provider.ServesCustomVoices())
	resp, err := provider.ListCustomVoices(context.Background(), core.AudioCustomVoicesParams{})
	require.NoError(t, err)
	assert.Equal(t, &core.AudioCustomVoicesResponse{Object: "list", Data: []core.AudioCustomVoice{}}, resp)
	assert.Empty(t, capture.Last(t).Query, "no cursor parameter is invented")
}

func TestListCustomVoices_Errors(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantMsg    string
	}{
		{name: "upstream rejects the limit", status: http.StatusBadRequest, body: `{"error":{"message":"Invalid 'limit': integer above maximum value.","type":"invalid_request_error","param":"limit"}}`, wantStatus: http.StatusBadRequest, wantMsg: "Invalid 'limit'"},
		{name: "unknown cursor", status: http.StatusNotFound, body: `{"error":{"message":"No voice found with id 'voice_x'","type":"invalid_request_error"}}`, wantStatus: http.StatusNotFound, wantMsg: "No voice found"},
		{name: "unrecognized body", status: http.StatusOK, body: `{"object":"list"}`, wantStatus: http.StatusBadGateway, wantMsg: "unrecognized voices response"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, _ := newNativeVoicesProvider(t, "", voicesHandler(tt.status, tt.body))

			_, err := provider.ListCustomVoices(context.Background(), core.AudioCustomVoicesParams{Limit: "101"})
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, tt.wantStatus, gatewayErr.HTTPStatusCode())
			assert.Contains(t, gatewayErr.Message, tt.wantMsg)
			assert.NotContains(t, gatewayErr.Error(), "sk-test")
		})
	}
}

// A self-hosted server registered as type openai does not have OpenAI's list.
func TestListCustomVoices_CustomBaseURLIsUnsupported(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"voices":["alba"]}`)
	provider := New(providers.ProviderConfig{APIKey: "sk-local", BaseURL: server.URL + "/v1"}, providertest.Options(llmclient.Hooks{})).(*Provider)

	assert.False(t, provider.ServesCustomVoices())
	_, err := provider.ListCustomVoices(context.Background(), core.AudioCustomVoicesParams{})
	require.ErrorIs(t, err, core.ErrAudioCustomVoicesUnsupported)
	assert.Zero(t, capture.Count())
}
