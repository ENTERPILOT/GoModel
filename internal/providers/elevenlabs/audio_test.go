package elevenlabs

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

func TestCreateSpeech_UsesVoiceIDInPathAndDefaultsToMP3(t *testing.T) {
	var gotPath, gotQuery, gotAuth string
	var gotBody speechRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("xi-api-key")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte{0x49, 0x44, 0x33})
	}))
	defer server.Close()

	provider := NewWithHTTPClient("elk_test", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model: "eleven_multilingual_v2",
		Input: "hello there",
		Voice: "21m00Tcm4TlvDq8ikWAM",
	})
	if err != nil {
		t.Fatalf("CreateSpeech() error = %v", err)
	}

	if gotPath != "/v1/text-to-speech/21m00Tcm4TlvDq8ikWAM" {
		t.Fatalf("path = %q, want voice_id in path", gotPath)
	}
	if gotQuery != "output_format=mp3_44100_128" {
		t.Fatalf("query = %q, want mp3_44100_128 output format", gotQuery)
	}
	if gotAuth != "elk_test" {
		t.Fatalf("xi-api-key = %q, want elk_test", gotAuth)
	}
	if gotBody.Text != "hello there" || gotBody.ModelID != "eleven_multilingual_v2" {
		t.Fatalf("request body = %+v", gotBody)
	}
	if gotBody.VoiceSetting != nil {
		t.Fatalf("voice_settings = %+v, want nil when speed unset", gotBody.VoiceSetting)
	}
	if resp.ContentType != "audio/mpeg" {
		t.Fatalf("content type = %q, want audio/mpeg", resp.ContentType)
	}
	if !bytes.Equal(resp.Data, []byte{0x49, 0x44, 0x33}) {
		t.Fatalf("audio data = %v", resp.Data)
	}
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
		{"pcm", "pcm", "output_format=pcm_44100", "audio/pcm"},
		{"wav", "wav", "output_format=wav_44100", "audio/wav"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotQuery string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotQuery = r.URL.RawQuery
				_, _ = w.Write([]byte{0x01})
			}))
			defer server.Close()

			provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
			resp, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
				Model: "eleven_multilingual_v2", Input: "hi", Voice: "voice-id",
				ResponseFormat: tt.responseFormat,
			})
			if err != nil {
				t.Fatalf("CreateSpeech() error = %v", err)
			}
			if gotQuery != tt.wantQuery {
				t.Fatalf("query = %q, want %q", gotQuery, tt.wantQuery)
			}
			if resp.ContentType != tt.wantContent {
				t.Fatalf("content type = %q, want %q", resp.ContentType, tt.wantContent)
			}
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
			var gotBody speechRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &gotBody)
				_, _ = w.Write([]byte{0x01})
			}))
			defer server.Close()

			provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
			if _, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
				Model: "eleven_multilingual_v2", Input: "hi", Voice: "voice-id", Speed: tt.speed,
			}); err != nil {
				t.Fatalf("CreateSpeech() error = %v", err)
			}
			if gotBody.VoiceSetting == nil || gotBody.VoiceSetting.Speed != tt.wantSpeed {
				t.Fatalf("voice_settings = %+v, want speed %v", gotBody.VoiceSetting, tt.wantSpeed)
			}
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
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CreateSpeech() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestCreateSpeech_ReturnsUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":{"status":"invalid_api_key","message":"bad key"}}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("bad-key", server.URL, server.Client(), llmclient.Hooks{})
	_, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model: "m", Input: "hi", Voice: "v",
	})
	gatewayErr, ok := err.(*core.GatewayError)
	if !ok {
		t.Fatalf("error type = %T, want *core.GatewayError", err)
	}
	if gatewayErr.StatusCode != http.StatusUnauthorized || gatewayErr.Type != core.ErrorTypeAuthentication {
		t.Fatalf("gateway error = %+v, want 401 authentication", gatewayErr)
	}
}

func TestCreateTranscription_SendsMultipartAndReturnsJSON(t *testing.T) {
	var gotPath, gotAuth string
	var gotModelID, gotLanguage, gotGranularity, gotFilename, gotFileContent string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("xi-api-key")

		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			http.Error(w, "bad content type", http.StatusBadRequest)
			return
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, "multipart error", http.StatusBadRequest)
				return
			}
			data, _ := io.ReadAll(part)
			switch part.FormName() {
			case "model_id":
				gotModelID = string(data)
			case "language_code":
				gotLanguage = string(data)
			case "timestamps_granularity":
				gotGranularity = string(data)
			case "file":
				gotFilename = part.FileName()
				gotFileContent = string(data)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"language_code":"en","text":"hello world","words":[{"text":"hello","type":"word","start":0,"end":0.5},{"text":" ","type":"spacing","start":0.5,"end":0.6},{"text":"world","type":"word","start":0.6,"end":1.1}]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("elk_test", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:    "scribe_v1",
		Filename: "clip.mp3",
		File:     []byte("fake-audio-bytes"),
		Language: "en",
	})
	if err != nil {
		t.Fatalf("CreateTranscription() error = %v", err)
	}

	if gotPath != "/v1/speech-to-text" || gotAuth != "elk_test" {
		t.Fatalf("path/auth = %q/%q", gotPath, gotAuth)
	}
	if gotModelID != "scribe_v1" || gotLanguage != "en" {
		t.Fatalf("model_id/language_code = %q/%q", gotModelID, gotLanguage)
	}
	if gotGranularity != "none" {
		t.Fatalf("timestamps_granularity = %q, want none", gotGranularity)
	}
	if gotFilename != "clip.mp3" || gotFileContent != "fake-audio-bytes" {
		t.Fatalf("file = %q/%q", gotFilename, gotFileContent)
	}
	if resp.ContentType != "application/json" {
		t.Fatalf("content type = %q, want application/json", resp.ContentType)
	}
	var decoded struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(resp.Data, &decoded); err != nil || decoded.Text != "hello world" {
		t.Fatalf("response body = %s, err = %v", resp.Data, err)
	}
}

func TestCreateTranscription_WordGranularityFromRequest(t *testing.T) {
	var gotGranularity string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			http.Error(w, "bad content type", http.StatusBadRequest)
			return
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				http.Error(w, "multipart error", http.StatusBadRequest)
				return
			}
			if part.FormName() == "timestamps_granularity" {
				data, _ := io.ReadAll(part)
				gotGranularity = string(data)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"hi"}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	if _, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:                  "scribe_v1",
		File:                   []byte("audio"),
		TimestampGranularities: []string{"word"},
	}); err != nil {
		t.Fatalf("CreateTranscription() error = %v", err)
	}
	if gotGranularity != "word" {
		t.Fatalf("timestamps_granularity = %q, want word", gotGranularity)
	}
}

func TestCreateTranscription_VerboseJSONIncludesWordsAndDuration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"language_code":"en","text":"hi there","words":[{"text":"hi","type":"word","start":0,"end":0.3},{"text":"there","type":"word","start":0.4,"end":0.9}]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:          "scribe_v1",
		File:           []byte("audio"),
		ResponseFormat: "verbose_json",
	})
	if err != nil {
		t.Fatalf("CreateTranscription() error = %v", err)
	}

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
	if err := json.Unmarshal(resp.Data, &decoded); err != nil {
		t.Fatalf("unmarshal error = %v, body = %s", err, resp.Data)
	}
	if decoded.Language != "en" || decoded.Text != "hi there" || decoded.Duration != 0.9 {
		t.Fatalf("verbose response = %+v", decoded)
	}
	if len(decoded.Words) != 2 || decoded.Words[1].Word != "there" {
		t.Fatalf("words = %+v", decoded.Words)
	}
}

func TestCreateTranscription_TextFormatReturnsPlainText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"plain text result"}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:          "scribe_v1",
		File:           []byte("audio"),
		ResponseFormat: "text",
	})
	if err != nil {
		t.Fatalf("CreateTranscription() error = %v", err)
	}
	if string(resp.Data) != "plain text result" {
		t.Fatalf("data = %q, want plain text result", resp.Data)
	}
	if !strings.HasPrefix(resp.ContentType, "text/plain") {
		t.Fatalf("content type = %q, want text/plain", resp.ContentType)
	}
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
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CreateTranscription() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}
