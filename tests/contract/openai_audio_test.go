//go:build contract

// Contract tests in this file are intended to run with: -tags=contract -timeout=5m.
package contract

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/openai"
)

func openAIAudioProvider(t *testing.T, routes map[string]replayRoute) core.AudioProvider {
	t.Helper()
	provider := newOpenAIReplayProvider(t, routes)
	audio, ok := provider.(core.AudioProvider)
	require.True(t, ok, "openai provider should implement core.AudioProvider")
	return audio
}

func TestOpenAIReplayCreateSpeech(t *testing.T) {
	// The response is tagged with the upstream Content-Type, which describes the
	// bytes actually returned (usage prices output-duration models from it). The
	// requested response_format does not override it — the last case returns mp3
	// for a wav request to prove the upstream type wins.
	testCases := []struct {
		name           string
		responseFormat string
		upstreamType   string
	}{
		{name: "mp3 default", responseFormat: "", upstreamType: "audio/mpeg"},
		{name: "wav", responseFormat: "wav", upstreamType: "audio/wav"},
		{name: "opus", responseFormat: "opus", upstreamType: "audio/opus"},
		{name: "upstream overrides requested format", responseFormat: "wav", upstreamType: "audio/mpeg"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			audioBytes := []byte("\x00\x01synthetic-audio")
			audio := openAIAudioProvider(t, map[string]replayRoute{
				replayKey(http.MethodPost, "/audio/speech"): {
					statusCode:  http.StatusOK,
					contentType: tc.upstreamType,
					body:        audioBytes,
				},
			})

			resp, err := audio.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
				Model:          "gpt-4o-mini-tts",
				Input:          "Hello from the gateway.",
				Voice:          "alloy",
				ResponseFormat: tc.responseFormat,
			})
			require.NoError(t, err)
			require.NotNil(t, resp)
			require.Equal(t, tc.upstreamType, resp.ContentType)
			require.Equal(t, audioBytes, resp.Data)
		})
	}
}

func TestOpenAIReplayCreateSpeechValidation(t *testing.T) {
	audio := openAIAudioProvider(t, map[string]replayRoute{})

	_, err := audio.CreateSpeech(context.Background(), &core.AudioSpeechRequest{Model: "gpt-4o-mini-tts", Voice: "alloy"})
	require.Error(t, err, "missing input should be rejected before any upstream call")

	_, err = audio.CreateSpeech(context.Background(), &core.AudioSpeechRequest{Model: "gpt-4o-mini-tts", Input: "hi"})
	require.Error(t, err, "missing voice should be rejected before any upstream call")
}

func TestOpenAIReplayCreateTranscription(t *testing.T) {
	body := []byte(`{"text":"hello world"}`)
	audio := openAIAudioProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/audio/transcriptions"): {
			statusCode:  http.StatusOK,
			contentType: "application/json",
			body:        body,
		},
	})

	resp, err := audio.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:    "gpt-4o-transcribe",
		Filename: "speech.mp3",
		File:     []byte("fake-audio-bytes"),
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, "application/json", resp.ContentType)
	require.JSONEq(t, `{"text":"hello world"}`, string(resp.Data))
}

func TestOpenAIReplayCreateTranscriptionTextFormat(t *testing.T) {
	audio := openAIAudioProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/audio/transcriptions"): {
			statusCode:  http.StatusOK,
			contentType: "text/plain",
			body:        []byte("hello world"),
		},
	})

	resp, err := audio.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:          "gpt-4o-transcribe",
		Filename:       "speech.mp3",
		File:           []byte("fake-audio-bytes"),
		ResponseFormat: "text",
	})
	require.NoError(t, err)
	require.Equal(t, "text/plain; charset=utf-8", resp.ContentType)
	require.Equal(t, "hello world", string(resp.Data))
}

func TestOpenAIReplayCreateTranscriptionUpstreamError(t *testing.T) {
	audio := openAIAudioProvider(t, map[string]replayRoute{
		replayKey(http.MethodPost, "/audio/transcriptions"): {
			statusCode:  http.StatusBadRequest,
			contentType: "application/json",
			body:        []byte(`{"error":{"message":"invalid file"}}`),
		},
	})

	_, err := audio.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:    "gpt-4o-transcribe",
		Filename: "speech.mp3",
		File:     []byte("fake-audio-bytes"),
	})
	require.Error(t, err)
}

// capturedRequest records what the provider sent upstream so request-translation
// can be asserted at the wire level.
type capturedRequest struct {
	method      string
	path        string
	contentType string
	body        []byte
}

type capturingTransport struct {
	t        *testing.T
	captured *capturedRequest
	respType string
	respBody []byte
}

func (ct *capturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ct.t.Helper()
	var body []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		require.NoError(ct.t, err)
		body = b
	}
	*ct.captured = capturedRequest{
		method:      req.Method,
		path:        req.URL.Path,
		contentType: req.Header.Get("Content-Type"),
		body:        body,
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{ct.respType}},
		Body:       io.NopCloser(bytes.NewReader(ct.respBody)),
		Request:    req,
	}, nil
}

func newOpenAICapturingAudio(t *testing.T, respType string, respBody []byte) (core.AudioProvider, *capturedRequest) {
	t.Helper()
	captured := &capturedRequest{}
	client := &http.Client{Transport: &capturingTransport{t: t, captured: captured, respType: respType, respBody: respBody}}
	provider := openai.NewWithHTTPClient("sk-test", client, llmclient.Hooks{})
	provider.SetBaseURL("https://replay.local")
	// *openai.Provider implements core.AudioProvider via the embedded CompatibleProvider.
	return provider, captured
}

// TestOpenAIAudioSpeechRequestTranslation verifies the speech request fields are
// forwarded verbatim in the outbound JSON body.
func TestOpenAIAudioSpeechRequestTranslation(t *testing.T) {
	audio, captured := newOpenAICapturingAudio(t, "audio/mpeg", []byte("AUDIO"))

	_, err := audio.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model:          "gpt-4o-mini-tts",
		Input:          "hello world",
		Voice:          "alloy",
		Instructions:   "speak cheerfully",
		ResponseFormat: "wav",
		Speed:          1.25,
	})
	require.NoError(t, err)

	require.Equal(t, http.MethodPost, captured.method)
	require.Equal(t, "/audio/speech", captured.path)
	require.Contains(t, captured.contentType, "application/json")

	var payload map[string]any
	require.NoError(t, json.Unmarshal(captured.body, &payload))
	require.Equal(t, "gpt-4o-mini-tts", payload["model"])
	require.Equal(t, "hello world", payload["input"])
	require.Equal(t, "alloy", payload["voice"])
	require.Equal(t, "speak cheerfully", payload["instructions"])
	require.Equal(t, "wav", payload["response_format"])
	require.InEpsilon(t, 1.25, payload["speed"], 0.0001)
}

// TestOpenAIAudioTranscriptionRequestTranslation verifies the transcription
// request fields are forwarded as multipart form parts, including the bracketed
// timestamp_granularities[] key and the audio file part.
func TestOpenAIAudioTranscriptionRequestTranslation(t *testing.T) {
	audio, captured := newOpenAICapturingAudio(t, "application/json", []byte(`{"text":"x"}`))

	_, err := audio.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:                  "gpt-4o-transcribe",
		Filename:               "speech.wav",
		File:                   []byte("audio-bytes"),
		Language:               "en",
		Prompt:                 "a hint",
		ResponseFormat:         "verbose_json",
		Temperature:            "0.2",
		TimestampGranularities: []string{"word", "segment"},
	})
	require.NoError(t, err)

	require.Equal(t, http.MethodPost, captured.method)
	require.Equal(t, "/audio/transcriptions", captured.path)
	require.Contains(t, captured.contentType, "multipart/form-data")

	_, params, err := mime.ParseMediaType(captured.contentType)
	require.NoError(t, err)
	form, err := multipart.NewReader(bytes.NewReader(captured.body), params["boundary"]).ReadForm(1 << 20)
	require.NoError(t, err)
	defer func() { _ = form.RemoveAll() }()

	firstValue := func(key string) string {
		values := form.Value[key]
		require.NotEmpty(t, values, "missing form field %q", key)
		return values[0]
	}
	require.Equal(t, "gpt-4o-transcribe", firstValue("model"))
	require.Equal(t, "en", firstValue("language"))
	require.Equal(t, "a hint", firstValue("prompt"))
	require.Equal(t, "verbose_json", firstValue("response_format"))
	require.Equal(t, "0.2", firstValue("temperature"))
	require.Equal(t, []string{"word", "segment"}, form.Value["timestamp_granularities[]"])

	require.Len(t, form.File["file"], 1)
	require.Equal(t, "speech.wav", form.File["file"][0].Filename)
}
