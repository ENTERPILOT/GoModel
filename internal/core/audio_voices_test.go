package core

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAudioVoices(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []AudioVoice
	}{
		{
			name: "audio.cpp and xn-ptts: object of id strings",
			body: `{"voices":["alba","marius"],"aliases":{"narrator":"alba"}}`,
			want: []AudioVoice{{ID: "alba", Name: "alba"}, {ID: "marius", Name: "marius"}},
		},
		{
			name: "Kokoro-FastAPI: objects with id, name, and extra fields",
			body: `{"voices":[{"id":"af_bella","name":"af_bella","overall_grade":"A-"}],"default_voice":"af_heart"}`,
			want: []AudioVoice{{ID: "af_bella", Name: "af_bella"}},
		},
		{
			name: "Speaches: objects with id and language",
			body: `{"voices":[{"id":"af_heart","name":"af_heart","language":"en-us","gender":"female"}],"object":"list"}`,
			want: []AudioVoice{{ID: "af_heart", Name: "af_heart"}},
		},
		{
			name: "ElevenLabs: voice_id with display name",
			body: `{"voices":[{"voice_id":"21m00Tcm4TlvDq8ikWAM","name":"Rachel","category":"premade"}]}`,
			want: []AudioVoice{{ID: "21m00Tcm4TlvDq8ikWAM", Name: "Rachel"}},
		},
		{
			name: "MiniMax: voice_id with voice_name",
			body: `[{"voice_id":"English_Graceful_Lady","voice_name":"Graceful Lady","description":["calm"]},{"voice_id":"clone-1"}]`,
			want: []AudioVoice{{ID: "English_Graceful_Lady", Name: "Graceful Lady"}, {ID: "clone-1", Name: "clone-1"}},
		},
		{
			name: "LocalAI: data grouped per model, voices named only",
			body: `{"data":[{"model":"kokoro","voices":[{"name":"af_sky","language":"en"},{"name":"am_adam"}]}]}`,
			want: []AudioVoice{{ID: "af_sky", Name: "af_sky"}, {ID: "am_adam", Name: "am_adam"}},
		},
		{
			name: "data envelope of plain voices",
			body: `{"data":[{"id":"v1","name":"Voice One"}]}`,
			want: []AudioVoice{{ID: "v1", Name: "Voice One"}},
		},
		{
			name: "Mistral-style items envelope",
			body: `{"items":[{"voice_id":"v2","name":"Voice Two"}]}`,
			want: []AudioVoice{{ID: "v2", Name: "Voice Two"}},
		},
		{
			name: "bare array of strings",
			body: `["a","b"]`,
			want: []AudioVoice{{ID: "a", Name: "a"}, {ID: "b", Name: "b"}},
		},
		{
			name: "blank ids, duplicates, and junk items are dropped",
			body: `[" a ","", {"id":" "}, 7, null, {"id":"a","name":"dup"}, {"id":"b","name":"  "}]`,
			want: []AudioVoice{{ID: "a", Name: "a"}, {ID: "b", Name: "b"}},
		},
		{
			name: "empty list",
			body: `{"voices":[]}`,
			want: []AudioVoice{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAudioVoices([]byte(tt.body))
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseAudioVoices_RejectsUnrecognizedShapes(t *testing.T) {
	for _, body := range []string{``, `null`, `"alba"`, `{}`, `{"voices":"alba"}`, `<html>not found</html>`, `[`} {
		t.Run(body, func(t *testing.T) {
			_, err := ParseAudioVoices([]byte(body))
			assert.ErrorIs(t, err, ErrUnrecognizedVoices)
		})
	}
}

func TestAudioVoicesFromBody_UnrecognizedIsBadGateway(t *testing.T) {
	_, err := AudioVoicesFromBody("kokoro", []byte(`{"unexpected":true}`))
	var gatewayErr *GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, ErrorTypeProvider, gatewayErr.Type)
	assert.Equal(t, http.StatusBadGateway, gatewayErr.HTTPStatusCode())
	assert.Equal(t, "kokoro", gatewayErr.Provider)
}

func TestNewAudioVoicesUnsupportedError(t *testing.T) {
	err := NewAudioVoicesUnsupportedError("gpt-4o-mini-tts")
	assert.Equal(t, ErrorTypeInvalidRequest, err.Type)
	assert.Equal(t, http.StatusBadRequest, err.HTTPStatusCode())
	assert.Equal(t, `model "gpt-4o-mini-tts" does not support listing voices`, err.Message)
	require.NotNil(t, err.Param)
	assert.Equal(t, "model", *err.Param)
}
