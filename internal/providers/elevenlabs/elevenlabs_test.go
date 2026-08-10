package elevenlabs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

func TestNew_ConstructsRegisteredProvider(t *testing.T) {
	provider, ok := New(providers.ProviderConfig{
		APIKey:  "elk_test",
		BaseURL: "https://elevenlabs.example",
	}, providers.ProviderOptions{}).(*Provider)
	if !ok || provider.client == nil {
		t.Fatalf("New() = %T, want initialized *Provider", provider)
	}
	if Registration.Discovery.DefaultBaseURL != defaultBaseURL {
		t.Fatalf("registration base URL = %q, want %q", Registration.Discovery.DefaultBaseURL, defaultBaseURL)
	}
}

func TestProvider_ImplementsExpectedInterfaces(t *testing.T) {
	provider := NewWithHTTPClient("key", "", nil, llmclient.Hooks{})
	if _, ok := any(provider).(core.Provider); !ok {
		t.Fatal("elevenlabs provider should implement core.Provider")
	}
	if _, ok := any(provider).(core.AudioProvider); !ok {
		t.Fatal("elevenlabs provider should implement core.AudioProvider")
	}
	if _, ok := any(provider).(core.PassthroughProvider); !ok {
		t.Fatal("elevenlabs provider should implement core.PassthroughProvider")
	}
}

func TestSetBaseURL_ChangesRequestTarget(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("xi-api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("elk_test", "https://unused.example", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)
	if _, err := provider.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/v1/models" {
		t.Fatalf("method/path = %q/%q, want GET /v1/models", gotMethod, gotPath)
	}
	if gotAuth != "elk_test" {
		t.Fatalf("xi-api-key = %q, want elk_test", gotAuth)
	}
}

func TestListModels_FiltersToTextToSpeechAndAddsScribe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"model_id":"eleven_multilingual_v2","name":"Eleven Multilingual v2","can_do_text_to_speech":true,"languages":[{"language_id":"en"},{"language_id":"es"}]},
			{"model_id":"eleven_english_sts_v2","name":"Eleven English STS v2","can_do_text_to_speech":false}
		]`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}

	byID := make(map[string]core.Model, len(resp.Data))
	for _, model := range resp.Data {
		byID[model.ID] = model
	}
	if _, ok := byID["eleven_english_sts_v2"]; ok {
		t.Fatal("ListModels() should exclude models that cannot do text-to-speech")
	}
	tts, ok := byID["eleven_multilingual_v2"]
	if !ok {
		t.Fatal("ListModels() should include text-to-speech models")
	}
	if tts.Metadata == nil || len(tts.Metadata.Modes) != 1 || tts.Metadata.Modes[0] != "audio_speech" {
		t.Fatalf("tts model metadata = %+v, want audio_speech mode", tts.Metadata)
	}
	if !tts.Metadata.Capabilities["multilingual"] {
		t.Fatalf("tts model capabilities = %+v, want multilingual", tts.Metadata.Capabilities)
	}
	if _, ok := byID["scribe_v1"]; !ok {
		t.Fatal("ListModels() should include the static scribe_v1 model")
	}
	scribe, ok := byID["scribe_v2"]
	if !ok {
		t.Fatal("ListModels() should include the static scribe_v2 model")
	}
	if scribe.Metadata == nil || len(scribe.Metadata.Modes) != 1 || scribe.Metadata.Modes[0] != "audio_transcription" {
		t.Fatalf("scribe model metadata = %+v, want audio_transcription mode", scribe.Metadata)
	}
}

func TestListModels_FallsBackToStaticModelsOnFirstFetchFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v, want static fallback on first-ever fetch failure", err)
	}
	if len(resp.Data) != len(staticTranscriptionModels) {
		t.Fatalf("ListModels() data = %+v, want only the static transcription models", resp.Data)
	}
	if _, ok := func() (core.Model, bool) {
		for _, m := range resp.Data {
			if m.ID == "scribe_v2" {
				return m, true
			}
		}
		return core.Model{}, false
	}(); !ok {
		t.Fatal("ListModels() fallback should include scribe_v2")
	}
}

func TestListModels_PropagatesErrorOnceCatalogHasSucceededOnce(t *testing.T) {
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"model_id":"eleven_multilingual_v2","name":"Eleven Multilingual v2","can_do_text_to_speech":true}]`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("key", server.URL, server.Client(), llmclient.Hooks{})
	if _, err := provider.ListModels(context.Background()); err != nil {
		t.Fatalf("first ListModels() error = %v, want success", err)
	}

	fail = true
	if _, err := provider.ListModels(context.Background()); err == nil {
		t.Fatal("ListModels() error = nil, want propagated catalog error once a fetch has already succeeded, so the registry's stale-inventory carry-forward keeps the larger prior list instead of this call shrinking it")
	}
}

func TestUnsupportedCapabilities_ReturnInvalidRequestErrors(t *testing.T) {
	provider := NewWithHTTPClient("key", "", nil, llmclient.Hooks{})

	tests := []struct {
		name string
		call func() error
		want string
	}{
		{"chat", func() error { _, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{}); return err }, "does not support chat"},
		{"chat stream", func() error {
			_, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{})
			return err
		}, "does not support chat"},
		{"responses", func() error { _, err := provider.Responses(context.Background(), &core.ResponsesRequest{}); return err }, "does not support the responses"},
		{"responses stream", func() error {
			_, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{})
			return err
		}, "does not support the responses"},
		{"embeddings", func() error {
			_, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{})
			return err
		}, "does not support embeddings"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestPassthrough_ForwardsOpaqueRequest(t *testing.T) {
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("xi-api-key")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("elk_test", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodGet,
		Endpoint: "voices",
		Headers:  http.Header{},
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	defer resp.Body.Close()

	if gotPath != "/voices" {
		t.Fatalf("path = %q, want /voices", gotPath)
	}
	if gotAuth != "elk_test" {
		t.Fatalf("xi-api-key = %q, want elk_test", gotAuth)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
}
