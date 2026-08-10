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

func TestUnsupportedCapabilities_ReturnInvalidRequestErrors(t *testing.T) {
	provider := NewWithHTTPClient("key", "", nil, llmclient.Hooks{})

	if _, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{}); err == nil || !strings.Contains(err.Error(), "does not support chat") {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if _, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{}); err == nil || !strings.Contains(err.Error(), "does not support chat") {
		t.Fatalf("StreamChatCompletion() error = %v", err)
	}
	if _, err := provider.Responses(context.Background(), &core.ResponsesRequest{}); err == nil || !strings.Contains(err.Error(), "does not support the responses") {
		t.Fatalf("Responses() error = %v", err)
	}
	if _, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{}); err == nil || !strings.Contains(err.Error(), "does not support the responses") {
		t.Fatalf("StreamResponses() error = %v", err)
	}
	if _, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{}); err == nil || !strings.Contains(err.Error(), "does not support embeddings") {
		t.Fatalf("Embeddings() error = %v", err)
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
