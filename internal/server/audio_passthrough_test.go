package server

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
)

// TestAudioTranscription_ForwardsUnknownFormFields covers ADR-0011 rule 1 on the
// multipart audio path: form values the gateway does not consume itself (here
// include[], chunking_strategy and a repeated vendor extra) reach the provider,
// while the parts the gateway controls stay out of the passthrough list.
func TestAudioTranscription_ForwardsUnknownFormFields(t *testing.T) {
	mock := &audioMockProvider{
		mockProvider:      &mockProvider{supportedModels: []string{"gpt-4o-transcribe"}},
		transcriptionResp: &core.AudioResponse{ContentType: "application/json", Data: []byte(`{"text":"hi"}`)},
	}
	handler := NewHandler(mock, nil, nil, nil)

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, field := range [][2]string{
		{"model", "gpt-4o-transcribe"},
		{"response_format", "json"},
		{"prompt", "GoModel"},
		{"temperature", "0"},
		{"language", "en"},
		{"timestamp_granularities[]", "word"},
		{"include[]", "logprobs"},
		{"chunking_strategy", "auto"},
		{"x_vendor", "a"},
		{"x_vendor", "b"},
	} {
		err := w.WriteField(field[0], field[1])
		require.NoError(t, err, "WriteField(%s): %v", field[0], err)
	}
	part, err := w.CreateFormFile("file", "speech.mp3")
	require.NoError(t, err)

	_, _ = part.Write([]byte("audio-bytes"))
	err = w.Close()
	require.NoError(t, err)

	c, rec := echotest.Post(t, "/v1/audio/transcriptions", &buf, echotest.WithContentType(w.FormDataContentType()))
	err = handler.AudioTranscriptions(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	captured := mock.capturedTranscription
	require.NotNil(t, captured)

	want := []core.FormField{
		{Name: "chunking_strategy", Value: "auto"},
		{Name: "include[]", Value: "logprobs"},
		{Name: "x_vendor", Value: "a"},
		{Name: "x_vendor", Value: "b"},
	}
	require.Len(t, captured.Fields, len(want), "forwarded fields")

	for i, field := range want {
		assert.Equal(t, field, captured.Fields[i])
	}
	// The gateway-owned parts keep their typed home and never travel twice.
	assert.Equal(t, "en", captured.Language)
	assert.Equal(t, "GoModel", captured.Prompt)
	assert.Equal(t, "json", captured.ResponseFormat, "typed fields mismatch: %+v", captured)
}

// TestPassthroughFormFields_SkipsReservedNames pins the exclusion list so a
// client-supplied value can never overwrite a gateway-controlled multipart part.
func TestPassthroughFormFields_SkipsReservedNames(t *testing.T) {
	form := &multipart.Form{Value: map[string][]string{
		"model": {"evil"}, "file": {"evil"}, "provider": {"evil"},
		"language": {"en"}, "prompt": {"p"}, "response_format": {"json"},
		"temperature": {"0"}, "timestamp_granularities": {"word"},
		"timestamp_granularities[]": {"segment"},
		"include[]":                 {"logprobs"},
	}}
	got := passthroughFormFields(form)
	require.Len(t, got, 1)
	require.Equal(t, core.FormField{Name: "include[]", Value: "logprobs"}, got[0])
	assert.Nil(t, passthroughFormFields(nil))
}

// TestAudioTranscriptionAuditInput_RecordsFieldNamesOnly keeps arbitrary
// client input out of the audit record: a forwarded field may carry a
// provider-native credential, so only its name is stored.
func TestAudioTranscriptionAuditInput_RecordsFieldNamesOnly(t *testing.T) {
	meta := audioTranscriptionAuditInput(&core.AudioTranscriptionRequest{
		Model:    "gpt-4o-transcribe",
		Filename: "speech.wav",
		File:     []byte("audio"),
		Fields: []core.FormField{
			{Name: "include[]", Value: "logprobs"},
			{Name: "x_api_key", Value: "super-secret"},
			{Name: "x_api_key", Value: "super-secret-2"},
		},
	})
	names, ok := meta["forwarded_fields"].([]string)
	require.True(t, ok)
	require.Len(t, names, 2)
	require.Equal(t, "include[]", names[0])
	require.Equal(t, "x_api_key", names[1])

	for key, value := range meta {
		if str, isString := value.(string); isString {
			assert.NotContains(t, str, "super-secret", "audit meta %q leaked a forwarded value", key)
		}
	}
	_, present := meta["x_api_key"]
	assert.False(t, present)
}

// TestAudioSpeech_ForwardsUnknownJSONFields is the JSON half of ADR-0011 rule 1:
// an unknown speech parameter survives the service layer and reaches the
// provider request.
func TestAudioSpeech_ForwardsUnknownJSONFields(t *testing.T) {
	mock := &audioMockProvider{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}},
		speechResp:   &core.AudioResponse{ContentType: "audio/mpeg", Data: []byte("synthetic-audio")},
	}
	handler := NewHandler(mock, nil, nil, nil)

	body := `{"model":"gpt-4o-mini-tts","input":"hello","voice":"alloy","stream_format":"sse"}`
	c, rec := echotest.Post(t, "/v1/audio/speech", body)
	err := handler.AudioSpeech(c)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, mock.capturedSpeech)
	got := string(mock.capturedSpeech.ExtraFields.Lookup("stream_format"))
	assert.Equal(t, `"sse"`, got)
}
