//go:build contract

// Contract tests in this file are intended to run with: -tags=contract -timeout=5m.
package contract

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"gomodel/internal/core"
)

func openAIAudioProvider(t *testing.T, routes map[string]replayRoute) core.AudioProvider {
	t.Helper()
	provider := newOpenAIReplayProvider(t, routes)
	audio, ok := provider.(core.AudioProvider)
	require.True(t, ok, "openai provider should implement core.AudioProvider")
	return audio
}

func TestOpenAIReplayCreateSpeech(t *testing.T) {
	testCases := []struct {
		name           string
		responseFormat string
		wantType       string
	}{
		{name: "default", responseFormat: "", wantType: "audio/mpeg"},
		{name: "wav", responseFormat: "wav", wantType: "audio/wav"},
		{name: "opus", responseFormat: "opus", wantType: "audio/ogg"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			audioBytes := []byte("\x00\x01synthetic-audio")
			audio := openAIAudioProvider(t, map[string]replayRoute{
				replayKey(http.MethodPost, "/audio/speech"): {
					statusCode:  http.StatusOK,
					contentType: "audio/mpeg",
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
			// Content type is derived from the requested format, not the upstream header.
			require.Equal(t, tc.wantType, resp.ContentType)
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
