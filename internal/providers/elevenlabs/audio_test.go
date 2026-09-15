package elevenlabs

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// audioServer answers every request with a one-byte audio payload.
func audioServer(t *testing.T, contentType string) (string, *http.Client, *providertest.Capture) {
	t.Helper()
	server, capture := providertest.Server(t, func(w http.ResponseWriter, _ *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		_, _ = w.Write([]byte{0x49, 0x44, 0x33})
	})
	return server.URL, server.Client(), capture
}

// multipartFields decodes the recorded multipart body into its form fields,
// returning the file part's filename separately.
func multipartFields(t *testing.T, req providertest.Recorded) (fields map[string]string, filename string) {
	t.Helper()
	_, params, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	require.NoError(t, err)

	fields = map[string]string{}
	reader := multipart.NewReader(bytes.NewReader(req.Body), params["boundary"])
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return fields, filename
		}
		require.NoError(t, err)
		data, err := io.ReadAll(part)
		require.NoError(t, err)
		fields[part.FormName()] = string(data)
		if part.FormName() == "file" {
			filename = part.FileName()
		}
	}
}

func TestCreateSpeech_UsesVoiceIDInPathAndDefaultsToMP3(t *testing.T) {
	url, client, capture := audioServer(t, "audio/mpeg")

	provider := NewWithHTTPClient("elk_test", url, client, llmclient.Hooks{})
	resp, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model: "eleven_multilingual_v2",
		Input: "hello there",
		Voice: "21m00Tcm4TlvDq8ikWAM",
	})
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, "/v1/text-to-speech/21m00Tcm4TlvDq8ikWAM", req.Path)
	assert.Equal(t, "output_format=mp3_44100_128", req.Query.Encode())
	assert.Equal(t, "elk_test", req.Header.Get("xi-api-key"))

	var gotBody speechRequest
	require.NoError(t, json.Unmarshal(req.Body, &gotBody))
	assert.Equal(t, "hello there", gotBody.Text)
	assert.Equal(t, "eleven_multilingual_v2", gotBody.ModelID)
	assert.Nil(t, gotBody.VoiceSetting)
	assert.Equal(t, "audio/mpeg", resp.ContentType)
	assert.Equal(t, []byte{0x49, 0x44, 0x33}, resp.Data)
}

func TestCreateSpeech_MapsResponseFormats(t *testing.T) {
	tests := []struct {
		name           string
		responseFormat string
		wantQuery      string
		wantContent    string
	}{
		{"default mp3", "", "output_format=mp3_44100_128", "audio/mpeg"},
		{"opus", "opus", "output_format=opus_48000_128", "audio/ogg"},
		{"pcm", "pcm", "output_format=pcm_24000", "audio/pcm"},
		{"wav", "wav", "output_format=wav_44100", "audio/wav"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, client, capture := audioServer(t, "")

			provider := NewWithHTTPClient("key", url, client, llmclient.Hooks{})
			resp, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
				Model: "eleven_multilingual_v2", Input: "hi", Voice: "voice-id",
				ResponseFormat: tt.responseFormat,
			})
			require.NoError(t, err)
			assert.Equal(t, tt.wantQuery, capture.Last(t).Query.Encode())
			assert.Equal(t, tt.wantContent, resp.ContentType)
		})
	}
}

func TestCreateSpeech_ClampsSpeedToSupportedRange(t *testing.T) {
	tests := []struct {
		name      string
		speed     float64
		wantSpeed float64
	}{
		{"within range", 1.1, 1.1},
		{"too slow clamps to minimum", 0.3, 0.7},
		{"too fast clamps to maximum", 3.0, 1.2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url, client, capture := audioServer(t, "")

			provider := NewWithHTTPClient("key", url, client, llmclient.Hooks{})
			_, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
				Model: "eleven_multilingual_v2", Input: "hi", Voice: "voice-id", Speed: tt.speed,
			})
			require.NoError(t, err)

			var gotBody speechRequest
			require.NoError(t, json.Unmarshal(capture.Last(t).Body, &gotBody))
			require.NotNil(t, gotBody.VoiceSetting)
			assert.Equal(t, tt.wantSpeed, gotBody.VoiceSetting.Speed)
		})
	}
}

func TestCreateSpeech_ValidatesRequest(t *testing.T) {
	provider := NewWithHTTPClient("key", "https://example.invalid", nil, llmclient.Hooks{})
	tests := []struct {
		name string
		req  *core.AudioSpeechRequest
		want string
	}{
		{name: "nil request", req: nil, want: "request is required"},
		{name: "missing model", req: &core.AudioSpeechRequest{Input: "hi", Voice: "v"}, want: "model is required"},
		{name: "missing input", req: &core.AudioSpeechRequest{Model: "m", Voice: "v"}, want: "input is required"},
		{name: "missing voice", req: &core.AudioSpeechRequest{Model: "m", Input: "hi"}, want: "voice is required"},
		{name: "instructions", req: &core.AudioSpeechRequest{Model: "m", Input: "hi", Voice: "v", Instructions: "whisper"}, want: "does not support instructions"},
		{name: "format", req: &core.AudioSpeechRequest{Model: "m", Input: "hi", Voice: "v", ResponseFormat: "aac"}, want: "supports mp3, opus, pcm, or wav"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := provider.CreateSpeech(context.Background(), tt.req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestCreateSpeech_ReturnsUpstreamError(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusUnauthorized, `{"detail":{"status":"invalid_api_key","message":"bad key"}}`)

	provider := NewWithHTTPClient("bad-key", server.URL, server.Client(), llmclient.Hooks{})
	_, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model: "m", Input: "hi", Voice: "v",
	})
	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusUnauthorized, gatewayErr.StatusCode)
	assert.Equal(t, core.ErrorTypeAuthentication, gatewayErr.Type)
	assert.Equal(t, "bad key", gatewayErr.Message)
}

func TestRefineElevenLabsError_UnwrapsDetailShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"string detail", `{"detail":"Not Found"}`, "Not Found"},
		{"object detail with message", `{"detail":{"type":"authorization_error","code":"subscription_required","message":"Output format 'wav_44100' is only available on the Pro tier and above.","status":"output_format_not_allowed"}}`, "Output format 'wav_44100' is only available on the Pro tier and above."},
		{"no detail field keeps the generic-parser message", `{"error":"something else"}`, "something else"},
		{"not JSON falls back to raw body", `plain text error`, "plain text error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := core.ParseProviderError("elevenlabs", http.StatusBadRequest, []byte(tt.body), nil)
			refined := refineElevenLabsError(original)
			var gatewayErr *core.GatewayError
			require.ErrorAs(t, refined, &gatewayErr)
			assert.Equal(t, tt.want, gatewayErr.Message)
		})
	}
}

func TestCreateTranscription_SendsMultipartAndReturnsJSON(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"language_code":"en","text":"hello world","words":[{"text":"hello","type":"word","start":0,"end":0.5},{"text":" ","type":"spacing","start":0.5,"end":0.6},{"text":"world","type":"word","start":0.6,"end":1.1}]}`)

	provider := NewWithHTTPClient("elk_test", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:    "scribe_v1",
		Filename: "clip.mp3",
		File:     []byte("fake-audio-bytes"),
		Language: "en",
	})
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, "/v1/speech-to-text", req.Path)
	assert.Equal(t, "elk_test", req.Header.Get("xi-api-key"))

	fields, filename := multipartFields(t, req)
	assert.Equal(t, "scribe_v1", fields["model_id"])
	assert.Equal(t, "en", fields["language_code"])
	assert.Equal(t, "none", fields["timestamps_granularity"])
	assert.Equal(t, "clip.mp3", filename)
	assert.Equal(t, "fake-audio-bytes", fields["file"])
	assert.Equal(t, "application/json", resp.ContentType)

	var decoded struct {
		Text string `json:"text"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &decoded))
	assert.Equal(t, "hello world", decoded.Text)
}

func TestCreateTranscription_WordGranularityFromRequest(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"text":"hi"}`)

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	_, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:                  "scribe_v1",
		File:                   []byte("audio"),
		TimestampGranularities: []string{"word"},
	})
	require.NoError(t, err)

	fields, _ := multipartFields(t, capture.Last(t))
	assert.Equal(t, "word", fields["timestamps_granularity"])
}

func TestCreateTranscription_VerboseJSONIncludesWordsAndDuration(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"language_code":"en","text":"hi there","words":[{"text":"hi","type":"word","start":0,"end":0.3},{"text":"there","type":"word","start":0.4,"end":0.9}]}`)

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:          "scribe_v1",
		File:           []byte("audio"),
		ResponseFormat: "verbose_json",
	})
	require.NoError(t, err)

	var decoded struct {
		Language string  `json:"language"`
		Duration float64 `json:"duration"`
		Text     string  `json:"text"`
		Words    []struct {
			Word  string  `json:"word"`
			Start float64 `json:"start"`
			End   float64 `json:"end"`
		} `json:"words"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &decoded))
	assert.Equal(t, "en", decoded.Language)
	assert.Equal(t, "hi there", decoded.Text)
	assert.Equal(t, 0.9, decoded.Duration)
	require.Len(t, decoded.Words, 2)
	assert.Equal(t, "there", decoded.Words[1].Word)
}

func TestCreateTranscription_TextFormatReturnsPlainText(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{"text":"plain text result"}`)

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:          "scribe_v1",
		File:           []byte("audio"),
		ResponseFormat: "text",
	})
	require.NoError(t, err)
	assert.Equal(t, "plain text result", string(resp.Data))
	assert.True(t, strings.HasPrefix(resp.ContentType, "text/plain"), "content type = %q, want text/plain", resp.ContentType)
}

func TestCreateTranscription_ValidatesRequest(t *testing.T) {
	provider := NewWithHTTPClient("key", "https://example.invalid", nil, llmclient.Hooks{})
	tests := []struct {
		name string
		req  *core.AudioTranscriptionRequest
		want string
	}{
		{name: "nil request", req: nil, want: "request is required"},
		{name: "missing model", req: &core.AudioTranscriptionRequest{File: []byte("a")}, want: "model is required"},
		{name: "bad format", req: &core.AudioTranscriptionRequest{Model: "scribe_v1", File: []byte("a"), ResponseFormat: "srt"}, want: "supports json, text, or verbose_json"},
		{name: "prompt", req: &core.AudioTranscriptionRequest{Model: "scribe_v1", File: []byte("a"), Prompt: "context"}, want: "does not support prompt"},
		{name: "missing file", req: &core.AudioTranscriptionRequest{Model: "scribe_v1"}, want: "file is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := provider.CreateTranscription(context.Background(), tt.req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}
