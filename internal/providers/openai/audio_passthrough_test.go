package openai

import (
	"context"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateTranscription_ForwardsExtraFormFields checks that fields the gateway
// does not consume itself reach the upstream multipart body unchanged
// (ADR-0011 rule 1), including repeated values, and that a reserved name cannot
// be duplicated through the passthrough list.
func TestCreateTranscription_ForwardsExtraFormFields(t *testing.T) {
	provider, capture := newTestProvider(t, jsonHandler(`{"text":"hi"}`))

	_, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:    "gpt-4o-transcribe",
		Filename: "speech.wav",
		File:     []byte("wave-bytes"),
		Fields: []core.FormField{
			{Name: "include[]", Value: "logprobs"},
			{Name: "chunking_strategy", Value: "auto"},
			{Name: "x_vendor", Value: "a"},
			{Name: "x_vendor", Value: "b"},
			// A reserved name must never be re-emitted from the extras.
			{Name: "model", Value: "evil"},
		},
	})
	require.NoError(t, err)

	got := recordedMultipart(t, capture.Last(t)).values
	assert.Equal(t, []string{"logprobs"}, got["include[]"])
	assert.Equal(t, []string{"auto"}, got["chunking_strategy"])
	assert.Equal(t, []string{"a", "b"}, got["x_vendor"])
	assert.Equal(t, []string{"gpt-4o-transcribe"}, got["model"])
}

// TestCreateTranslation_ForwardsExtraFormFields covers the same passthrough on
// the translations endpoint, which shares the multipart builder.
func TestCreateTranslation_ForwardsExtraFormFields(t *testing.T) {
	provider, capture := newTestProvider(t, jsonHandler(`{"text":"hi"}`))

	_, err := provider.CreateTranslation(context.Background(), &core.AudioTranscriptionRequest{
		Model:    "whisper-1",
		Filename: "speech.wav",
		File:     []byte("wave-bytes"),
		Fields:   []core.FormField{{Name: "x_vendor", Value: "a"}},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, recordedMultipart(t, capture.Last(t)).values["x_vendor"])
}

// TestCreateTranscription_HostileFieldNamesKeepMultipartFraming pins the
// framing guarantee of the passthrough path: a forwarded field name or value is
// client input, so it must stay inside its own part. Each case would otherwise
// let a caller graft a second gateway-controlled part onto the upstream body.
func TestCreateTranscription_HostileFieldNamesKeepMultipartFraming(t *testing.T) {
	tests := []struct {
		name  string
		field core.FormField
	}{
		{"crlf in name", core.FormField{
			Name:  "evil\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nhijacked\r\n",
			Value: "v",
		}},
		{"quote escape promoting to a file part", core.FormField{
			Name: "x\"; filename=\"boom.txt", Value: "v",
		}},
		{"forged boundary in value", core.FormField{
			Name:  "ok",
			Value: "v\r\n--boundary\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\nhijacked",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, capture := newTestProvider(t, jsonHandler(`{"text":"hi"}`))
			_, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
				Model:    "gpt-4o-transcribe",
				Filename: "speech.wav",
				File:     []byte("wave-bytes"),
				Fields:   []core.FormField{tt.field},
			})
			require.NoError(t, err)

			form := recordedMultipart(t, capture.Last(t))
			assert.Len(t, form.files, 1)
			assert.Equal(t, []string{"gpt-4o-transcribe"}, form.values["model"])
		})
	}
}

// TestCreateTranscription_StreamedResponseKeepsEventStreamType ensures a
// forwarded stream=true is not mislabelled as JSON: the client needs the
// upstream text/event-stream type to parse the body it gets back.
func TestCreateTranscription_StreamedResponseKeepsEventStreamType(t *testing.T) {
	provider, _ := newTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = w.Write([]byte("data: {\"type\":\"transcript.text.delta\"}\n\n"))
	})

	resp, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:    "gpt-4o-transcribe",
		Filename: "speech.wav",
		File:     []byte("wave-bytes"),
		Fields:   []core.FormField{{Name: "stream", Value: "true"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "text/event-stream; charset=utf-8", resp.ContentType)
}

// TestCreateSpeech_ForwardsUnknownJSONFields checks the JSON audio path: an
// unknown parameter (OpenAI's stream_format) is merged into the upstream body.
func TestCreateSpeech_ForwardsUnknownJSONFields(t *testing.T) {
	provider, capture := newTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"speech.audio.delta\"}\n\n"))
	})

	req, err := core.DecodeAudioSpeechRequest([]byte(
		`{"model":"gpt-4o-mini-tts","input":"hi","voice":"alloy","stream_format":"sse"}`), nil)
	require.NoError(t, err)

	resp, err := provider.CreateSpeech(context.Background(), req)
	require.NoError(t, err)

	body := capture.Last(t).JSON(t)
	assert.Equal(t, "sse", body["stream_format"], "unknown field must be forwarded")
	assert.Equal(t, "gpt-4o-mini-tts", body["model"])
	assert.Equal(t, "hi", body["input"])
	assert.Equal(t, "alloy", body["voice"])

	// The upstream answered with a stream; its Content-Type must survive so the
	// client can parse the events.
	assert.Equal(t, "text/event-stream", resp.ContentType)
}
