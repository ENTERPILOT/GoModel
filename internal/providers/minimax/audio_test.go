package minimax

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

func TestCreateSpeech_UsesNativeEndpointAndDecodesHex(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"data":{"audio":"000102ff","status":2},"base_resp":{"status_code":0,"status_msg":"success"}}`)

	provider := NewWithHTTPClient("minimax-key", server.URL+"/v1", server.Client(), llmclient.Hooks{})
	resp, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model:          "speech-2.8-hd",
		Input:          "hello",
		Voice:          "English_expressive_narrator",
		ResponseFormat: "wav",
		Speed:          1.5,
	})
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, "/v1/t2a_v2", req.Path)
	assert.Equal(t, "Bearer minimax-key", req.Header.Get("Authorization"))

	var gotRequest speechRequest
	require.NoError(t, json.Unmarshal(req.Body, &gotRequest))
	assert.Equal(t, "speech-2.8-hd", gotRequest.Model)
	assert.Equal(t, "hello", gotRequest.Text)
	assert.False(t, gotRequest.Stream)
	assert.Equal(t, "hex", gotRequest.OutputFormat)
	assert.Equal(t, "English_expressive_narrator", gotRequest.VoiceSetting.VoiceID)
	assert.Equal(t, 1.5, gotRequest.VoiceSetting.Speed)
	assert.Equal(t, "wav", gotRequest.AudioSetting.Format)
	assert.Equal(t, "audio/wav", resp.ContentType)
	assert.Equal(t, []byte{0x00, 0x01, 0x02, 0xff}, resp.Data)
}

func TestCreateSpeech_DefaultsToMP3AndNormalSpeed(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"data":{"audio":"ff","status":2},"base_resp":{"status_code":0}}`)

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model: "speech-2.8-hd",
		Input: "hello",
		Voice: "voice-id",
	})
	require.NoError(t, err)

	var gotRequest speechRequest
	require.NoError(t, json.Unmarshal(capture.Last(t).Body, &gotRequest))
	assert.Equal(t, "mp3", gotRequest.AudioSetting.Format)
	assert.Equal(t, float64(1), gotRequest.VoiceSetting.Speed)
	assert.Equal(t, "audio/mpeg", resp.ContentType)
}

func TestCreateSpeech_ValidatesNativeConstraints(t *testing.T) {
	provider := NewWithHTTPClient("key", "https://example.invalid/v1", nil, llmclient.Hooks{})
	tests := []struct {
		name string
		req  *core.AudioSpeechRequest
		want string
	}{
		{name: "nil request", req: nil, want: "request is required"},
		{name: "missing model", req: &core.AudioSpeechRequest{Input: "hello", Voice: "voice"}, want: "model is required"},
		{name: "missing input", req: &core.AudioSpeechRequest{Model: "speech-2.8-hd", Voice: "voice"}, want: "input is required"},
		{name: "missing voice", req: &core.AudioSpeechRequest{Model: "speech-2.8-hd", Input: "hello"}, want: "voice is required"},
		{name: "instructions", req: &core.AudioSpeechRequest{Model: "speech-2.8-hd", Input: "hello", Voice: "voice", Instructions: "whisper"}, want: "does not support instructions"},
		{name: "format", req: &core.AudioSpeechRequest{Model: "speech-2.8-hd", Input: "hello", Voice: "voice", ResponseFormat: "aac"}, want: "supports mp3, wav, flac, or pcm"},
		{name: "speed", req: &core.AudioSpeechRequest{Model: "speech-2.8-hd", Input: "hello", Voice: "voice", Speed: 0.25}, want: "between 0.5 and 2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := provider.CreateSpeech(context.Background(), tt.req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestCreateSpeech_MapsNativeStatusCodes(t *testing.T) {
	tests := []struct {
		name           string
		nativeStatus   int
		statusMsg      string
		wantHTTPStatus int
		wantType       core.ErrorType
	}{
		{name: "rate limit", nativeStatus: 1002, statusMsg: "rate limit triggered", wantHTTPStatus: http.StatusTooManyRequests, wantType: core.ErrorTypeRateLimit},
		{name: "tpm limit", nativeStatus: 1039, statusMsg: "token limit", wantHTTPStatus: http.StatusTooManyRequests, wantType: core.ErrorTypeRateLimit},
		{name: "rate growth limit", nativeStatus: 2045, statusMsg: "rate growth limit", wantHTTPStatus: http.StatusTooManyRequests, wantType: core.ErrorTypeRateLimit},
		{name: "usage limit", nativeStatus: 2056, statusMsg: "usage limit exceeded", wantHTTPStatus: http.StatusTooManyRequests, wantType: core.ErrorTypeRateLimit},
		{name: "auth failed", nativeStatus: 1004, statusMsg: "not authorized", wantHTTPStatus: http.StatusUnauthorized, wantType: core.ErrorTypeAuthentication},
		{name: "invalid api key", nativeStatus: 2049, statusMsg: "invalid API Key", wantHTTPStatus: http.StatusUnauthorized, wantType: core.ErrorTypeAuthentication},
		{name: "insufficient balance", nativeStatus: 1008, statusMsg: "insufficient balance", wantHTTPStatus: http.StatusPaymentRequired, wantType: core.ErrorTypeProvider},
		{name: "sensitive input", nativeStatus: 1026, statusMsg: "sensitive content", wantHTTPStatus: http.StatusBadRequest, wantType: core.ErrorTypeInvalidRequest},
		{name: "invisible characters", nativeStatus: 1042, statusMsg: "invisible character ratio limit", wantHTTPStatus: http.StatusBadRequest, wantType: core.ErrorTypeInvalidRequest},
		{name: "invalid params", nativeStatus: 2013, statusMsg: "invalid params", wantHTTPStatus: http.StatusBadRequest, wantType: core.ErrorTypeInvalidRequest},
		{name: "invalid voice", nativeStatus: 20132, statusMsg: "invalid samples or voice_id", wantHTTPStatus: http.StatusBadRequest, wantType: core.ErrorTypeInvalidRequest},
		{name: "unknown code", nativeStatus: 1000, statusMsg: "unknown error", wantHTTPStatus: http.StatusBadGateway, wantType: core.ErrorTypeProvider},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{
				"data":      nil,
				"base_resp": map[string]any{"status_code": tt.nativeStatus, "status_msg": tt.statusMsg},
			})
			require.NoError(t, err)
			server, _ := providertest.JSONServer(t, http.StatusOK, string(body))

			provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
			_, err = provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
				Model: "speech-2.8-hd",
				Input: "hello",
				Voice: "voice-id",
			})
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, tt.wantHTTPStatus, gatewayErr.StatusCode)
			assert.Equal(t, tt.wantType, gatewayErr.Type)
			assert.Contains(t, gatewayErr.Message, tt.statusMsg)
			assert.Contains(t, gatewayErr.Message, strconv.Itoa(tt.nativeStatus))
			assert.Equal(t, "minimax", gatewayErr.Provider)
		})
	}
}

func TestCreateSpeech_RejectsMalformedAudio(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"data":{"audio":"not-hex","status":2},"base_resp":{"status_code":0}}`)

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	_, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model: "speech-2.8-hd",
		Input: "hello",
		Voice: "voice-id",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid hexadecimal")
}

func TestCreateTranscription_IsUnsupported(t *testing.T) {
	provider := NewWithHTTPClient("key", "", nil, llmclient.Hooks{})
	_, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support speech-to-text")
}
