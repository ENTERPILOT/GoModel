package deepgram

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// listenBody follows the pre-recorded result Deepgram documents: the transcript
// and its words per channel alternative, and utterances when asked for.
const listenBody = `{
	"metadata": {"request_id":"a847f427","duration":4.25,"channels":1},
	"results": {
		"channels": [{
			"detected_language": "en",
			"alternatives": [{
				"transcript": "Hello there. How are you?",
				"confidence": 0.98,
				"words": [
					{"word":"hello","start":0.08,"end":0.4,"confidence":0.99,"punctuated_word":"Hello"},
					{"word":"there","start":0.4,"end":0.9,"confidence":0.98,"punctuated_word":"there."}
				]
			}]
		}],
		"utterances": [
			{"start":0.08,"end":0.9,"confidence":0.98,"channel":0,"transcript":"Hello there.","id":"u1"},
			{"start":3661.5,"end":3663.004,"confidence":0.97,"channel":0,"transcript":"How are you?","id":"u2"}
		]
	}
}`

func transcribe(t *testing.T, req *core.AudioTranscriptionRequest) (*core.AudioResponse, providertest.Recorded) {
	t.Helper()
	server, capture := providertest.JSONServer(t, http.StatusOK, listenBody)
	provider := newTestProvider("dg_test", server.URL, server.Client())
	resp, err := provider.CreateTranscription(context.Background(), req)
	require.NoError(t, err)
	return resp, capture.Last(t)
}

func TestCreateTranscription_SendsAudioBodyAndReturnsJSON(t *testing.T) {
	resp, req := transcribe(t, &core.AudioTranscriptionRequest{
		Model:           "nova-3",
		Filename:        "clip.wav",
		FileContentType: "audio/wav",
		File:            []byte("fake-audio-bytes"),
		Language:        "en",
	})

	assert.Equal(t, http.MethodPost, req.Method)
	assert.Equal(t, "/listen", req.Path)
	assert.Equal(t, "Token dg_test", req.Header.Get("Authorization"))
	assert.Equal(t, "audio/wav", req.Header.Get("Content-Type"))
	assert.Equal(t, "fake-audio-bytes", string(req.Body), "the audio is the request body, not a multipart part")
	assert.Equal(t, url.Values{
		"model":        {"nova-3"},
		"language":     {"en"},
		"smart_format": {"true"},
	}, req.Query)

	assert.Equal(t, "application/json", resp.ContentType)
	assert.JSONEq(t, `{"text":"Hello there. How are you?","usage":{"type":"duration","seconds":4.25}}`, string(resp.Data))
	assert.Zero(t, resp.AudioSeconds, "json carries its usage in the body")
}

func TestCreateTranscription_DetectsLanguageWhenNoneIsGiven(t *testing.T) {
	_, req := transcribe(t, &core.AudioTranscriptionRequest{Model: "nova-3", File: []byte("audio")})

	assert.Equal(t, "true", req.Query.Get("detect_language"))
	assert.False(t, req.Query.Has("language"))
	assert.Equal(t, "application/octet-stream", req.Header.Get("Content-Type"))
}

func TestCreateTranscription_ForwardsDeepgramOptions(t *testing.T) {
	_, req := transcribe(t, &core.AudioTranscriptionRequest{
		Model:    "nova-3",
		File:     []byte("audio"),
		Language: "en",
		Fields: []core.FormField{
			{Name: "keyterm", Value: "GoModel"},
			{Name: "keyterm", Value: "Deepgram"},
			{Name: "diarize", Value: "true"},
			{Name: "smart_format", Value: "false"},
			{Name: "detect_language", Value: "true"},
			{Name: "stream", Value: "false"},
			// Gateway-controlled parameters cannot be set through a field.
			{Name: "model", Value: "nova-2"},
			{Name: "language", Value: "de"},
		},
	})

	assert.Equal(t, url.Values{
		"model":        {"nova-3"},
		"language":     {"en"},
		"keyterm":      {"GoModel", "Deepgram"},
		"diarize":      {"true"},
		"smart_format": {"false"},
	}, req.Query)
}

func TestCreateTranscription_VerboseJSONSegmentsAndWords(t *testing.T) {
	tests := []struct {
		name          string
		granularities []string
		wantSegments  bool
		wantWords     bool
	}{
		{"segments by default", nil, true, false},
		{"words only", []string{"word"}, false, true},
		{"both", []string{"word", "segment"}, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, req := transcribe(t, &core.AudioTranscriptionRequest{
				Model:                  "nova-3",
				File:                   []byte("audio"),
				ResponseFormat:         "verbose_json",
				TimestampGranularities: tt.granularities,
			})
			assert.Equal(t, "true", req.Query.Get("utterances"))

			want := `"task":"transcribe","language":"en","duration":4.25,"text":"Hello there. How are you?","usage":{"type":"duration","seconds":4.25}`
			if tt.wantSegments {
				want += `,"segments":[{"id":0,"start":0.08,"end":0.9,"text":"Hello there."},{"id":1,"start":3661.5,"end":3663.004,"text":"How are you?"}]`
			}
			if tt.wantWords {
				want += `,"words":[{"word":"hello","start":0.08,"end":0.4},{"word":"there","start":0.4,"end":0.9}]`
			}
			assert.JSONEq(t, "{"+want+"}", string(resp.Data))
		})
	}
}

// A forwarded diarize=true makes Deepgram label speakers, which verbose_json
// carries on its segments and words.
func TestCreateTranscription_VerboseJSONKeepsSpeakers(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"metadata": {"duration": 2},
		"results": {
			"channels": [{"alternatives": [{"transcript": "Hi. Hello.", "words": [
				{"word":"hi","start":0,"end":0.5,"speaker":0,"speaker_confidence":0.9},
				{"word":"hello","start":1,"end":1.5,"speaker":1,"speaker_confidence":0.8}
			]}]}],
			"utterances": [
				{"start":0,"end":0.5,"transcript":"Hi.","speaker":0},
				{"start":1,"end":1.5,"transcript":"Hello.","speaker":1}
			]
		}
	}`)
	provider := newTestProvider("key", server.URL, server.Client())
	resp, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:                  "nova-3",
		File:                   []byte("audio"),
		Language:               "en",
		ResponseFormat:         "verbose_json",
		TimestampGranularities: []string{"word", "segment"},
		Fields:                 []core.FormField{{Name: "diarize", Value: "true"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "true", capture.Last(t).Query.Get("diarize"))

	assert.JSONEq(t, `{
		"task":"transcribe","language":"en","duration":2,"text":"Hi. Hello.",
		"usage":{"type":"duration","seconds":2},
		"segments":[{"id":0,"start":0,"end":0.5,"text":"Hi.","speaker":0},{"id":1,"start":1,"end":1.5,"text":"Hello.","speaker":1}],
		"words":[{"word":"hi","start":0,"end":0.5,"speaker":0},{"word":"hello","start":1,"end":1.5,"speaker":1}]
	}`, string(resp.Data))
}

func TestCreateTranscription_TextAndCaptionFormats(t *testing.T) {
	tests := []struct {
		format         string
		wantUtterances bool
		want           string
	}{
		{"text", false, "Hello there. How are you?"},
		{"srt", true, "1\n00:00:00,080 --> 00:00:00,900\nHello there.\n\n2\n01:01:01,500 --> 01:01:03,004\nHow are you?\n\n"},
		{"vtt", true, "WEBVTT\n\n00:00:00.080 --> 00:00:00.900\nHello there.\n\n01:01:01.500 --> 01:01:03.004\nHow are you?\n\n"},
	}
	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			resp, req := transcribe(t, &core.AudioTranscriptionRequest{
				Model: "nova-3", File: []byte("audio"), ResponseFormat: tt.format,
			})
			assert.Equal(t, tt.wantUtterances, req.Query.Has("utterances"))
			assert.Equal(t, tt.want, string(resp.Data))
			assert.True(t, strings.HasPrefix(resp.ContentType, "text/plain"), "content type = %q, want text/plain", resp.ContentType)
			// The body has no room for usage, so the duration rides beside it.
			assert.InDelta(t, 4.25, resp.AudioSeconds, 1e-9)
		})
	}
}

func TestUploadContentType_PassesOnlyMediaTypes(t *testing.T) {
	tests := map[string]string{
		"audio/mpeg":               "audio/mpeg",
		"Video/MP4":                "video/mp4",
		"":                         "application/octet-stream",
		"application/octet-stream": "application/octet-stream",
		// Deepgram reads a JSON body as a request to fetch a URL.
		"application/json": "application/octet-stream",
	}
	for contentType, want := range tests {
		assert.Equal(t, want, uploadContentType(contentType), "content type %q", contentType)
	}
}

func TestCreateTranscription_ValidatesRequest(t *testing.T) {
	provider := newTestProvider("key", "https://example.invalid", nil)
	audio := []byte("a")
	tests := []struct {
		name string
		req  *core.AudioTranscriptionRequest
		want string
	}{
		{name: "nil request", req: nil, want: "request is required"},
		{name: "missing model", req: &core.AudioTranscriptionRequest{File: audio}, want: "model is required"},
		{name: "missing file", req: &core.AudioTranscriptionRequest{Model: "nova-3"}, want: "file is required"},
		{name: "bad format", req: &core.AudioTranscriptionRequest{Model: "nova-3", File: audio, ResponseFormat: "diarized_json"}, want: "supports json, text, verbose_json, srt, or vtt"},
		{name: "prompt", req: &core.AudioTranscriptionRequest{Model: "nova-3", File: audio, Prompt: "context"}, want: "does not support prompt"},
		{name: "granularity", req: &core.AudioTranscriptionRequest{Model: "nova-3", File: audio, TimestampGranularities: []string{"sentence"}}, want: "word or segment"},
		{name: "stream", req: &core.AudioTranscriptionRequest{Model: "nova-3", File: audio, Fields: []core.FormField{{Name: "stream", Value: "true"}}}, want: "does not support stream"},
		{name: "callback", req: &core.AudioTranscriptionRequest{Model: "nova-3", File: audio, Fields: []core.FormField{{Name: "callback", Value: "https://example.com"}}}, want: "does not support callback"},
		{name: "multichannel", req: &core.AudioTranscriptionRequest{Model: "nova-3", File: audio, Fields: []core.FormField{{Name: "multichannel", Value: "true"}}}, want: "does not support multichannel"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := provider.CreateTranscription(context.Background(), tt.req)
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, http.StatusBadRequest, gatewayErr.StatusCode)
			assert.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
			assert.Contains(t, gatewayErr.Message, tt.want)
		})
	}
}

func TestCreateTranscription_ReturnsUpstreamError(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusBadRequest,
		`{"err_code":"Bad Request","err_msg":"Bad Request: failed to process audio: corrupt or unsupported data","request_id":"uuid"}`)

	provider := newTestProvider("key", server.URL, server.Client())
	_, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model: "nova-3", File: []byte("not audio"),
	})
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusBadRequest, gatewayErr.StatusCode)
	assert.Equal(t, "Bad Request: failed to process audio: corrupt or unsupported data", gatewayErr.Message)
}

// A 200 body that is not a transcription result (for example the request id a
// callback request is answered with) is an upstream failure, not an empty
// transcript.
func TestCreateTranscription_RejectsUnrecognizedResponse(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"request_id":"a847f427"}`)

	provider := newTestProvider("key", server.URL, server.Client())
	_, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model: "nova-3", File: []byte("audio"),
	})
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusBadGateway, gatewayErr.StatusCode)
}
