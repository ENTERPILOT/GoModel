package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

// sttUpstream simulates an OpenAI-compatible upstream with configurable
// /models and /audio/transcriptions behavior and counts transcription probes.
type sttUpstream struct {
	modelsStatus        int
	modelsBody          string
	transcriptionStatus int
	probes              atomic.Int32
}

func (u *sttUpstream) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/models", func(w http.ResponseWriter, _ *http.Request) {
		if u.modelsStatus != http.StatusOK {
			http.Error(w, `{"error":{"message":"not found"}}`, u.modelsStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(u.modelsBody))
	})
	mux.HandleFunc("/audio/transcriptions", func(w http.ResponseWriter, r *http.Request) {
		u.probes.Add(1)
		// The probe contract is a POST with a deliberately empty body; anything
		// else reaching this handler means the fallback sent a real request.
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":{"message":"probe must be a POST"}}`, http.StatusInternalServerError)
			return
		}
		if body, err := io.ReadAll(r.Body); err != nil || len(body) != 0 {
			http.Error(w, `{"error":{"message":"probe body must be empty"}}`, http.StatusInternalServerError)
			return
		}
		if u.transcriptionStatus == http.StatusNotFound {
			http.NotFound(w, r)
			return
		}
		http.Error(w, `{"error":{"message":"file is required"}}`, u.transcriptionStatus)
	})
	return mux
}

func TestListModels_SpeechToTextFallback(t *testing.T) {
	tests := []struct {
		name                string
		modelsStatus        int
		modelsBody          string
		transcriptionStatus int
		wantFallback        bool
		wantErrStatus       int // 0 means no error expected
		wantProbes          int32
	}{
		{
			name:                "models endpoint missing, transcriptions present",
			modelsStatus:        http.StatusNotFound,
			transcriptionStatus: http.StatusUnprocessableEntity,
			wantFallback:        true,
			wantProbes:          1,
		},
		{
			// Middleware that authenticates before route matching answers 401
			// for absent routes too, so an auth failure must not count as
			// proof the transcription endpoint exists.
			name:                "models endpoint missing, transcriptions behind failing auth",
			modelsStatus:        http.StatusNotFound,
			transcriptionStatus: http.StatusUnauthorized,
			wantErrStatus:       http.StatusNotFound,
			wantProbes:          1,
		},
		{
			name:                "models endpoint missing, transcriptions missing too",
			modelsStatus:        http.StatusNotFound,
			transcriptionStatus: http.StatusNotFound,
			wantErrStatus:       http.StatusNotFound,
			wantProbes:          1,
		},
		{
			name:                "models endpoint auth failure is not probed",
			modelsStatus:        http.StatusUnauthorized,
			transcriptionStatus: http.StatusUnprocessableEntity,
			wantErrStatus:       http.StatusUnauthorized,
			wantProbes:          0,
		},
		{
			name:                "empty model list, transcriptions present",
			modelsStatus:        http.StatusOK,
			modelsBody:          `{"object":"list","data":[]}`,
			transcriptionStatus: http.StatusBadRequest,
			wantFallback:        true,
			wantProbes:          1,
		},
		{
			name:                "populated model list is returned as-is",
			modelsStatus:        http.StatusOK,
			modelsBody:          `{"object":"list","data":[{"id":"gpt-test","object":"model"}]}`,
			transcriptionStatus: http.StatusUnprocessableEntity,
			wantProbes:          0,
		},
		{
			name:                "empty model list without transcriptions stays empty",
			modelsStatus:        http.StatusOK,
			modelsBody:          `{"object":"list","data":[]}`,
			transcriptionStatus: http.StatusNotFound,
			wantProbes:          1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &sttUpstream{
				modelsStatus:        tt.modelsStatus,
				modelsBody:          tt.modelsBody,
				transcriptionStatus: tt.transcriptionStatus,
			}
			server := httptest.NewServer(upstream.handler())
			defer server.Close()

			provider := NewWithHTTPClient("test-key", server.Client(), llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			resp, err := provider.ListModels(context.Background())

			if tt.wantErrStatus != 0 {
				if err == nil {
					t.Fatalf("ListModels() error = nil, want status %d", tt.wantErrStatus)
				}
				var gatewayErr *core.GatewayError
				if !errors.As(err, &gatewayErr) {
					t.Fatalf("ListModels() error = %v, want *core.GatewayError", err)
				}
				if gatewayErr.StatusCode != tt.wantErrStatus {
					t.Errorf("error status = %d, want %d", gatewayErr.StatusCode, tt.wantErrStatus)
				}
			} else if err != nil {
				t.Fatalf("ListModels() error = %v", err)
			}

			if tt.wantFallback {
				if len(resp.Data) != 1 || resp.Data[0].ID != sttFallbackModelID {
					t.Fatalf("ListModels() = %+v, want single %q model", resp, sttFallbackModelID)
				}
				meta := resp.Data[0].Metadata
				if meta == nil || len(meta.Modes) != 1 || meta.Modes[0] != "audio_transcription" {
					t.Errorf("fallback model metadata = %+v, want audio_transcription mode", meta)
				}
				if resp.Data[0].OwnedBy != "openai" {
					t.Errorf("fallback model owned_by = %q, want %q", resp.Data[0].OwnedBy, "openai")
				}
			}

			if got := upstream.probes.Load(); got != tt.wantProbes {
				t.Errorf("transcription probes = %d, want %d", got, tt.wantProbes)
			}
		})
	}
}

func TestNew_ConfiguredModelsDisableSpeechToTextDetection(t *testing.T) {
	withModels := New(providers.ProviderConfig{
		APIKey: "test-key",
		Models: []string{"Systran/faster-whisper-small"},
	}, providers.ProviderOptions{}).(*Provider)
	if withModels.detectSpeechToText {
		t.Error("detectSpeechToText = true with configured models, want false so the configured list stays authoritative")
	}

	withoutModels := New(providers.ProviderConfig{APIKey: "test-key"}, providers.ProviderOptions{}).(*Provider)
	if !withoutModels.detectSpeechToText {
		t.Error("detectSpeechToText = false without configured models, want true")
	}
}

func TestListModels_SpeechToTextFallbackDisabledByDefault(t *testing.T) {
	upstream := &sttUpstream{
		modelsStatus:        http.StatusNotFound,
		transcriptionStatus: http.StatusUnprocessableEntity,
	}
	server := httptest.NewServer(upstream.handler())
	defer server.Close()

	provider := NewCompatibleProviderWithHTTPClient("test-key", server.Client(), llmclient.Hooks{}, CompatibleProviderConfig{
		ProviderName: "custom",
		BaseURL:      server.URL,
	})

	if _, err := provider.ListModels(context.Background()); err == nil {
		t.Fatal("ListModels() error = nil, want missing /models error when detection is off")
	}
	if got := upstream.probes.Load(); got != 0 {
		t.Errorf("transcription probes = %d, want 0", got)
	}
}
