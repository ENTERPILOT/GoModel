package xiaomi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

func newTTSServer(t *testing.T, audioBase64 string) (*httptest.Server, *[]byte) {
	t.Helper()
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-tts","created":1677652288,"model":"mimo-v2.5-tts",
			"choices":[{"index":0,"message":{"role":"assistant","content":"","audio":{"id":"a1","data":"` + audioBase64 + `","format":"wav"}},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":50,"total_tokens":60}
		}`))
	}))
	return server, &gotBody
}

func TestCreateSpeech_TranslatesToMiMoChatTTS(t *testing.T) {
	wavBytes := []byte("RIFF-fake-wav")
	server, gotBody := newTTSServer(t, base64.StdEncoding.EncodeToString(wavBytes))
	defer server.Close()

	provider := NewWithHTTPClient("mimo-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model:        "mimo-v2.5-tts",
		Input:        "Hello world",
		Voice:        "Chloe",
		Instructions: "Bright bouncy tone",
	})
	if err != nil {
		t.Fatalf("CreateSpeech() error = %v", err)
	}
	if resp.ContentType != "audio/wav" {
		t.Fatalf("ContentType = %q, want audio/wav", resp.ContentType)
	}
	if string(resp.Data) != string(wavBytes) {
		t.Fatalf("Data = %q, want decoded wav bytes", resp.Data)
	}

	var sent struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Audio map[string]string `json:"audio"`
	}
	if err := json.Unmarshal(*gotBody, &sent); err != nil {
		t.Fatalf("failed to decode upstream body: %v", err)
	}
	if sent.Model != "mimo-v2.5-tts" {
		t.Fatalf("model = %q, want mimo-v2.5-tts", sent.Model)
	}
	if len(sent.Messages) != 2 || sent.Messages[0].Role != "user" || sent.Messages[1].Role != "assistant" {
		t.Fatalf("messages = %+v, want user instructions + assistant text", sent.Messages)
	}
	if sent.Messages[1].Content != "Hello world" {
		t.Fatalf("assistant content = %q, want synthesis text", sent.Messages[1].Content)
	}
	if sent.Audio["format"] != "wav" || sent.Audio["voice"] != "Chloe" {
		t.Fatalf("audio = %+v, want format=wav voice=Chloe", sent.Audio)
	}
}

func TestCreateSpeech_MapsPCMAndRejectsUnsupportedFormats(t *testing.T) {
	server, gotBody := newTTSServer(t, base64.StdEncoding.EncodeToString([]byte("pcm")))
	defer server.Close()

	provider := NewWithHTTPClient("mimo-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model: "mimo-v2.5-tts", Input: "hi", ResponseFormat: "pcm",
	})
	if err != nil {
		t.Fatalf("CreateSpeech(pcm) error = %v", err)
	}
	if resp.ContentType != "audio/pcm" {
		t.Fatalf("ContentType = %q, want audio/pcm", resp.ContentType)
	}
	if !strings.Contains(string(*gotBody), `"format":"pcm16"`) {
		t.Fatalf("upstream body should request pcm16, got: %s", *gotBody)
	}

	_, err = provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model: "mimo-v2.5-tts", Input: "hi", ResponseFormat: "mp3",
	})
	if err == nil {
		t.Fatal("CreateSpeech(mp3) succeeded, want unsupported-format error")
	}
}

func TestCreateSpeech_RequiresInput(t *testing.T) {
	provider := NewWithHTTPClient("mimo-key", "", nil, llmclient.Hooks{})
	_, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{Model: "mimo-v2.5-tts"})
	if err == nil {
		t.Fatal("CreateSpeech() succeeded, want input-required error")
	}
}

func TestCreateSpeech_RejectsSpeedControl(t *testing.T) {
	provider := NewWithHTTPClient("mimo-key", "", nil, llmclient.Hooks{})
	_, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model: "mimo-v2.5-tts", Input: "hi", Speed: 1.5,
	})
	if err == nil {
		t.Fatal("CreateSpeech(speed=1.5) succeeded, want unsupported-speed error")
	}

	server, _ := newTTSServer(t, base64.StdEncoding.EncodeToString([]byte("wav")))
	defer server.Close()
	provider = NewWithHTTPClient("mimo-key", server.URL, server.Client(), llmclient.Hooks{})
	if _, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model: "mimo-v2.5-tts", Input: "hi", Speed: 1,
	}); err != nil {
		t.Fatalf("CreateSpeech(speed=1) error = %v, want default speed accepted", err)
	}
}

func TestCreateTranscription_TranslatesToMiMoChatASR(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-asr","created":1677652288,"model":"mimo-v2.5-asr",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello there"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("mimo-key", server.URL, server.Client(), llmclient.Hooks{})

	audio := []byte("RIFF-fake-wav")
	resp, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:       "mimo-v2.5-asr",
		Filename:    "clip.wav",
		File:        audio,
		Language:    "auto",
		Temperature: "0.2",
	})
	if err != nil {
		t.Fatalf("CreateTranscription() error = %v", err)
	}
	if resp.ContentType != "application/json" {
		t.Fatalf("ContentType = %q, want application/json", resp.ContentType)
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil || out.Text != "hello there" {
		t.Fatalf("Data = %s, want {\"text\":\"hello there\"}", resp.Data)
	}

	wantDataURI := "data:audio/wav;base64," + base64.StdEncoding.EncodeToString(audio)
	body := string(gotBody)
	if !strings.Contains(body, `"type":"input_audio"`) || !strings.Contains(body, wantDataURI) {
		t.Fatalf("upstream body missing input_audio data URI, got: %s", body)
	}
	if strings.Contains(body, `"format"`) {
		t.Fatalf("upstream body should not contain a format field, got: %s", body)
	}
	if !strings.Contains(body, `"asr_options":{"language":"auto"}`) {
		t.Fatalf("upstream body missing asr_options, got: %s", body)
	}
	if !strings.Contains(body, `"temperature":0.2`) {
		t.Fatalf("upstream body missing forwarded temperature, got: %s", body)
	}
}

func TestCreateTranscription_TextFormatAndValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-asr","created":1677652288,"model":"mimo-v2.5-asr",
			"choices":[{"index":0,"message":{"role":"assistant","content":"plain text"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("mimo-key", server.URL, server.Client(), llmclient.Hooks{})

	resp, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model: "mimo-v2.5-asr", Filename: "clip.wav", File: []byte("audio"), ResponseFormat: "text",
	})
	if err != nil {
		t.Fatalf("CreateTranscription(text) error = %v", err)
	}
	if string(resp.Data) != "plain text" || !strings.HasPrefix(resp.ContentType, "text/plain") {
		t.Fatalf("got %q (%s), want plain text body", resp.Data, resp.ContentType)
	}

	for _, unsupported := range []core.AudioTranscriptionRequest{
		{Model: "mimo-v2.5-asr", File: []byte("audio"), ResponseFormat: "srt"},
		{Model: "mimo-v2.5-asr", File: []byte("audio"), ResponseFormat: "verbose_json"},
		{Model: "mimo-v2.5-asr", File: []byte("audio"), Prompt: "domain hint"},
		{Model: "mimo-v2.5-asr", File: []byte("audio"), TimestampGranularities: []string{"word"}},
		{Model: "mimo-v2.5-asr", File: []byte("audio"), Temperature: "not-a-number"},
		{Model: "mimo-v2.5-asr"},
	} {
		req := unsupported
		if _, err := provider.CreateTranscription(context.Background(), &req); err == nil {
			t.Fatalf("CreateTranscription(%+v) succeeded, want validation error", req)
		}
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
			var gotBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"x","model":"mimo-v2.5-asr","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
			}))
			defer server.Close()

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

			if _, err := provider.CreateTranscription(context.Background(), req); err != nil {
				t.Fatalf("CreateTranscription() error = %v", err)
			}

			wantData := tc.wantDataPrefix + base64.StdEncoding.EncodeToString(audio)
			if !strings.Contains(string(gotBody), wantData) {
				t.Fatalf("upstream body missing %q, got: %s", wantData, gotBody)
			}
		})
	}
}
