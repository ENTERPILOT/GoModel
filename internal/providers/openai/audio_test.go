package openai

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

func newSpeechTestProvider(t *testing.T, handler http.HandlerFunc) *CompatibleProvider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return NewCompatibleProviderWithHTTPClient(
		"test-key",
		server.Client(),
		llmclient.Hooks{},
		CompatibleProviderConfig{ProviderName: "openai", BaseURL: server.URL},
	)
}

// TestCreateSpeech_PreservesUpstreamContentType ensures the response is tagged
// with the upstream Content-Type — the authoritative description of the bytes
// usage prices output-duration models from — rather than re-deriving it from the
// requested response_format.
func TestCreateSpeech_PreservesUpstreamContentType(t *testing.T) {
	tests := []struct {
		name           string
		responseFormat string
		upstreamType   string // "" => upstream sends no Content-Type
		wantType       string
	}{
		{"upstream wav honored", "wav", "audio/wav", "audio/wav"},
		// The provider transcoded to mp3 even though wav was requested: the
		// upstream type must win so billing sees the real format.
		{"upstream overrides request", "wav", "audio/mpeg", "audio/mpeg"},
		{"fallback to request format", "pcm", "", "audio/pcm"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := newSpeechTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
				if tt.upstreamType != "" {
					w.Header().Set("Content-Type", tt.upstreamType)
				} else {
					w.Header()["Content-Type"] = nil // suppress net/http content sniffing
				}
				_, _ = w.Write([]byte("audio-bytes"))
			})

			resp, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
				Model: "gpt-4o-mini-tts", Input: "hello", Voice: "alloy", ResponseFormat: tt.responseFormat,
			})
			if err != nil {
				t.Fatalf("CreateSpeech() error = %v", err)
			}
			if resp.ContentType != tt.wantType {
				t.Errorf("ContentType = %q, want %q", resp.ContentType, tt.wantType)
			}
		})
	}
}

func TestCreateTranslation_UsesTranslationMultipartShape(t *testing.T) {
	provider := newSpeechTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/translations" {
			t.Errorf("path = %q, want /audio/translations", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		for field, want := range map[string]string{
			"model": "whisper-1", "prompt": "Use product names", "response_format": "text", "temperature": "0.2",
		} {
			if got := r.FormValue(field); got != want {
				t.Errorf("%s = %q, want %q", field, got, want)
			}
		}
		if _, ok := r.MultipartForm.Value["language"]; ok {
			t.Error("translation request must not forward language")
		}
		if _, ok := r.MultipartForm.Value["timestamp_granularities[]"]; ok {
			t.Error("translation request must not forward timestamp granularities")
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("FormFile: %v", err)
		}
		defer func() { _ = file.Close() }()
		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatalf("ReadAll(file): %v", err)
		}
		if header.Filename != "speech.wav" || !bytes.Equal(data, []byte("wave-bytes")) {
			t.Errorf("file = %q %q, want speech.wav wave-bytes", header.Filename, data)
		}

		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("Hello from GoModel."))
	})

	resp, err := provider.CreateTranslation(context.Background(), &core.AudioTranscriptionRequest{
		Model:                  "whisper-1",
		Filename:               "speech.wav",
		File:                   []byte("wave-bytes"),
		Language:               "de",
		Prompt:                 "Use product names",
		ResponseFormat:         "text",
		Temperature:            "0.2",
		TimestampGranularities: []string{"word"},
	})
	if err != nil {
		t.Fatalf("CreateTranslation() error = %v", err)
	}
	if resp.ContentType != "text/plain; charset=utf-8" {
		t.Errorf("ContentType = %q, want text/plain; charset=utf-8", resp.ContentType)
	}
	if string(resp.Data) != "Hello from GoModel." {
		t.Errorf("Data = %q, want translated text", resp.Data)
	}
}

func TestCreateTranslation_RejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name        string
		req         *core.AudioTranscriptionRequest
		wantMessage string
	}{
		{name: "nil request", wantMessage: "audio translation request is required"},
		{name: "missing file", req: &core.AudioTranscriptionRequest{Model: "whisper-1"}, wantMessage: "file is required"},
	}

	provider := &CompatibleProvider{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := provider.CreateTranslation(context.Background(), tt.req)
			var gatewayErr *core.GatewayError
			if !errors.As(err, &gatewayErr) {
				t.Fatalf("CreateTranslation() error = %v, want GatewayError", err)
			}
			if gatewayErr.Message != tt.wantMessage {
				t.Fatalf("CreateTranslation() message = %q, want %q", gatewayErr.Message, tt.wantMessage)
			}
		})
	}
}

func TestMultipartAudioModelBreakerIsolation(t *testing.T) {
	var models []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1024); err != nil {
			t.Error(err)
			return
		}
		defer func() {
			if err := r.MultipartForm.RemoveAll(); err != nil {
				t.Error(err)
			}
		}()
		model := r.FormValue("model")
		models = append(models, model)
		if model == "transcribe" {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(`{"text":"translated"}`))
	}))
	defer server.Close()
	resilience := config.ResilienceConfig{Retry: config.DefaultRetryConfig(), CircuitBreaker: config.DefaultCircuitBreakerConfig()}
	resilience.CircuitBreaker.Scope = "model"
	resilience.CircuitBreaker.FailureThreshold = 1
	provider := NewCompatibleProvider("test", providers.ProviderOptions{Resilience: resilience}, CompatibleProviderConfig{ProviderName: "test", BaseURL: server.URL})
	_, err := provider.CreateTranscription(t.Context(), &core.AudioTranscriptionRequest{Model: "transcribe", File: []byte("audio")})
	if err == nil {
		t.Fatal("expected transcription failure")
	}
	_, err = provider.CreateTranslation(t.Context(), &core.AudioTranscriptionRequest{Model: "translate", File: []byte("audio")})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0] != "transcribe" || models[1] != "translate" {
		t.Fatalf("upstream models=%v", models)
	}
}
