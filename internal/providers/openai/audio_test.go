package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
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
