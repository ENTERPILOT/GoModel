package core

import (
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeAudioSpeechRequest_Voice(t *testing.T) {
	tests := []struct {
		name      string
		voice     string // raw JSON for the voice member; "" => member absent
		wantVoice string
		wantErr   bool
	}{
		{name: "string", voice: `"alloy"`, wantVoice: "alloy"},
		{name: "object", voice: `{"id":"voice_1234"}`, wantVoice: "voice_1234"},
		{name: "object with extra members", voice: `{"id":"voice_1234","future":{"a":1}}`, wantVoice: "voice_1234"},
		{name: "null", voice: `null`, wantVoice: ""},
		{name: "absent", voice: "", wantVoice: ""},
		{name: "object missing id", voice: `{"name":"x"}`, wantErr: true},
		{name: "object empty id", voice: `{"id":""}`, wantErr: true},
		{name: "object blank id", voice: `{"id":"  "}`, wantErr: true},
		{name: "object null id", voice: `{"id":null}`, wantErr: true},
		{name: "object numeric id", voice: `{"id":42}`, wantErr: true},
		{name: "number", voice: `42`, wantErr: true},
		{name: "array", voice: `["alloy"]`, wantErr: true},
		{name: "boolean", voice: `true`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"model":"tts-1","input":"hi"`
			if tt.voice != "" {
				body += `,"voice":` + tt.voice
			}
			body += `}`

			req, err := DecodeAudioSpeechRequest([]byte(body), nil)
			if tt.wantErr {
				require.Error(t, err)
				gatewayErr := NewInvalidRequestBodyError([]byte(body), err)
				require.NotNil(t, gatewayErr.Param)
				assert.Equal(t, "voice", *gatewayErr.Param)
				assert.Equal(t, ErrorTypeInvalidRequest, gatewayErr.Type)
				assert.Contains(t, gatewayErr.Message, `must be a string or an object with a non-empty string "id"`)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantVoice, req.Voice)
			assert.Equal(t, "tts-1", req.Model)
			assert.True(t, req.ExtraFields.IsEmpty(), "voice must not leak into ExtraFields")
		})
	}
}

func TestAudioSpeechRequest_VoiceRoundTrip(t *testing.T) {
	tests := []struct {
		name      string
		voice     string
		wantVoice any
	}{
		{name: "string", voice: `"alloy"`, wantVoice: "alloy"},
		{name: "object", voice: `{"id":"voice_1234"}`, wantVoice: map[string]any{"id": "voice_1234"}},
		{
			name:      "object keeps extra members",
			voice:     `{"id":"voice_1234","future":{"a":1}}`,
			wantVoice: map[string]any{"id": "voice_1234", "future": map[string]any{"a": float64(1)}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"model":"tts-1","input":"hi","voice":` + tt.voice + `,"stream_format":"sse"}`
			req, err := DecodeAudioSpeechRequest([]byte(body), nil)
			require.NoError(t, err)

			encoded, err := json.Marshal(req)
			require.NoError(t, err)
			var round map[string]any
			require.NoError(t, json.Unmarshal(encoded, &round))
			assert.Equal(t, tt.wantVoice, round["voice"])
			assert.Equal(t, "sse", round["stream_format"])
			assert.Equal(t, "tts-1", round["model"])
			assert.Equal(t, "hi", round["input"])
		})
	}
}

func TestAudioSpeechRequest_StringVoiceMarshalsUnchanged(t *testing.T) {
	body := `{"model":"tts-1","input":"hi","voice":"alloy"}`
	req, err := DecodeAudioSpeechRequest([]byte(body), nil)
	require.NoError(t, err)

	encoded, err := json.Marshal(req)
	require.NoError(t, err)
	assert.JSONEq(t, body, string(encoded))
}

func TestAudioSpeechRequest_ReplacedVoiceIsSentAsString(t *testing.T) {
	req, err := DecodeAudioSpeechRequest([]byte(`{"model":"tts-1","input":"hi","voice":{"id":"voice_1234"}}`), nil)
	require.NoError(t, err)

	replaced := *req
	replaced.Voice = "nova"
	encoded, err := json.Marshal(replaced)
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"tts-1","input":"hi","voice":"nova"}`, string(encoded))
}

func TestAudioSpeechRequest_WithStringVoice(t *testing.T) {
	req, err := DecodeAudioSpeechRequest([]byte(`{"model":"tts-1","input":"hi","voice":{"id":"voice_1234","future":1}}`), nil)
	require.NoError(t, err)

	plain := req.WithStringVoice()
	require.NotNil(t, plain)
	assert.NotSame(t, req, plain)
	encoded, err := json.Marshal(plain)
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"tts-1","input":"hi","voice":"voice_1234"}`, string(encoded))

	original, err := json.Marshal(req)
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"tts-1","input":"hi","voice":{"id":"voice_1234","future":1}}`, string(original),
		"the caller's request must keep the object form")

	stringReq := &AudioSpeechRequest{Model: "tts-1", Input: "hi", Voice: "alloy"}
	assert.Same(t, stringReq, stringReq.WithStringVoice())
}

func TestAudioSpeechRequest_WithObjectVoice(t *testing.T) {
	req := &AudioSpeechRequest{Model: "tts-1", Input: "hi", Voice: `voice_"1234`}

	object := req.WithObjectVoice()
	require.NotNil(t, object)
	assert.NotSame(t, req, object)
	assert.Equal(t, req.Voice, object.Voice)
	encoded, err := json.Marshal(object)
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"tts-1","input":"hi","voice":{"id":"voice_\"1234"}}`, string(encoded))

	original, err := json.Marshal(req)
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"tts-1","input":"hi","voice":"voice_\"1234"}`, string(original),
		"the caller's request must keep the string form")

	assert.Same(t, object, object.WithObjectVoice())
	noVoice := &AudioSpeechRequest{Model: "tts-1", Input: "hi"}
	assert.Same(t, noVoice, noVoice.WithObjectVoice())
	assert.Nil(t, (*AudioSpeechRequest)(nil).WithObjectVoice())

	// A voice replaced after decoding is the one sent as an object.
	replaced, err := DecodeAudioSpeechRequest([]byte(`{"model":"tts-1","input":"hi","voice":{"id":"voice_old"}}`), nil)
	require.NoError(t, err)
	replaced.Voice = "voice_new"
	encoded, err = json.Marshal(replaced.WithObjectVoice())
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"tts-1","input":"hi","voice":{"id":"voice_new"}}`, string(encoded))
}
