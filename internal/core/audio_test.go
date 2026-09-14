package core

import (
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpeechResponseContentType(t *testing.T) {
	cases := map[string]string{
		"":       "audio/mpeg",
		"mp3":    "audio/mpeg",
		"MP3":    "audio/mpeg",
		"  wav ": "audio/wav",
		"opus":   "audio/ogg",
		"aac":    "audio/aac",
		"flac":   "audio/flac",
		"pcm":    "audio/pcm",
		"bogus":  "application/octet-stream",
	}
	for format, want := range cases {
		got := SpeechResponseContentType(format)
		assert.Equal(t, want, got)
	}
}

func TestTranscriptionResponseContentType(t *testing.T) {
	cases := map[string]string{
		"":             "application/json",
		"json":         "application/json",
		"verbose_json": "application/json",
		"text":         "text/plain; charset=utf-8",
		"srt":          "text/plain; charset=utf-8",
		"VTT":          "text/plain; charset=utf-8",
		"unknown":      "application/json",
	}
	for format, want := range cases {
		got := TranscriptionResponseContentType(format)
		assert.Equal(t, want, got)
	}
}

func TestDecodeAudioSpeechRequest(t *testing.T) {
	req, err := DecodeAudioSpeechRequest([]byte(`{"model":"gpt-4o-mini-tts","input":"hi","voice":"alloy","response_format":"wav","speed":1.5}`), nil)
	require.NoError(t, err)
	require.Equal(t, "gpt-4o-mini-tts", req.Model)
	require.Equal(t, "hi", req.Input)
	require.Equal(t, "alloy", req.Voice)
	require.Equal(t, "wav", req.ResponseFormat)
	require.Equal(t, 1.5, req.Speed, "decoded request mismatch: %+v", req)
	_, err = DecodeAudioSpeechRequest([]byte(`{"model":`), nil)
	require.Error(t, err)
}

// TestAudioSpeechRequest_PreservesUnknownFields covers ADR-0011 rule 1 on
// /v1/audio/speech: a parameter the gateway has no typed field for (here
// OpenAI's stream_format) must survive the decode and reach the provider body.
func TestAudioSpeechRequest_PreservesUnknownFields(t *testing.T) {
	body := []byte(`{"model":"gpt-4o-mini-tts","input":"hi","voice":"alloy","stream_format":"sse","x_vendor":{"beta":true}}`)

	req, err := DecodeAudioSpeechRequest(body, nil)
	require.NoError(t, err)
	require.Equal(t, "gpt-4o-mini-tts", req.Model)
	require.Equal(t, "hi", req.Input)
	require.Equal(t, "alloy", req.Voice, "typed fields mismatch: %+v", req)
	got := string(req.ExtraFields.Lookup("stream_format"))
	assert.Equal(t, `"sse"`, got)
	got = string(req.ExtraFields.Lookup("x_vendor"))
	assert.Equal(t, `{"beta":true}`, got)

	encoded, err := json.Marshal(req)
	require.NoError(t, err)

	var round map[string]any
	err = json.Unmarshal(encoded, &round)
	require.NoError(t, err)

	for field, want := range map[string]any{
		"model": "gpt-4o-mini-tts", "input": "hi", "voice": "alloy", "stream_format": "sse",
	} {
		assert.Equal(t, want, round[field])
	}
	// Typed members must not be duplicated into the extras on the way out.
	assert.Equal(t, 1, strings.Count(string(encoded), `"model"`), "model duplicated in encoded body: %s", encoded)
}

// TestAudioSpeechRequest_KnownFieldsAreNotExtras guards the gateway-controlled
// members: a client cannot smuggle a second model/voice/provider through the
// passthrough object.
func TestAudioSpeechRequest_KnownFieldsAreNotExtras(t *testing.T) {
	req, err := DecodeAudioSpeechRequest([]byte(
		`{"model":"tts-1","input":"hi","voice":"alloy","instructions":"calm","response_format":"wav","speed":1.5,"provider":"openai"}`), nil)
	require.NoError(t, err)
	require.True(t, req.ExtraFields.IsEmpty(), "ExtraFields = %+v, want empty", req.ExtraFields)
	require.Equal(t, "wav", req.ResponseFormat)
	require.Equal(t, 1.5, req.Speed)
	require.Equal(t, "calm", req.Instructions)
	require.Equal(t, "openai", req.Provider, "typed fields mismatch: %+v", req)
}
