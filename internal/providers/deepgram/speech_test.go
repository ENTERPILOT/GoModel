package deepgram

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// audioServer answers every request with a three-byte audio payload.
func audioServer(t *testing.T) (*Provider, *providertest.Capture) {
	t.Helper()
	server, capture := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/l16;rate=24000")
		_, _ = w.Write([]byte{0x49, 0x44, 0x33})
	})
	return newTestProvider("dg_test", server.URL, server.Client()), capture
}

func TestCreateSpeech_SendsTextAndModelQuery(t *testing.T) {
	provider, capture := audioServer(t)

	resp, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model: "aura-2-thalia-en",
		Input: "hello there",
	})
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, http.MethodPost, req.Method)
	assert.Equal(t, "/speak", req.Path)
	assert.Equal(t, "encoding=mp3&model=aura-2-thalia-en", req.Query.Encode())
	assert.Equal(t, "Token dg_test", req.Header.Get("Authorization"))
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	assert.JSONEq(t, `{"text":"hello there"}`, string(req.Body))

	assert.Equal(t, "audio/mpeg", resp.ContentType)
	require.NotNil(t, resp.Stream, "synthesized audio is relayed, not buffered")
	assert.Equal(t, []byte{0x49, 0x44, 0x33}, providertest.AudioBytes(t, resp))
}

func TestCreateSpeech_MapsResponseFormats(t *testing.T) {
	tests := []struct {
		responseFormat string
		wantQuery      string
		wantContent    string
	}{
		{"", "encoding=mp3", "audio/mpeg"},
		{"MP3", "encoding=mp3", "audio/mpeg"},
		{"opus", "encoding=opus", "audio/ogg"},
		{"aac", "encoding=aac", "audio/aac"},
		{"flac", "encoding=flac", "audio/flac"},
		{"wav", "container=wav&encoding=linear16", "audio/wav"},
		{"pcm", "container=none&encoding=linear16&sample_rate=24000", "audio/pcm"},
	}
	for _, tt := range tests {
		t.Run(tt.responseFormat, func(t *testing.T) {
			provider, capture := audioServer(t)

			resp, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
				Model: "aura-2-thalia-en", Input: "hi", ResponseFormat: tt.responseFormat,
			})
			require.NoError(t, err)
			defer resp.Stream.Close()
			want, err := url.ParseQuery(tt.wantQuery + "&model=aura-2-thalia-en")
			require.NoError(t, err)
			assert.Equal(t, want, capture.Last(t).Query)
			assert.Equal(t, tt.wantContent, resp.ContentType)
		})
	}
}

func TestCreateSpeech_FitsSpeedToSupportedRange(t *testing.T) {
	tests := []struct {
		name      string
		speed     float64
		wantSpeed string
	}{
		{"unset", 0, ""},
		{"default is not sent", 1, ""},
		{"within range", 1.25, "1.25"},
		{"rounds to a 0.05 step", 0.83, "0.85"},
		{"too slow clamps to minimum", 0.25, "0.7"},
		{"too fast clamps to maximum", 4, "1.5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, capture := audioServer(t)

			resp, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
				Model: "aura-2-thalia-en", Input: "hi", Speed: tt.speed,
			})
			require.NoError(t, err)
			defer resp.Stream.Close()
			assert.Equal(t, tt.wantSpeed, capture.Last(t).Query.Get("speed"))
		})
	}
}

func TestSpeechModel_CombinesModelAndVoice(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		voice   string
		want    string
		wantErr string
	}{
		{name: "full id without voice", model: "aura-2-thalia-en", want: "aura-2-thalia-en"},
		{name: "family with short voice", model: "aura-2", voice: "thalia-en", want: "aura-2-thalia-en"},
		{name: "first-generation family", model: "aura", voice: "asteria-en", want: "aura-asteria-en"},
		{name: "family with full id as voice", model: "aura-2", voice: "aura-2-thalia-en", want: "aura-2-thalia-en"},
		{name: "voice repeats the model id", model: "aura-2-thalia-en", voice: "aura-2-thalia-en", want: "aura-2-thalia-en"},
		{name: "voice repeats the model's voice", model: "aura-2-thalia-en", voice: "thalia-en", want: "aura-2-thalia-en"},
		{name: "voice name without language", model: "aura-2-thalia-en", voice: "Thalia", want: "aura-2-thalia-en"},
		{name: "case and padding", model: " Aura-2 ", voice: " Thalia-EN ", want: "aura-2-thalia-en"},
		{name: "ids outside the aura scheme pass through", model: "custom-voice", want: "custom-voice"},
		{name: "id outside the aura scheme repeated as voice", model: "custom-voice", voice: "custom-voice", want: "custom-voice"},

		{name: "family without voice", model: "aura-2", wantErr: "voice is required"},
		{name: "another voice than the model's", model: "aura-2-thalia-en", voice: "luna-en", wantErr: "does not match"},
		{name: "an OpenAI voice", model: "aura-2-thalia-en", voice: "alloy", wantErr: "does not match"},
		{name: "partial voice name", model: "aura-2-thalia-en", voice: "tha", wantErr: "does not match"},
		{name: "full id of another family", model: "aura", voice: "aura-2-thalia-en", wantErr: "is not a voice of"},
		{name: "family repeated as voice", model: "aura-2", voice: "aura-2", wantErr: "is not a voice of"},
		{name: "another family as voice", model: "aura-2", voice: "aura", wantErr: "is not a voice of"},
		{name: "voice reaching into another family", model: "aura", voice: "2-thalia-en", wantErr: "is not a voice of"},
		{name: "voice on an id outside the aura scheme", model: "custom-voice", voice: "thalia-en", wantErr: "does not match"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := speechModel(tt.model, tt.voice)
			if tt.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
				return
			}
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, http.StatusBadRequest, gatewayErr.StatusCode)
			assert.Contains(t, gatewayErr.Message, tt.wantErr)
		})
	}
}

// An OpenAI custom-voice object names the Deepgram voice by its id.
func TestCreateSpeech_UsesVoiceWithFamilyModel(t *testing.T) {
	provider, capture := audioServer(t)

	req, err := core.DecodeAudioSpeechRequest([]byte(
		`{"model":"aura-2","input":"hello","voice":{"id":"thalia-en"}}`), nil)
	require.NoError(t, err)
	resp, err := provider.CreateSpeech(context.Background(), req)
	require.NoError(t, err)
	defer resp.Stream.Close()

	recorded := capture.Last(t)
	assert.Equal(t, "aura-2-thalia-en", recorded.Query.Get("model"))
	assert.JSONEq(t, `{"text":"hello"}`, string(recorded.Body))
}

func TestCreateSpeech_ValidatesRequest(t *testing.T) {
	provider := newTestProvider("key", "https://example.invalid", nil)
	tests := []struct {
		name string
		req  *core.AudioSpeechRequest
		want string
	}{
		{name: "nil request", req: nil, want: "request is required"},
		{name: "missing model", req: &core.AudioSpeechRequest{Input: "hi"}, want: "model is required"},
		{name: "missing input", req: &core.AudioSpeechRequest{Model: "aura-2-thalia-en"}, want: "input is required"},
		{name: "instructions", req: &core.AudioSpeechRequest{Model: "aura-2-thalia-en", Input: "hi", Instructions: "whisper"}, want: "does not support instructions"},
		{name: "format", req: &core.AudioSpeechRequest{Model: "aura-2-thalia-en", Input: "hi", ResponseFormat: "ogg"}, want: "supports mp3, opus, aac, flac, wav, or pcm"},
		{name: "voice", req: &core.AudioSpeechRequest{Model: "aura-2-thalia-en", Input: "hi", Voice: "alloy"}, want: "does not match"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := provider.CreateSpeech(context.Background(), tt.req)
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, http.StatusBadRequest, gatewayErr.StatusCode)
			assert.Equal(t, core.ErrorTypeInvalidRequest, gatewayErr.Type)
			assert.Contains(t, gatewayErr.Message, tt.want)
		})
	}
}

func TestCreateSpeech_ReturnsUpstreamError(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusUnauthorized,
		`{"err_code":"INVALID_AUTH","err_msg":"Invalid credentials.","request_id":"01a1f360"}`)

	provider := newTestProvider("bad-key", server.URL, server.Client())
	_, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model: "aura-2-thalia-en", Input: "hi",
	})
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusUnauthorized, gatewayErr.StatusCode)
	assert.Equal(t, core.ErrorTypeAuthentication, gatewayErr.Type)
	assert.Equal(t, "Invalid credentials.", gatewayErr.Message)
}
