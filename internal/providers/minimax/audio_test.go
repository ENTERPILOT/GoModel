package minimax

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

func TestProvider_ImplementsAudioProvider(t *testing.T) {
	provider := NewWithHTTPClient("key", "", nil, llmclient.Hooks{})
	if _, ok := any(provider).(core.AudioProvider); !ok {
		t.Fatal("minimax provider should implement core.AudioProvider")
	}
}

func TestCreateSpeech_UsesNativeEndpointAndDecodesHex(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotRequest speechRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(body, &gotRequest); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"audio":"000102ff","status":2},"base_resp":{"status_code":0,"status_msg":"success"}}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("minimax-key", server.URL+"/v1", server.Client(), llmclient.Hooks{})
	resp, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model:          "speech-2.8-hd",
		Input:          "hello",
		Voice:          "English_expressive_narrator",
		ResponseFormat: "wav",
		Speed:          1.5,
	})
	if err != nil {
		t.Fatalf("CreateSpeech() error = %v", err)
	}

	if gotPath != "/v1/t2a_v2" {
		t.Fatalf("path = %q, want /v1/t2a_v2", gotPath)
	}
	if gotAuth != "Bearer minimax-key" {
		t.Fatalf("authorization = %q, want Bearer minimax-key", gotAuth)
	}
	if gotRequest.Model != "speech-2.8-hd" || gotRequest.Text != "hello" {
		t.Fatalf("request model/text = %q/%q", gotRequest.Model, gotRequest.Text)
	}
	if gotRequest.Stream {
		t.Fatal("stream = true, want false")
	}
	if gotRequest.OutputFormat != "hex" {
		t.Fatalf("output_format = %q, want hex", gotRequest.OutputFormat)
	}
	if gotRequest.VoiceSetting.VoiceID != "English_expressive_narrator" || gotRequest.VoiceSetting.Speed != 1.5 {
		t.Fatalf("voice_setting = %+v", gotRequest.VoiceSetting)
	}
	if gotRequest.AudioSetting.Format != "wav" {
		t.Fatalf("audio_setting.format = %q, want wav", gotRequest.AudioSetting.Format)
	}
	if resp.ContentType != "audio/wav" {
		t.Fatalf("content type = %q, want audio/wav", resp.ContentType)
	}
	if !bytes.Equal(resp.Data, []byte{0x00, 0x01, 0x02, 0xff}) {
		t.Fatalf("audio data = %v", resp.Data)
	}
}

func TestCreateSpeech_DefaultsToMP3AndNormalSpeed(t *testing.T) {
	var gotRequest speechRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotRequest)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"audio":"ff","status":2},"base_resp":{"status_code":0}}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model: "speech-2.8-hd",
		Input: "hello",
		Voice: "voice-id",
	})
	if err != nil {
		t.Fatalf("CreateSpeech() error = %v", err)
	}
	if gotRequest.AudioSetting.Format != "mp3" || gotRequest.VoiceSetting.Speed != 1 {
		t.Fatalf("defaults = format %q, speed %v", gotRequest.AudioSetting.Format, gotRequest.VoiceSetting.Speed)
	}
	if resp.ContentType != "audio/mpeg" {
		t.Fatalf("content type = %q, want audio/mpeg", resp.ContentType)
	}
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
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CreateSpeech() error = %v, want substring %q", err, tt.want)
			}
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
		{name: "auth failed", nativeStatus: 1004, statusMsg: "authentication failed", wantHTTPStatus: http.StatusUnauthorized, wantType: core.ErrorTypeAuthentication},
		{name: "insufficient balance", nativeStatus: 1008, statusMsg: "insufficient balance", wantHTTPStatus: http.StatusPaymentRequired, wantType: core.ErrorTypeProvider},
		{name: "sensitive input", nativeStatus: 1026, statusMsg: "sensitive content", wantHTTPStatus: http.StatusBadRequest, wantType: core.ErrorTypeInvalidRequest},
		{name: "invalid params", nativeStatus: 2013, statusMsg: "invalid voice_id", wantHTTPStatus: http.StatusBadRequest, wantType: core.ErrorTypeInvalidRequest},
		{name: "unknown code", nativeStatus: 1000, statusMsg: "unknown error", wantHTTPStatus: http.StatusBadGateway, wantType: core.ErrorTypeProvider},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				body, _ := json.Marshal(map[string]any{
					"data":      nil,
					"base_resp": map[string]any{"status_code": tt.nativeStatus, "status_msg": tt.statusMsg},
				})
				_, _ = w.Write(body)
			}))
			defer server.Close()

			provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
			_, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
				Model: "speech-2.8-hd",
				Input: "hello",
				Voice: "voice-id",
			})
			var gatewayErr *core.GatewayError
			if !errors.As(err, &gatewayErr) {
				t.Fatalf("CreateSpeech() error = %v, want *core.GatewayError", err)
			}
			if gatewayErr.StatusCode != tt.wantHTTPStatus {
				t.Fatalf("status = %d, want %d", gatewayErr.StatusCode, tt.wantHTTPStatus)
			}
			if gatewayErr.Type != tt.wantType {
				t.Fatalf("type = %q, want %q", gatewayErr.Type, tt.wantType)
			}
			if !strings.Contains(gatewayErr.Message, tt.statusMsg) {
				t.Fatalf("message = %q, want substring %q", gatewayErr.Message, tt.statusMsg)
			}
		})
	}
}

func TestCreateSpeech_RejectsMalformedAudio(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"audio":"not-hex","status":2},"base_resp":{"status_code":0}}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	_, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model: "speech-2.8-hd",
		Input: "hello",
		Voice: "voice-id",
	})
	if err == nil || !strings.Contains(err.Error(), "not valid hexadecimal") {
		t.Fatalf("CreateSpeech() error = %v, want malformed audio error", err)
	}
}

func TestCreateTranscription_IsUnsupported(t *testing.T) {
	provider := NewWithHTTPClient("key", "", nil, llmclient.Hooks{})
	_, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{})
	if err == nil || !strings.Contains(err.Error(), "does not support speech-to-text") {
		t.Fatalf("CreateTranscription() error = %v", err)
	}
}
