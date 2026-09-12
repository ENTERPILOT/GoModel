package openai

import (
	"context"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestProvider starts a recording upstream served by handler and returns a
// compatible provider pointed at it together with the recorded requests.
func newTestProvider(t *testing.T, handler http.HandlerFunc) (*CompatibleProvider, *providertest.Capture) {
	t.Helper()
	server, capture := providertest.Server(t, handler)
	provider := NewCompatibleProviderWithHTTPClient(
		"test-key",
		server.Client(),
		llmclient.Hooks{},
		CompatibleProviderConfig{ProviderName: "openai", BaseURL: server.URL},
	)
	return provider, capture
}

// jsonHandler answers every request with body as application/json.
func jsonHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
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
			provider, _ := newTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
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
			require.NoError(t, err)
			assert.Equal(t, tt.wantType, resp.ContentType)
		})
	}
}

func TestCreateTranslation_UsesTranslationMultipartShape(t *testing.T) {
	provider, capture := newTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
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
	require.NoError(t, err)
	assert.Equal(t, "text/plain; charset=utf-8", resp.ContentType)
	assert.Equal(t, "Hello from GoModel.", string(resp.Data))

	req := capture.Last(t)
	assert.Equal(t, "/audio/translations", req.Path)
	form := recordedMultipart(t, req)
	for field, want := range map[string]string{
		"model": "whisper-1", "prompt": "Use product names", "response_format": "text", "temperature": "0.2",
	} {
		assert.Equal(t, []string{want}, form.values[field], field)
	}
	// Translations have no language or timestamp granularity parameters.
	assert.NotContains(t, form.values, "language")
	assert.NotContains(t, form.values, "timestamp_granularities[]")

	files := form.files["file"]
	require.Len(t, files, 1)
	assert.Equal(t, "speech.wav", files[0].filename)
	assert.Equal(t, "wave-bytes", files[0].data)
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
			require.ErrorAs(t, err, &gatewayErr)
			assert.Equal(t, tt.wantMessage, gatewayErr.Message)
		})
	}
}

// A model-scoped circuit breaker opened by one audio model must not block a
// different model on the other multipart endpoint.
func TestMultipartAudioModelBreakerIsolation(t *testing.T) {
	server, capture := providertest.Server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("model") == "transcribe" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"text":"translated"}`))
	})
	resilience := config.ResilienceConfig{Retry: config.DefaultRetryConfig(), CircuitBreaker: config.DefaultCircuitBreakerConfig()}
	resilience.CircuitBreaker.Scope = "model"
	resilience.CircuitBreaker.FailureThreshold = 1
	provider := NewCompatibleProvider("test", providers.ProviderOptions{Resilience: resilience}, CompatibleProviderConfig{ProviderName: "test", BaseURL: server.URL})

	_, err := provider.CreateTranscription(t.Context(), &core.AudioTranscriptionRequest{Model: "transcribe", File: []byte("audio")})
	require.Error(t, err)
	_, err = provider.CreateTranslation(t.Context(), &core.AudioTranscriptionRequest{Model: "translate", File: []byte("audio")})
	require.NoError(t, err)

	requests := capture.All()
	require.Len(t, requests, 2)
	assert.Equal(t, []string{"transcribe"}, recordedMultipart(t, requests[0]).values["model"])
	assert.Equal(t, []string{"translate"}, recordedMultipart(t, requests[1]).values["model"])
}
