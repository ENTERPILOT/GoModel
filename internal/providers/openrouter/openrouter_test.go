package openrouter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
)

// ListModels must keep OpenRouter's architecture modalities and context
// length, mapping output modalities onto gateway modes so the catalog's long
// tail is categorized without remote-registry entries.
func TestListModels_StampsArchitectureModalities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("Path = %q, want /models", r.URL.Path)
		}
		// The endpoint defaults to text-output models; without this parameter
		// embedding models would never enter the catalog.
		if got := r.URL.Query().Get("output_modalities"); got != "all" {
			t.Errorf("output_modalities = %q, want all", got)
		}
		_, _ = w.Write([]byte(`{"data":[
			{"id":"openai/gpt-4o-mini","created":1721260800,"context_length":128000,
			 "architecture":{"input_modalities":["text","image"],"output_modalities":["text"]}},
			{"id":"google/gemini-3-pro-image","created":1721260800,
			 "architecture":{"input_modalities":["text"],"output_modalities":["image"]}},
			{"id":"voyageai/voyage-4-lite","created":1721260800,
			 "architecture":{"input_modalities":["text"],"output_modalities":["embeddings"]}},
			{"id":"fish-audio/s1","created":1721260800,
			 "architecture":{"input_modalities":["text"],"output_modalities":["speech"]}},
			{"id":"mistralai/voxtral-mini-3b-2507","created":1721260800,
			 "architecture":{"input_modalities":["audio"],"output_modalities":["transcription"]}},
			{"id":"cohere/rerank-only","created":1721260800,
			 "architecture":{"input_modalities":["text"],"output_modalities":["rerank"]}},
			{"id":"acme/video-only","created":1721260800,
			 "architecture":{"input_modalities":["text"],"output_modalities":["video"]}},
			{"id":"mystery/no-architecture","created":1721260800}
		]}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Data) != 6 {
		t.Fatalf("len(Data) = %d, want 6 (rerank-only and video-only skipped): %+v", len(resp.Data), resp.Data)
	}
	byID := map[string]core.Model{}
	for _, m := range resp.Data {
		byID[m.ID] = m
	}

	chat := byID["openai/gpt-4o-mini"]
	if chat.Metadata == nil || len(chat.Metadata.Modes) != 1 || chat.Metadata.Modes[0] != "chat" {
		t.Errorf("gpt-4o-mini metadata = %+v, want chat modes", chat.Metadata)
	}
	if chat.Metadata == nil || chat.Metadata.ContextWindow == nil || *chat.Metadata.ContextWindow != 128000 {
		t.Errorf("gpt-4o-mini context window = %+v, want 128000", chat.Metadata)
	}
	image := byID["google/gemini-3-pro-image"]
	if image.Metadata == nil || len(image.Metadata.Modes) != 1 || image.Metadata.Modes[0] != "image_generation" {
		t.Errorf("image model metadata = %+v, want image_generation modes", image.Metadata)
	}
	if image.Metadata == nil || len(image.Metadata.Categories) != 1 || image.Metadata.Categories[0] != core.CategoryImage {
		t.Errorf("image model categories = %+v, want [image]", image.Metadata)
	}
	embed := byID["voyageai/voyage-4-lite"]
	if embed.Metadata == nil || len(embed.Metadata.Modes) != 1 || embed.Metadata.Modes[0] != "embedding" {
		t.Errorf("voyage-4-lite metadata = %+v, want embedding modes", embed.Metadata)
	}
	if embed.Metadata == nil || len(embed.Metadata.Categories) != 1 || embed.Metadata.Categories[0] != core.CategoryEmbedding {
		t.Errorf("voyage-4-lite categories = %+v, want [embedding]", embed.Metadata)
	}
	speech := byID["fish-audio/s1"]
	if speech.Metadata == nil || len(speech.Metadata.Modes) != 1 || speech.Metadata.Modes[0] != "audio_speech" {
		t.Errorf("speech model metadata = %+v, want audio_speech modes", speech.Metadata)
	}
	if speech.Metadata == nil || len(speech.Metadata.Categories) != 1 || speech.Metadata.Categories[0] != core.CategoryAudio {
		t.Errorf("speech model categories = %+v, want [audio]", speech.Metadata)
	}
	stt := byID["mistralai/voxtral-mini-3b-2507"]
	if stt.Metadata == nil || len(stt.Metadata.Modes) != 1 || stt.Metadata.Modes[0] != "audio_transcription" {
		t.Errorf("transcription model metadata = %+v, want audio_transcription modes", stt.Metadata)
	}
	if _, ok := byID["cohere/rerank-only"]; ok {
		t.Error("rerank-only model must be skipped: no gateway surface reaches it on OpenRouter")
	}
	if _, ok := byID["acme/video-only"]; ok {
		t.Error("video-only model must be skipped: no gateway surface reaches it on OpenRouter")
	}
	noArch, ok := byID["mystery/no-architecture"]
	if !ok {
		t.Fatal("no-architecture model must be retained: missing signal is not proof of unservability")
	}
	if noArch.Metadata != nil {
		t.Errorf("no-architecture metadata = %+v, want nil", noArch.Metadata)
	}
}

// A failed upstream listing must propagate as an error, not an empty catalog.
func TestListModels_UpstreamErrorPropagates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream exploded"}}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.ListModels(context.Background())
	if err == nil {
		t.Fatalf("expected error, got response: %+v", resp)
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error type = %T, want *core.GatewayError: %v", err, err)
	}
}

// Audio flows through the embedded OpenAI-compatible implementation;
// OpenRouter-specific request mutation (attribution headers) must still apply
// on that path so audio traffic is attributed like every other call.
func TestAudio_UsesOpenAISurfaceWithAttributionHeaders(t *testing.T) {
	type seen struct {
		path    string
		referer string
	}
	requests := make(chan seen, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- seen{path: r.URL.Path, referer: r.Header.Get("HTTP-Referer")}
		switch r.URL.Path {
		case "/audio/speech":
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte("mp3-bytes"))
		case "/audio/transcriptions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"text":"hello"}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	speech, err := provider.CreateSpeech(context.Background(), &core.AudioSpeechRequest{
		Model: "fish-audio/s1",
		Input: "hello world",
		Voice: "alloy",
	})
	if err != nil {
		t.Fatalf("CreateSpeech() error = %v", err)
	}
	if speech.ContentType != "audio/mpeg" {
		t.Errorf("speech ContentType = %q, want audio/mpeg", speech.ContentType)
	}

	transcription, err := provider.CreateTranscription(context.Background(), &core.AudioTranscriptionRequest{
		Model:    "mistralai/voxtral-mini-3b-2507",
		File:     []byte("wav-bytes"),
		Filename: "clip.wav",
	})
	if err != nil {
		t.Fatalf("CreateTranscription() error = %v", err)
	}
	if !strings.Contains(string(transcription.Data), "hello") {
		t.Errorf("transcription Data = %q, want to contain hello", transcription.Data)
	}

	for _, want := range []string{"/audio/speech", "/audio/transcriptions"} {
		got := <-requests
		if got.path != want {
			t.Errorf("path = %q, want %q", got.path, want)
		}
		if got.referer != defaultSiteURL {
			t.Errorf("HTTP-Referer on %s = %q, want %q", want, got.referer, defaultSiteURL)
		}
	}
}

func TestChatCompletion_AddsDefaultAttributionHeaders(t *testing.T) {
	var gotReferer string
	var gotTitle string
	var gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReferer = r.Header.Get("HTTP-Referer")
		gotTitle = r.Header.Get("X-OpenRouter-Title")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-123",
			"object":"chat.completion",
			"created":1677652288,
			"model":"openai/gpt-4o-mini",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "openai/gpt-4o-mini",
		Messages: []core.Message{
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAuth != "Bearer test-api-key" {
		t.Fatalf("authorization = %q, want Bearer test-api-key", gotAuth)
	}
	if gotReferer != defaultSiteURL {
		t.Fatalf("HTTP-Referer = %q, want %q", gotReferer, defaultSiteURL)
	}
	if gotTitle != defaultAppName {
		t.Fatalf("X-OpenRouter-Title = %q, want %q", gotTitle, defaultAppName)
	}
}

func TestChatCompletion_ForwardsGoModelSessionID(t *testing.T) {
	gotSessionID := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSessionID <- r.Header.Get("X-Session-Id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-123","object":"chat.completion","created":1677652288,
			"model":"openai/gpt-4o-mini",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)
	ctx := core.WithSessionID(context.Background(), "conversation-42")
	_, err := provider.ChatCompletion(ctx, &core.ChatRequest{
		Model:    "openai/gpt-4o-mini",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if got := <-gotSessionID; got != "conversation-42" {
		t.Fatalf("X-Session-Id = %q, want conversation-42", got)
	}
}

func TestChatCompletion_UsesEnvOverridesForAttributionHeaders(t *testing.T) {
	t.Setenv("OPENROUTER_SITE_URL", "https://example.com")
	t.Setenv("OPENROUTER_APP_NAME", "Example App")

	var gotReferer string
	var gotTitle string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReferer = r.Header.Get("HTTP-Referer")
		gotTitle = r.Header.Get("X-OpenRouter-Title")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-123",
			"object":"chat.completion",
			"created":1677652288,
			"model":"openai/gpt-4o-mini",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "openai/gpt-4o-mini",
		Messages: []core.Message{
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotReferer != "https://example.com" {
		t.Fatalf("HTTP-Referer = %q, want https://example.com", gotReferer)
	}
	if gotTitle != "Example App" {
		t.Fatalf("X-OpenRouter-Title = %q, want Example App", gotTitle)
	}
}

func TestPassthrough_PreservesUserProvidedAttributionHeaders(t *testing.T) {
	var gotReferer string
	var gotTitle string
	var gotLegacyTitle string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReferer = r.Header.Get("HTTP-Referer")
		gotTitle = r.Header.Get("X-OpenRouter-Title")
		gotLegacyTitle = r.Header.Get("X-Title")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "responses",
		Body:     io.NopCloser(strings.NewReader(`{"model":"openai/gpt-4o-mini"}`)),
		Headers: http.Header{
			"Content-Type": {"application/json"},
			"HTTP-Referer": {"https://caller.example"},
			"X-Title":      {"Caller App"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if gotReferer != "https://caller.example" {
		t.Fatalf("HTTP-Referer = %q, want https://caller.example", gotReferer)
	}
	if gotLegacyTitle != "Caller App" {
		t.Fatalf("X-Title = %q, want Caller App", gotLegacyTitle)
	}
	if gotTitle != "" {
		t.Fatalf("X-OpenRouter-Title = %q, want empty when caller provided X-Title", gotTitle)
	}
}
