package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
)

// TestAudioSpeech_VoiceObject covers OpenAI's custom-voice object form: it is
// accepted, its id is what providers and the request log read, and it is
// forwarded upstream as the object.
func TestAudioSpeech_VoiceObject(t *testing.T) {
	mock := &audioMockProvider{
		mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}},
		speechResp:   &core.AudioResponse{ContentType: "audio/mpeg", Data: []byte("synthetic-audio")},
	}
	handler := NewHandler(mock, nil, nil, nil)

	body := `{"model":"gpt-4o-mini-tts","input":"hello","voice":{"id":"voice_1234"}}`
	c, rec := echotest.Post(t, "/v1/audio/speech", body)
	require.NoError(t, handler.AudioSpeech(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, mock.capturedSpeech)
	assert.Equal(t, "voice_1234", mock.capturedSpeech.Voice)
	assert.Equal(t, "voice_1234", audioSpeechRequestLogInput(mock.capturedSpeech)["voice"])

	encoded, err := json.Marshal(mock.capturedSpeech)
	require.NoError(t, err)
	var sent map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &sent))
	assert.JSONEq(t, `{"id":"voice_1234"}`, string(sent["voice"]))
}

func TestAudioSpeech_InvalidVoiceIsRejected(t *testing.T) {
	tests := []struct {
		name  string
		voice string
	}{
		{name: "object without id", voice: `{"name":"x"}`},
		{name: "object with empty id", voice: `{"id":""}`},
		{name: "object with numeric id", voice: `{"id":7}`},
		{name: "number", voice: `7`},
		{name: "array", voice: `["alloy"]`},
		{name: "boolean", voice: `false`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &audioMockProvider{mockProvider: &mockProvider{supportedModels: []string{"gpt-4o-mini-tts"}}}
			handler := NewHandler(mock, nil, nil, nil)

			body := `{"model":"gpt-4o-mini-tts","input":"hello","voice":` + tt.voice + `}`
			c, rec := echotest.Post(t, "/v1/audio/speech", body)
			require.NoError(t, handler.AudioSpeech(c))
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Nil(t, mock.capturedSpeech)

			var resp struct {
				Error struct {
					Type    string  `json:"type"`
					Param   *string `json:"param"`
					Message string  `json:"message"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			assert.Equal(t, "invalid_request_error", resp.Error.Type)
			require.NotNil(t, resp.Error.Param)
			assert.Equal(t, "voice", *resp.Error.Param)
			assert.Contains(t, resp.Error.Message, `voice: must be a string or an object with a non-empty string "id"`)
		})
	}
}
