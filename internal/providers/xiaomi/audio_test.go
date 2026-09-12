package xiaomi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTTSServer(t *testing.T, audioBase64 string) (*httptest.Server, *providertest.Capture) {
	t.Helper()
	return providertest.JSONServer(t, http.StatusOK, `{
		"id":"chatcmpl-tts","created":1677652288,"model":"mimo-v2.5-tts",
		"choices":[{"index":0,"message":{"role":"assistant","content":"","audio":{"id":"a1","data":"`+audioBase64+`","format":"wav"}},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":10,"completion_tokens":50,"total_tokens":60}
	}`)
}

func newASRServer(t *testing.T, text string) (*httptest.Server, *providertest.Capture) {
	t.Helper()
	return providertest.JSONServer(t, http.StatusOK, `{
		"id":"chatcmpl-asr","created":1677652288,"model":"mimo-v2.5-asr",
		"choices":[{"index":0,"message":{"role":"assistant","content":"`+text+`"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}
	}`)
}

func TestCreateSpeech_TranslatesToMiMoChatTTS(t *testing.T) {
	wavBytes := []byte("RIFF-fake-wav")
	server, capture := newTTSServer(t, base64.StdEncoding.EncodeToString(wavBytes))
	provider := NewWithHTTPClient("mimo-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model:        "mimo-v2.5-tts",
		Input:        "Hello world",
		Voice:        "Chloe",
		Instructions: "Bright bouncy tone",
	})
	require.NoError(t, err)
	assert.Equal(t, "audio/wav", resp.ContentType)
	assert.Equal(t, string(wavBytes), string(resp.Data))

	var sent struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Audio map[string]string `json:"audio"`
	}
	require.NoError(t, json.Unmarshal(capture.Last(t).Body, &sent))
	assert.Equal(t, "mimo-v2.5-tts", sent.Model)
	require.Len(t, sent.Messages, 2)
	assert.Equal(t, "user", sent.Messages[0].Role)
	assert.Equal(t, "assistant", sent.Messages[1].Role)
	assert.Equal(t, "Hello world", sent.Messages[1].Content)
	assert.Equal(t, "wav", sent.Audio["format"])
	assert.Equal(t, "Chloe", sent.Audio["voice"])
}

func TestCreateSpeech_MapsPCMAndRejectsUnsupportedFormats(t *testing.T) {
	server, capture := newTTSServer(t, base64.StdEncoding.EncodeToString([]byte("pcm")))
	provider := NewWithHTTPClient("mimo-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model: "mimo-v2.5-tts", Input: "hi", ResponseFormat: "pcm",
	})
	require.NoError(t, err)
	assert.Equal(t, "audio/pcm", resp.ContentType)
	assert.Contains(t, string(capture.Last(t).Body), `"format":"pcm16"`)

	for _, format := range []string{"opus", "aac", "flac"} {
		_, err = provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
			Model: "mimo-v2.5-tts", Input: "hi", ResponseFormat: format,
		})
		assert.Error(t, err, "format %q", format)
	}
}

// mp3 is OpenAI's documented default, so a client echoing it back is asking for
// "unspecified" rather than for a codec MiMo cannot synthesize: it is answered
// with wav, exactly like an omitted response_format.
func TestCreateSpeech_TreatsMP3AsUnspecified(t *testing.T) {
	for _, format := range []string{"", "mp3", "MP3", " mp3 ", "wav"} {
		t.Run(format, func(t *testing.T) {
			server, capture := newTTSServer(t, base64.StdEncoding.EncodeToString([]byte("wav")))
			provider := NewWithHTTPClient("mimo-key", server.URL, server.Client(), llmclient.Hooks{})

			resp, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
				Model: "mimo-v2.5-tts", Input: "hi", ResponseFormat: format,
			})
			require.NoError(t, err)
			assert.Equal(t, "audio/wav", resp.ContentType)
			assert.Contains(t, string(capture.Last(t).Body), `"format":"wav"`)
		})
	}
}

func TestCreateSpeech_RequiresInput(t *testing.T) {
	provider := NewWithHTTPClient("mimo-key", "", nil, llmclient.Hooks{})
	_, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{Model: "mimo-v2.5-tts"})
	require.Error(t, err)
}

func TestCreateSpeech_RejectsSpeedControl(t *testing.T) {
	provider := NewWithHTTPClient("mimo-key", "", nil, llmclient.Hooks{})
	_, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model: "mimo-v2.5-tts", Input: "hi", Speed: 1.5,
	})
	require.Error(t, err)

	server, _ := newTTSServer(t, base64.StdEncoding.EncodeToString([]byte("wav")))
	provider = NewWithHTTPClient("mimo-key", server.URL, server.Client(), llmclient.Hooks{})
	_, err = provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model: "mimo-v2.5-tts", Input: "hi", Speed: 1,
	})
	require.NoError(t, err)
}

func TestCreateTranscription_TranslatesToMiMoChatASR(t *testing.T) {
	server, capture := newASRServer(t, "hello there")
	provider := NewWithHTTPClient("mimo-key", server.URL, server.Client(), llmclient.Hooks{})

	audio := []byte("RIFF-fake-wav")
	resp, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:       "mimo-v2.5-asr",
		Filename:    "clip.wav",
		File:        audio,
		Language:    "auto",
		Temperature: "0.2",
	})
	require.NoError(t, err)
	assert.Equal(t, "application/json", resp.ContentType)

	var out struct {
		Text string `json:"text"`
	}
	require.NoError(t, json.Unmarshal(resp.Data, &out))
	assert.Equal(t, "hello there", out.Text)

	wantDataURI := "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(audio)
	body := string(capture.Last(t).Body)
	assert.Contains(t, body, `"type":"input_audio"`)
	assert.Contains(t, body, wantDataURI)
	assert.NotContains(t, body, `"format"`)
	assert.Contains(t, body, `"asr_options":{"language":"auto"}`)
	assert.Contains(t, body, `"temperature":0.2`)
}

func TestCreateTranscription_TextFormatAndValidation(t *testing.T) {
	server, _ := newASRServer(t, "plain text")
	provider := NewWithHTTPClient("mimo-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model: "mimo-v2.5-asr", Filename: "clip.wav", File: []byte("audio"), ResponseFormat: "text",
	})
	require.NoError(t, err)
	assert.Equal(t, "plain text", string(resp.Data))
	assert.True(t, strings.HasPrefix(resp.ContentType, "text/plain"), "ContentType = %q, want text/plain", resp.ContentType)

	for _, unsupported := range []core.AudioTranscriptionRequest{
		{Model: "mimo-v2.5-asr", File: []byte("audio"), ResponseFormat: "srt"},
		{Model: "mimo-v2.5-asr", File: []byte("audio"), ResponseFormat: "verbose_json"},
		{Model: "mimo-v2.5-asr", File: []byte("audio"), Prompt: "domain hint"},
		{Model: "mimo-v2.5-asr", File: []byte("audio"), TimestampGranularities: []string{"word"}},
		{Model: "mimo-v2.5-asr", File: []byte("audio"), Temperature: "not-a-number"},
		{Model: "mimo-v2.5-asr"},
	} {
		req := unsupported
		_, err := provider.CreateTranscription(context.Background(), &req)
		assert.Error(t, err, "request %+v", req)
	}
}

func TestCreateTranscription_FileReaderAndMIMEInference(t *testing.T) {
	cases := []struct {
		name            string
		filename        string
		fileContentType string
		useReader       bool
		wantDataPrefix  string
	}{
		{name: "reader ingestion with content type", fileContentType: "audio/ogg", useReader: true, wantDataPrefix: "data:audio/ogg;base64,"},
		{name: "content type wins over extension", filename: "clip.wav", fileContentType: "audio/mpeg", wantDataPrefix: "data:audio/mpeg;base64,"},
		{name: "extension fallback mp3", filename: "clip.mp3", wantDataPrefix: "data:audio/mpeg;base64,"},
		{name: "extension fallback flac", filename: "clip.flac", wantDataPrefix: "data:audio/flac;base64,"},
		{name: "default to wav", filename: "clip.unknown", wantDataPrefix: "data:audio/wav;base64,"},
		{name: "non-audio content type ignored", filename: "clip.m4a", fileContentType: "application/octet-stream", wantDataPrefix: "data:audio/mp4;base64,"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, capture := newASRServer(t, "ok")
			provider := NewWithHTTPClient("mimo-key", server.URL, server.Client(), llmclient.Hooks{})

			audio := []byte("audio-bytes-" + tc.name)
			req := &core.AudioTranscriptionRequest{
				Model:           "mimo-v2.5-asr",
				Filename:        tc.filename,
				FileContentType: tc.fileContentType,
			}
			if tc.useReader {
				req.FileReader = strings.NewReader(string(audio))
			} else {
				req.File = audio
			}
			_, err := provider.CreateTranscription(context.Background(), req)
			require.NoError(t, err)

			wantData := tc.wantDataPrefix + base64.StdEncoding.EncodeToString(audio)
			assert.Contains(t, string(capture.Last(t).Body), wantData)
		})
	}
}
