package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
	"gomodel/internal/providers/googleauth"

	"golang.org/x/oauth2"
)

func TestNew(t *testing.T) {
	apiKey := "test-api-key"
	// Use NewWithHTTPClient to get concrete type for internal testing
	provider := NewWithHTTPClient(apiKey, nil, llmclient.Hooks{})

	if provider.apiKey != apiKey {
		t.Errorf("apiKey = %q, want %q", provider.apiKey, apiKey)
	}
	if provider.modelsURL != defaultModelsBaseURL {
		t.Errorf("modelsURL = %q, want %q", provider.modelsURL, defaultModelsBaseURL)
	}
	if provider.client == nil {
		t.Error("client should not be nil")
	}
}

func TestNew_ReturnsProvider(t *testing.T) {
	provider := New(providers.ProviderConfig{APIKey: "test-api-key"}, providers.ProviderOptions{})

	if provider == nil {
		t.Error("provider should not be nil")
	}
}

func TestNew_AIStudioRejectsGCPAuthAliases(t *testing.T) {
	for _, authType := range []string{"adc", "service_account"} {
		t.Run(authType, func(t *testing.T) {
			provider := New(providers.ProviderConfig{
				BaseURL:  defaultOpenAICompatibleBaseURL,
				AuthType: authType,
			}, providers.ProviderOptions{})

			geminiProvider, ok := provider.(*Provider)
			if !ok {
				t.Fatalf("provider type = %T, want *Provider", provider)
			}
			err := geminiProvider.ready()
			if err == nil {
				t.Fatal("expected GCP auth alias to be rejected for AI Studio")
			}
			if !strings.Contains(err.Error(), "ai studio backend does not support GCP auth") {
				t.Fatalf("error = %v, want AI Studio GCP auth rejection", err)
			}
		})
	}
}

func TestNew_VertexAcceptsGCPAuthAliases(t *testing.T) {
	for _, authType := range []string{"adc", "service_account"} {
		t.Run(authType, func(t *testing.T) {
			p := NewVertexWithHTTPClient(providers.ProviderConfig{
				AuthType:       authType,
				VertexProject:  "prod-ai",
				VertexLocation: "us-central1",
			}, providers.ProviderOptions{}, http.DefaultClient)

			if err := p.ready(); err != nil {
				t.Fatalf("ready() error = %v, want nil for Vertex auth alias", err)
			}
		})
	}
}

func TestNew_VertexConfigErrorUsesVertexProviderName(t *testing.T) {
	p := NewVertexWithHTTPClient(providers.ProviderConfig{
		AuthType: "api_key",
		BaseURL:  "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/publishers/google",
	}, providers.ProviderOptions{}, http.DefaultClient)

	err := p.ready()
	if err == nil {
		t.Fatal("expected config error, got nil")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %T %[1]v, want *core.GatewayError", err)
	}
	if gatewayErr.Provider != "vertex" {
		t.Fatalf("provider = %q, want vertex", gatewayErr.Provider)
	}
}

func TestGeminiBaseURLs(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		wantCompat string
		wantNative string
	}{
		{
			name:       "empty uses official defaults",
			wantCompat: defaultOpenAICompatibleBaseURL,
			wantNative: defaultModelsBaseURL,
		},
		{
			name:       "official OpenAI-compatible default derives native default",
			configured: defaultOpenAICompatibleBaseURL,
			wantCompat: defaultOpenAICompatibleBaseURL,
			wantNative: defaultModelsBaseURL,
		},
		{
			name:       "official native default keeps OpenAI-compatible default",
			configured: defaultModelsBaseURL,
			wantCompat: defaultOpenAICompatibleBaseURL,
			wantNative: defaultModelsBaseURL,
		},
		{
			name:       "custom OpenAI-compatible URL derives native sibling",
			configured: "https://proxy.example.com/v1beta/openai/",
			wantCompat: "https://proxy.example.com/v1beta/openai",
			wantNative: "https://proxy.example.com/v1beta",
		},
		{
			name:       "custom URL without OpenAI suffix is used for both clients",
			configured: "https://proxy.example.com/gemini",
			wantCompat: "https://proxy.example.com/gemini",
			wantNative: "https://proxy.example.com/gemini",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCompat, gotNative := geminiBaseURLs(providers.ProviderConfig{BaseURL: tt.configured}, geminiBackendAIStudio)
			if gotCompat != tt.wantCompat {
				t.Fatalf("OpenAI-compatible base = %q, want %q", gotCompat, tt.wantCompat)
			}
			if gotNative != tt.wantNative {
				t.Fatalf("native base = %q, want %q", gotNative, tt.wantNative)
			}
		})
	}
}

func TestVertexBaseURLs(t *testing.T) {
	tests := []struct {
		name       string
		cfg        providers.ProviderConfig
		wantCompat string
		wantNative string
	}{
		{
			name: "derives official vertex bases from project and location",
			cfg: providers.ProviderConfig{
				VertexProject:  "prod-ai",
				VertexLocation: "us-central1",
			},
			wantCompat: "https://aiplatform.googleapis.com/v1/projects/prod-ai/locations/us-central1/endpoints/openapi",
			wantNative: "https://aiplatform.googleapis.com/v1/projects/prod-ai/locations/us-central1/publishers/google",
		},
		{
			name: "custom OpenAI-compatible vertex URL derives native sibling",
			cfg: providers.ProviderConfig{
				BaseURL: "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/endpoints/openapi/",
			},
			wantCompat: "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/endpoints/openapi",
			wantNative: "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/publishers/google",
		},
		{
			name: "custom native vertex URL derives OpenAI-compatible sibling",
			cfg: providers.ProviderConfig{
				BaseURL: "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/publishers/google/",
			},
			wantCompat: "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/endpoints/openapi",
			wantNative: "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/publishers/google",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCompat, gotNative := geminiBaseURLs(tt.cfg, geminiBackendVertex)
			if gotCompat != tt.wantCompat {
				t.Fatalf("OpenAI-compatible base = %q, want %q", gotCompat, tt.wantCompat)
			}
			if gotNative != tt.wantNative {
				t.Fatalf("native base = %q, want %q", gotNative, tt.wantNative)
			}
		})
	}
}

func TestVertexModelsBaseURL(t *testing.T) {
	tests := []struct {
		name       string
		nativeBase string
		want       string
	}{
		{
			name:       "official vertex native base",
			nativeBase: "https://aiplatform.googleapis.com/v1/projects/prod-ai/locations/us-central1/publishers/google",
			want:       "https://aiplatform.googleapis.com/v1beta1/publishers/google",
		},
		{
			name:       "custom proxy vertex native base",
			nativeBase: "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/publishers/google",
			want:       "https://proxy.example.com/v1beta1/publishers/google",
		},
		{
			name:       "unknown custom base keeps native base",
			nativeBase: "https://proxy.example.com/custom/gemini",
			want:       "https://proxy.example.com/custom/gemini",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := geminiModelsBaseURL(geminiBackendVertex, tt.nativeBase); got != tt.want {
				t.Fatalf("geminiModelsBaseURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNewVertexWithHTTPClientAcceptsBaseURLWithoutProjectLocation(t *testing.T) {
	p := NewVertexWithHTTPClient(providers.ProviderConfig{
		BaseURL:  "https://proxy.example.com/v1/projects/prod-ai/locations/us-central1/publishers/google",
		AuthType: "gcp_adc",
	}, providers.ProviderOptions{}, http.DefaultClient)

	if err := p.ready(); err != nil {
		t.Fatalf("ready() error = %v, want nil for Vertex custom base URL", err)
	}
}

func TestVertexModelNormalization(t *testing.T) {
	tests := []struct {
		in         string
		wantNative string
		wantOpenAI string
	}{
		{in: "gemini-2.5-flash", wantNative: "gemini-2.5-flash", wantOpenAI: "google/gemini-2.5-flash"},
		{in: "models/gemini-2.5-flash", wantNative: "gemini-2.5-flash", wantOpenAI: "google/gemini-2.5-flash"},
		{in: "google/gemini-2.5-flash", wantNative: "gemini-2.5-flash", wantOpenAI: "google/gemini-2.5-flash"},
		{
			in:         "projects/prod-ai/locations/us-central1/publishers/google/models/gemini-2.5-flash",
			wantNative: "gemini-2.5-flash",
			wantOpenAI: "google/gemini-2.5-flash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := normalizeGeminiModelID(tt.in); got != tt.wantNative {
				t.Fatalf("normalizeGeminiModelID() = %q, want %q", got, tt.wantNative)
			}
			if got := vertexOpenAIModelID(tt.in); got != tt.wantOpenAI {
				t.Fatalf("vertexOpenAIModelID() = %q, want %q", got, tt.wantOpenAI)
			}
		})
	}
}

func TestNew_CustomBaseURLDerivesNativeBaseURL(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	provider := New(providers.ProviderConfig{
		APIKey:  "test-api-key",
		BaseURL: "https://proxy.example.com/v1beta/openai",
	}, providers.ProviderOptions{})

	geminiProvider, ok := provider.(*Provider)
	if !ok {
		t.Fatalf("provider type = %T, want *Provider", provider)
	}
	if !geminiProvider.useNativeAPI {
		t.Fatal("useNativeAPI = false, want true for custom OpenAI-compatible base URL")
	}
	if got := geminiProvider.client.BaseURL(); got != "https://proxy.example.com/v1beta/openai" {
		t.Fatalf("client.BaseURL() = %q, want OpenAI-compatible base URL", got)
	}
	if geminiProvider.modelsURL != "https://proxy.example.com/v1beta" {
		t.Fatalf("modelsURL = %q, want derived native base URL", geminiProvider.modelsURL)
	}
	if geminiProvider.nativeClient.BaseURL() != "https://proxy.example.com/v1beta" {
		t.Fatalf("nativeClient.BaseURL() = %q, want derived native base URL", geminiProvider.nativeClient.BaseURL())
	}
}

func TestSetBaseURLDerivesNativeRouting(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	nativeHit := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/openai/") {
			t.Fatalf("native routing used OpenAI-compatible path %q", r.URL.Path)
		}
		nativeHit = true
		if r.URL.Path != "/v1beta/models/gemini-2.5-flash:generateContent" {
			t.Errorf("native path = %q, want /v1beta/models/gemini-2.5-flash:generateContent", r.URL.Path)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "test-api-key" {
			t.Errorf("x-goog-api-key = %q, want test-api-key", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty for native Gemini API", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"responseId": "gemini-native-baseurl",
			"candidates": [{
				"content": {"role": "model", "parts": [{"text": "ok"}]},
				"finishReason": "STOP"
			}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL + "/v1beta/openai")

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gemini-2.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil || resp.ID != "gemini-native-baseurl" {
		t.Fatalf("response = %+v, want native response", resp)
	}
	if !nativeHit {
		t.Fatal("native server was not called")
	}
}

func TestSetBaseURLDerivesModelsURL(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	modelsHit := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelsHit = true
		if strings.Contains(r.URL.Path, "/openai/") {
			t.Fatalf("models request used OpenAI-compatible path %q", r.URL.Path)
		}
		if r.URL.Path != "/v1beta/models" {
			t.Errorf("models path = %q, want /v1beta/models", r.URL.Path)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "test-api-key" {
			t.Errorf("x-goog-api-key = %q, want test-api-key", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"models": [{
				"name": "models/gemini-2.5-flash",
				"displayName": "Gemini 2.5 Flash",
				"supportedGenerationMethods": ["generateContent", "streamGenerateContent"],
				"inputTokenLimit": 1048576,
				"outputTokenLimit": 8192
			}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL + "/v1beta/openai")

	if provider.client.BaseURL() != server.URL+"/v1beta/openai" {
		t.Fatalf("client.BaseURL() = %q, want OpenAI-compatible base URL", provider.client.BaseURL())
	}
	if provider.modelsURL != server.URL+"/v1beta" {
		t.Fatalf("modelsURL = %q, want derived native base URL", provider.modelsURL)
	}
	if provider.nativeClient.BaseURL() != server.URL+"/v1beta" {
		t.Fatalf("nativeClient.BaseURL() = %q, want derived native base URL", provider.nativeClient.BaseURL())
	}

	resp, err := provider.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "gemini-2.5-flash" {
		t.Fatalf("models = %+v, want gemini-2.5-flash", resp.Data)
	}
	if !modelsHit {
		t.Fatal("models server was not called")
	}
}

func TestVertexNativeChatUsesOAuthAuthorization(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects/prod-ai/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent" {
			t.Errorf("Path = %q, want Vertex native generateContent endpoint", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer vertex-token" {
			t.Errorf("Authorization = %q, want Bearer vertex-token", got)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "" {
			t.Errorf("x-goog-api-key = %q, want empty for Vertex OAuth", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"responseId": "vertex-native",
			"candidates": [{
				"content": {"role": "model", "parts": [{"text": "ok"}]},
				"finishReason": "STOP"
			}]
		}`))
	}))
	defer server.Close()

	p := newVertexTestProvider(server, true)
	resp, err := p.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "google/gemini-2.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil || resp.ID != "vertex-native" {
		t.Fatalf("response = %+v, want vertex-native", resp)
	}
	if resp.Provider != "vertex" {
		t.Fatalf("provider = %q, want vertex", resp.Provider)
	}
}

func TestVertexNativeBlockedPromptUsesVertexProviderName(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects/prod-ai/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent" {
			t.Errorf("Path = %q, want Vertex native generateContent endpoint", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"responseId": "vertex-blocked",
			"promptFeedback": {
				"blockReason": "SAFETY",
				"blockReasonMessage": "unsafe prompt"
			}
		}`))
	}))
	defer server.Close()

	p := newVertexTestProvider(server, true)
	_, err := p.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "google/gemini-2.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "blocked"},
		},
	})
	if err == nil {
		t.Fatal("expected blocked prompt error, got nil")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %T %[1]v, want *core.GatewayError", err)
	}
	if gatewayErr.Provider != "vertex" {
		t.Fatalf("provider = %q, want vertex", gatewayErr.Provider)
	}
	if !strings.Contains(gatewayErr.Message, "SAFETY: unsafe prompt") {
		t.Fatalf("message = %q, want block reason", gatewayErr.Message)
	}
}

func TestVertexNativeStreamUsesVertexProviderName(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects/prod-ai/locations/us-central1/publishers/google/models/gemini-2.5-flash:streamGenerateContent" {
			t.Errorf("Path = %q, want Vertex native streamGenerateContent endpoint", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer vertex-token" {
			t.Errorf("Authorization = %q, want Bearer vertex-token", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"responseId":"vertex-stream","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}

`))
	}))
	defer server.Close()

	p := newVertexTestProvider(server, true)
	body, err := p.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "google/gemini-2.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
		StreamOptions: &core.StreamOptions{IncludeUsage: true},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("failed to read stream: %v", err)
	}
	chunks := parseOpenAIStreamChunks(t, string(raw))
	if len(chunks) == 0 {
		t.Fatalf("stream = %q, want chunks", string(raw))
	}
	for _, chunk := range chunks {
		if got := chunk["provider"]; got != "vertex" {
			t.Fatalf("provider = %#v, want vertex in stream %q", got, string(raw))
		}
	}
}

func TestVertexNativeStreamResponsesUsesVertexProviderName(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects/prod-ai/locations/us-central1/publishers/google/models/gemini-2.5-flash:streamGenerateContent" {
			t.Errorf("Path = %q, want Vertex native streamGenerateContent endpoint", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"responseId":"vertex-responses-stream","candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}

`))
	}))
	defer server.Close()

	p := newVertexTestProvider(server, true)
	body, err := p.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "google/gemini-2.5-flash",
		Input: "Hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("failed to read stream: %v", err)
	}
	stream := string(raw)
	if !strings.Contains(stream, `"provider":"vertex"`) {
		t.Fatalf("stream = %q, want vertex provider metadata", stream)
	}
}

func TestVertexOpenAICompatibleChatUsesOAuthAndGoogleModelPrefix(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "false")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects/prod-ai/locations/us-central1/endpoints/openapi/chat/completions" {
			t.Errorf("Path = %q, want Vertex OpenAI-compatible chat endpoint", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer vertex-token" {
			t.Errorf("Authorization = %q, want Bearer vertex-token", got)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "" {
			t.Errorf("x-goog-api-key = %q, want empty for Vertex OAuth", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if got := payload["model"]; got != "google/gemini-2.5-flash" {
			t.Fatalf("model = %#v, want google/gemini-2.5-flash", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "vertex-openai",
			"object": "chat.completion",
			"created": 1677652288,
			"model": "google/gemini-2.5-flash",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "ok"},
				"finish_reason": "stop"
			}]
		}`))
	}))
	defer server.Close()

	p := newVertexTestProvider(server, false)
	resp, err := p.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gemini-2.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil || resp.ID != "vertex-openai" {
		t.Fatalf("response = %+v, want vertex-openai", resp)
	}
	if resp.Provider != "vertex" {
		t.Fatalf("provider = %q, want vertex", resp.Provider)
	}
}

func TestVertexOpenAICompatibleEmbeddingsUsesOAuthAndGoogleModelPrefix(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "false")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects/prod-ai/locations/us-central1/endpoints/openapi/embeddings" {
			t.Errorf("Path = %q, want Vertex OpenAI-compatible embeddings endpoint", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer vertex-token" {
			t.Errorf("Authorization = %q, want Bearer vertex-token", got)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "" {
			t.Errorf("x-goog-api-key = %q, want empty for Vertex OAuth", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if got := payload["model"]; got != "google/text-embedding-005" {
			t.Fatalf("model = %#v, want google/text-embedding-005", got)
		}
		if got := payload["input"]; got != "text" {
			t.Fatalf("input = %#v, want text", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"object": "list",
			"model": "google/text-embedding-005",
			"data": [{
				"object": "embedding",
				"embedding": [0.1, 0.2],
				"index": 0
			}],
			"usage": {"prompt_tokens": 1, "total_tokens": 1}
		}`))
	}))
	defer server.Close()

	p := newVertexTestProvider(server, false)
	resp, err := p.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "text-embedding-005",
		Input: "text",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil || resp.Model != "google/text-embedding-005" {
		t.Fatalf("response = %+v, want google/text-embedding-005", resp)
	}
	if resp.Provider != "vertex" {
		t.Fatalf("provider = %q, want vertex", resp.Provider)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("data = %+v, want one embedding", resp.Data)
	}
	var embedding []float64
	if err := json.Unmarshal(resp.Data[0].Embedding, &embedding); err != nil {
		t.Fatalf("embedding = %s, want float array: %v", string(resp.Data[0].Embedding), err)
	}
	if len(embedding) != 2 || embedding[0] != 0.1 || embedding[1] != 0.2 {
		t.Fatalf("embedding = %v, want [0.1 0.2]", embedding)
	}
}

func TestVertexListModelsAcceptsPublisherModelsResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta1/publishers/google/models" {
			t.Errorf("Path = %q, want Vertex publisher models endpoint", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer vertex-token" {
			t.Errorf("Authorization = %q, want Bearer vertex-token", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"publisherModels": [
				{"name": "publishers/google/models/gemini-2.5-flash"},
				{"name": "publishers/google/models/text-embedding-005"},
				{"name": "publishers/google/models/imagen-4.0"}
			]
		}`))
	}))
	defer server.Close()

	p := newVertexTestProvider(server, true)
	resp, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("models = %+v, want 2 Gemini-compatible models", resp.Data)
	}
	if resp.Data[0].ID != "google/gemini-2.5-flash" || resp.Data[1].ID != "google/text-embedding-005" {
		t.Fatalf("models = %+v, want google/gemini-2.5-flash and google/text-embedding-005", resp.Data)
	}
}

func TestVertexListModelsErrorsUseVertexProviderName(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "native parse error",
			body: `{"publisherModels":"bad"}`,
		},
		{
			name: "unexpected format",
			body: `{"unexpected":true}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			p := newVertexTestProvider(server, true)
			_, err := p.ListModels(context.Background())
			if err == nil {
				t.Fatal("expected models error, got nil")
			}
			var gatewayErr *core.GatewayError
			if !errors.As(err, &gatewayErr) {
				t.Fatalf("error = %T %[1]v, want *core.GatewayError", err)
			}
			if gatewayErr.Provider != "vertex" {
				t.Fatalf("provider = %q, want vertex", gatewayErr.Provider)
			}
		})
	}
}

func newVertexTestProvider(server *httptest.Server, native bool) *Provider {
	tokenClient := googleauth.HTTPClient(server.Client(), oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: "vertex-token",
		TokenType:   "Bearer",
	}))
	p := &Provider{
		backend:      geminiBackendVertex,
		authType:     geminiAuthTypeGCPADC,
		useNativeAPI: native,
	}
	openAIBaseURL := server.URL + "/v1/projects/prod-ai/locations/us-central1/endpoints/openapi"
	nativeBaseURL := server.URL + "/v1/projects/prod-ai/locations/us-central1/publishers/google"
	modelsBaseURL := server.URL + "/v1beta1/publishers/google"
	openAICfg := llmclient.DefaultConfig("vertex", openAIBaseURL)
	nativeCfg := llmclient.DefaultConfig("vertex", nativeBaseURL)
	modelsCfg := llmclient.DefaultConfig("vertex", modelsBaseURL)
	p.client = llmclient.NewWithHTTPClient(tokenClient, openAICfg, p.setHeaders)
	p.nativeClient = llmclient.NewWithHTTPClient(tokenClient, nativeCfg, p.setNativeHeaders)
	p.modelsClient = llmclient.NewWithHTTPClient(tokenClient, modelsCfg, p.setNativeHeaders)
	p.modelsURL = modelsBaseURL
	return p
}

func TestChatCompletion(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "false")

	tests := []struct {
		name          string
		statusCode    int
		responseBody  string
		expectedError bool
		checkResponse func(*testing.T, *core.ChatResponse)
	}{
		{
			name:       "successful request",
			statusCode: http.StatusOK,
			responseBody: `{
				"id": "gemini-123",
				"object": "chat.completion",
				"created": 1677652288,
				"model": "gemini-2.0-flash",
				"choices": [{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "Hello! How can I help you today?"
					},
					"finish_reason": "stop"
				}],
				"usage": {
					"prompt_tokens": 10,
					"completion_tokens": 20,
					"total_tokens": 30
				}
			}`,
			expectedError: false,
			checkResponse: func(t *testing.T, resp *core.ChatResponse) {
				if resp.ID != "gemini-123" {
					t.Errorf("ID = %q, want %q", resp.ID, "gemini-123")
				}
				if resp.Model != "gemini-2.0-flash" {
					t.Errorf("Model = %q, want %q", resp.Model, "gemini-2.0-flash")
				}
				if resp.Provider != "gemini" {
					t.Errorf("Provider = %q, want gemini", resp.Provider)
				}
				if len(resp.Choices) != 1 {
					t.Fatalf("len(Choices) = %d, want 1", len(resp.Choices))
				}
				if resp.Choices[0].Message.Content != "Hello! How can I help you today?" {
					t.Errorf("Message content = %q, want %q", resp.Choices[0].Message.Content, "Hello! How can I help you today?")
				}
				if resp.Usage.PromptTokens != 10 {
					t.Errorf("PromptTokens = %d, want 10", resp.Usage.PromptTokens)
				}
				if resp.Usage.CompletionTokens != 20 {
					t.Errorf("CompletionTokens = %d, want 20", resp.Usage.CompletionTokens)
				}
				if resp.Usage.TotalTokens != 30 {
					t.Errorf("TotalTokens = %d, want 30", resp.Usage.TotalTokens)
				}
			},
		},
		{
			name:          "API error",
			statusCode:    http.StatusUnauthorized,
			responseBody:  `{"error": {"message": "Invalid API key"}}`,
			expectedError: true,
		},
		{
			name:          "rate limit error",
			statusCode:    http.StatusTooManyRequests,
			responseBody:  `{"error": {"message": "Rate limit exceeded"}}`,
			expectedError: true,
		},
		{
			name:          "server error",
			statusCode:    http.StatusInternalServerError,
			responseBody:  `{"error": {"message": "Internal server error"}}`,
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("Content-Type = %q, want %q", r.Header.Get("Content-Type"), "application/json")
				}
				authHeader := r.Header.Get("Authorization")
				if !strings.HasPrefix(authHeader, "Bearer ") {
					t.Errorf("Authorization header should start with 'Bearer '")
				}

				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("failed to read request body: %v", err)
				}
				var req core.ChatRequest
				if err := json.Unmarshal(body, &req); err != nil {
					t.Fatalf("failed to unmarshal request: %v", err)
				}

				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			req := &core.ChatRequest{
				Model: "gemini-2.0-flash",
				Messages: []core.Message{
					{Role: "user", Content: "Hello"},
				},
			}

			resp, err := provider.ChatCompletion(context.Background(), req)

			if tt.expectedError {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tt.checkResponse != nil {
					tt.checkResponse(t, resp)
				}
			}
		})
	}
}

func TestChatCompletion_UsesNativeGenerateContentByDefault(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Method = %q, want %q", r.Method, http.MethodPost)
		}
		if r.URL.Path != "/models/gemini-2.5-flash:generateContent" {
			t.Errorf("Path = %q, want native generateContent endpoint", r.URL.Path)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "test-api-key" {
			t.Errorf("x-goog-api-key = %q, want test-api-key", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty for native Gemini API", got)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}
		if _, ok := payload["messages"]; ok {
			t.Fatal("native request should not contain OpenAI messages")
		}
		if _, ok := payload["contents"]; !ok {
			t.Fatal("native request should contain contents")
		}
		generationConfig, ok := payload["generationConfig"].(map[string]any)
		if !ok {
			t.Fatalf("generationConfig = %#v, want object", payload["generationConfig"])
		}
		if got := generationConfig["maxOutputTokens"]; got != float64(128) {
			t.Fatalf("maxOutputTokens = %#v, want 128", got)
		}
		if _, ok := payload["system_instruction"]; !ok {
			t.Fatal("system_instruction missing")
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"responseId": "gemini-native-123",
			"candidates": [{
				"index": 0,
				"content": {"role": "model", "parts": [{"text": "Hello from native Gemini"}]},
				"finishReason": "STOP"
			}],
			"usageMetadata": {
				"promptTokenCount": 7,
				"candidatesTokenCount": 5,
				"totalTokenCount": 12
			}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	maxTokens := 128
	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:     "gemini-2.5-flash",
		MaxTokens: &maxTokens,
		Messages: []core.Message{
			{Role: "system", Content: "Be concise."},
			{Role: "user", Content: "Hello"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ID != "gemini-native-123" {
		t.Fatalf("ID = %q, want gemini-native-123", resp.ID)
	}
	if resp.Provider != "gemini" {
		t.Fatalf("Provider = %q, want gemini", resp.Provider)
	}
	if got := resp.Choices[0].Message.Content; got != "Hello from native Gemini" {
		t.Fatalf("content = %q, want native text", got)
	}
	if got := resp.Choices[0].FinishReason; got != "stop" {
		t.Fatalf("finish_reason = %q, want stop", got)
	}
	if resp.Usage.TotalTokens != 12 {
		t.Fatalf("TotalTokens = %d, want 12", resp.Usage.TotalTokens)
	}
}

func TestChatCompletion_NativeUsageMetadata(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"responseId": "gemini-native-usage",
			"candidates": [{
				"index": 0,
				"content": {"role": "model", "parts": [{"text": "Done"}]},
				"finishReason": "STOP"
			}],
			"usageMetadata": {
				"promptTokenCount": 100,
				"cachedContentTokenCount": 40,
				"candidatesTokenCount": 20,
				"thoughtsTokenCount": 7,
				"totalTokenCount": 127,
				"promptTokensDetails": [
					{"modality": "TEXT", "tokenCount": 60},
					{"modality": "AUDIO", "tokenCount": 40}
				],
				"candidatesTokensDetails": [
					{"modality": "AUDIO", "tokenCount": 5}
				]
			}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gemini-2.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Usage.PromptTokens != 100 {
		t.Fatalf("PromptTokens = %d, want 100", resp.Usage.PromptTokens)
	}
	if resp.Usage.CompletionTokens != 27 {
		t.Fatalf("CompletionTokens = %d, want candidates + thoughts = 27", resp.Usage.CompletionTokens)
	}
	if resp.Usage.TotalTokens != 127 {
		t.Fatalf("TotalTokens = %d, want 127", resp.Usage.TotalTokens)
	}
	if resp.Usage.PromptTokensDetails == nil || resp.Usage.PromptTokensDetails.CachedTokens != 40 || resp.Usage.PromptTokensDetails.AudioTokens != 40 {
		t.Fatalf("PromptTokensDetails = %+v, want cached=40 audio=40", resp.Usage.PromptTokensDetails)
	}
	if resp.Usage.CompletionTokensDetails == nil || resp.Usage.CompletionTokensDetails.ReasoningTokens != 7 || resp.Usage.CompletionTokensDetails.AudioTokens != 5 {
		t.Fatalf("CompletionTokensDetails = %+v, want reasoning=7 audio=5", resp.Usage.CompletionTokensDetails)
	}
	if resp.Usage.RawUsage["prompt_cached_tokens"] != 40 {
		t.Fatalf("RawUsage[prompt_cached_tokens] = %#v, want 40", resp.Usage.RawUsage["prompt_cached_tokens"])
	}
	if resp.Usage.RawUsage["completion_reasoning_tokens"] != 7 {
		t.Fatalf("RawUsage[completion_reasoning_tokens] = %#v, want 7", resp.Usage.RawUsage["completion_reasoning_tokens"])
	}
}

func TestCopyJSONNumberAcceptsOnlyNumericValues(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantSet bool
		want    float64
	}{
		{name: "number", raw: `42`, wantSet: true, want: 42},
		{name: "numeric string", raw: `"42.5"`, wantSet: true, want: 42.5},
		{name: "object", raw: `{"value":42}`, wantSet: false},
		{name: "array", raw: `[42]`, wantSet: false},
		{name: "boolean", raw: `true`, wantSet: false},
		{name: "non numeric string", raw: `"fast"`, wantSet: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := map[string]any{}
			copyJSONNumber(json.RawMessage(tt.raw), cfg, "value")

			got, ok := cfg["value"]
			if ok != tt.wantSet {
				t.Fatalf("cfg[value] set = %v, want %v; cfg = %#v", ok, tt.wantSet, cfg)
			}
			if !tt.wantSet {
				return
			}
			gotFloat, ok := got.(float64)
			if !ok {
				t.Fatalf("cfg[value] = %T(%[1]v), want float64", got)
			}
			if gotFloat != tt.want {
				t.Fatalf("cfg[value] = %v, want %v", gotFloat, tt.want)
			}
		})
	}
}

func TestChatCompletion_NativeBlockedPromptReturnsError(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"responseId": "gemini-blocked",
			"promptFeedback": {
				"blockReason": "SAFETY",
				"blockReasonMessage": "unsafe prompt"
			}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gemini-2.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "blocked"},
		},
	})
	if err == nil {
		t.Fatal("expected blocked prompt error, got nil")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %T %[1]v, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeProvider {
		t.Fatalf("error type = %q, want provider_error", gatewayErr.Type)
	}
	if !strings.Contains(gatewayErr.Message, "SAFETY: unsafe prompt") {
		t.Fatalf("message = %q, want block reason", gatewayErr.Message)
	}
}

func TestChatCompletion_NativeRejectsRemoteImageURL(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gemini-2.5-flash",
		Messages: []core.Message{{
			Role: "user",
			Content: []core.ContentPart{
				{Type: "text", Text: "Describe the image."},
				{Type: "image_url", ImageURL: &core.ImageURLContent{URL: "https://example.com/image.png"}},
			},
		}},
	})
	if err == nil {
		t.Fatal("expected remote image_url error, got nil")
	}
	var gatewayErr *core.GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error = %T %[1]v, want *core.GatewayError", err)
	}
	if gatewayErr.Type != core.ErrorTypeInvalidRequest {
		t.Fatalf("error type = %q, want invalid_request_error", gatewayErr.Type)
	}
	if !strings.Contains(gatewayErr.Message, "supports only data: URLs") {
		t.Fatalf("message = %q, want data URL guidance", gatewayErr.Message)
	}
}

func TestChatCompletion_NativeFunctionCallTranslation(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}
		tools, ok := payload["tools"].([]any)
		if !ok || len(tools) != 1 {
			t.Fatalf("tools = %#v, want one Gemini tool", payload["tools"])
		}
		toolConfig, ok := payload["toolConfig"].(map[string]any)
		if !ok {
			t.Fatalf("toolConfig = %#v, want object", payload["toolConfig"])
		}
		functionConfig := toolConfig["functionCallingConfig"].(map[string]any)
		if functionConfig["mode"] != "ANY" {
			t.Fatalf("tool mode = %#v, want ANY", functionConfig["mode"])
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"responseId": "gemini-tools-123",
			"candidates": [{
				"content": {"role": "model", "parts": [{
					"functionCall": {
						"id": "call_native",
						"name": "lookup_weather",
						"args": {"city": "Warsaw"}
					}
				}]},
				"finishReason": "STOP"
			}]
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gemini-2.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "Weather?"},
		},
		Tools: []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        "lookup_weather",
				"description": "Look up weather",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"city": map[string]any{"type": "string"},
					},
					"required": []any{"city"},
				},
			},
		}},
		ToolChoice: map[string]any{
			"type":     "function",
			"function": map[string]any{"name": "lookup_weather"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := resp.Choices[0].FinishReason; got != "tool_calls" {
		t.Fatalf("finish_reason = %q, want tool_calls", got)
	}
	if len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.Choices[0].Message.ToolCalls))
	}
	call := resp.Choices[0].Message.ToolCalls[0]
	if call.ID != "call_native" || call.Function.Name != "lookup_weather" {
		t.Fatalf("tool call = %+v, want native function call", call)
	}
	if call.Function.Arguments != `{"city":"Warsaw"}` {
		t.Fatalf("arguments = %q, want JSON object", call.Function.Arguments)
	}
}

func TestStreamChatCompletion(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "false")

	tests := []struct {
		name          string
		statusCode    int
		responseBody  string
		expectedError bool
	}{
		{
			name:       "successful streaming request",
			statusCode: http.StatusOK,
			responseBody: `data: {"id":"gemini-123","object":"chat.completion.chunk","created":1677652288,"model":"gemini-2.0-flash","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"gemini-123","object":"chat.completion.chunk","created":1677652288,"model":"gemini-2.0-flash","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":null}]}

data: [DONE]
`,
			expectedError: false,
		},
		{
			name:          "API error",
			statusCode:    http.StatusUnauthorized,
			responseBody:  `{"error": {"message": "Invalid API key"}}`,
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("Content-Type = %q, want %q", r.Header.Get("Content-Type"), "application/json")
				}
				authHeader := r.Header.Get("Authorization")
				if !strings.HasPrefix(authHeader, "Bearer ") {
					t.Errorf("Authorization header should start with 'Bearer '")
				}

				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("failed to read request body: %v", err)
				}
				var req core.ChatRequest
				if err := json.Unmarshal(body, &req); err != nil {
					t.Fatalf("failed to unmarshal request: %v", err)
				}
				if !req.Stream {
					t.Error("Stream should be true in request")
				}

				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
			provider.SetBaseURL(server.URL)

			req := &core.ChatRequest{
				Model: "gemini-2.0-flash",
				Messages: []core.Message{
					{Role: "user", Content: "Hello"},
				},
			}

			body, err := provider.StreamChatCompletion(context.Background(), req)

			if tt.expectedError {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if body == nil {
					t.Fatal("body should not be nil")
				}
				defer func() { _ = body.Close() }()

				respBody, err := io.ReadAll(body)
				if err != nil {
					t.Fatalf("failed to read response body: %v", err)
				}
				if string(respBody) != tt.responseBody {
					t.Errorf("response body = %q, want %q", string(respBody), tt.responseBody)
				}
			}
		})
	}
}

func TestStreamChatCompletion_UsesNativeStreamByDefault(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models/gemini-2.5-flash:streamGenerateContent" {
			t.Errorf("Path = %q, want native streamGenerateContent endpoint", r.URL.Path)
		}
		if got := r.URL.Query().Get("alt"); got != "sse" {
			t.Errorf("alt = %q, want sse", got)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "test-api-key" {
			t.Errorf("x-goog-api-key = %q, want test-api-key", got)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}
		if _, ok := payload["stream"]; ok {
			t.Fatal("native stream request should not contain OpenAI stream flag")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"responseId":"gemini-stream-123","candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]}}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":1,"totalTokenCount":5}}

data: {"responseId":"gemini-stream-123","candidates":[{"content":{"role":"model","parts":[{"text":"!"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2,"totalTokenCount":6}}

`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gemini-2.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
		StreamOptions: &core.StreamOptions{IncludeUsage: true},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("failed to read stream: %v", err)
	}
	stream := string(raw)
	if !strings.Contains(stream, `"id":"gemini-stream-123"`) {
		t.Fatalf("stream = %q, want native response id", stream)
	}
	if !strings.Contains(stream, `"content":"Hello"`) || !strings.Contains(stream, `"content":"!"`) {
		t.Fatalf("stream = %q, want converted content chunks", stream)
	}
	if !strings.Contains(stream, `"finish_reason":"stop"`) {
		t.Fatalf("stream = %q, want stop finish reason", stream)
	}
	if !strings.Contains(stream, `"usage"`) || !strings.Contains(stream, `"total_tokens":6`) {
		t.Fatalf("stream = %q, want usage chunk", stream)
	}
	usageChunks := 0
	for _, chunk := range parseOpenAIStreamChunks(t, stream) {
		if _, ok := chunk["usage"]; !ok {
			continue
		}
		usageChunks++
		choices, ok := chunk["choices"].([]any)
		if !ok {
			t.Fatalf("usage chunk choices = %T(%[1]v), want array", chunk["choices"])
		}
		if len(choices) != 0 {
			t.Fatalf("usage chunk choices = %#v, want empty choices", choices)
		}
	}
	if usageChunks != 1 {
		t.Fatalf("usage chunk count = %d, want 1 in stream %q", usageChunks, stream)
	}
	if !strings.Contains(stream, "data: [DONE]") {
		t.Fatalf("stream = %q, want [DONE]", stream)
	}
}

func parseOpenAIStreamChunks(t *testing.T, stream string) []map[string]any {
	t.Helper()

	var chunks []map[string]any
	for _, line := range strings.Split(stream, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("failed to parse stream chunk %q: %v", payload, err)
		}
		chunks = append(chunks, chunk)
	}
	return chunks
}

func TestStreamChatCompletion_NativePerChoiceState(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"responseId":"gemini-stream-choice-state","candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"id":"call_0","name":"lookup_weather","args":{"city":"Warsaw"}}}]},"finishReason":"STOP"},{"index":1,"content":{"role":"model","parts":[{"text":"plain text"}]},"finishReason":"STOP"}]}

`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gemini-2.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("failed to read stream: %v", err)
	}
	stream := string(raw)
	if got := strings.Count(stream, `"role":"assistant"`); got != 2 {
		t.Fatalf("assistant role count = %d, want 2 in stream %q", got, stream)
	}
	if got := strings.Count(stream, `"finish_reason":"tool_calls"`); got != 1 {
		t.Fatalf("tool_calls finish count = %d, want 1 in stream %q", got, stream)
	}
	if got := strings.Count(stream, `"finish_reason":"stop"`); got != 1 {
		t.Fatalf("stop finish count = %d, want 1 in stream %q", got, stream)
	}
}

func TestStreamChatCompletion_NativeBlockedPromptEmitsError(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"responseId":"gemini-stream-blocked","promptFeedback":{"blockReason":"SAFETY","blockReasonMessage":"unsafe prompt"}}

`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	body, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model: "gemini-2.5-flash",
		Messages: []core.Message{
			{Role: "user", Content: "blocked"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("failed to read stream: %v", err)
	}
	stream := string(raw)
	if !strings.Contains(stream, `"type":"provider_error"`) || !strings.Contains(stream, "Gemini blocked prompt: SAFETY: unsafe prompt") {
		t.Fatalf("stream = %q, want normalized provider error", stream)
	}
	if !strings.Contains(stream, "data: [DONE]") {
		t.Fatalf("stream = %q, want [DONE]", stream)
	}
}

func TestListModels(t *testing.T) {
	tests := []struct {
		name          string
		statusCode    int
		responseBody  string
		expectedError bool
		checkResponse func(*testing.T, *core.ModelsResponse)
	}{
		{
			name:       "successful request",
			statusCode: http.StatusOK,
			responseBody: `{
				"models": [
					{
						"name": "models/gemini-2.0-flash",
						"displayName": "Gemini 2.0 Flash",
						"description": "Fast and efficient model",
						"supportedGenerationMethods": ["generateContent", "streamGenerateContent"],
						"inputTokenLimit": 32768,
						"outputTokenLimit": 8192
					},
					{
						"name": "models/gemini-1.5-pro",
						"displayName": "Gemini 1.5 Pro",
						"description": "Advanced reasoning and complex tasks",
						"supportedGenerationMethods": ["generateContent", "streamGenerateContent"],
						"inputTokenLimit": 1048576,
						"outputTokenLimit": 8192
					},
					{
						"name": "models/embedding-001",
						"displayName": "Text Embedding",
						"description": "Embedding model",
						"supportedGenerationMethods": ["embedContent"],
						"inputTokenLimit": 2048,
						"outputTokenLimit": 1
					}
				]
			}`,
			expectedError: false,
			checkResponse: func(t *testing.T, resp *core.ModelsResponse) {
				if resp.Object != "list" {
					t.Errorf("Object = %q, want %q", resp.Object, "list")
				}
				if len(resp.Data) != 2 {
					t.Fatalf("len(Data) = %d, want 2", len(resp.Data))
				}
				if resp.Data[0].ID != "gemini-2.0-flash" {
					t.Errorf("Data[0].ID = %q, want %q", resp.Data[0].ID, "gemini-2.0-flash")
				}
				if resp.Data[0].OwnedBy != "google" {
					t.Errorf("Data[0].OwnedBy = %q, want %q", resp.Data[0].OwnedBy, "google")
				}
				if resp.Data[1].ID != "gemini-1.5-pro" {
					t.Errorf("Data[1].ID = %q, want %q", resp.Data[1].ID, "gemini-1.5-pro")
				}
			},
		},
		{
			name:          "API error",
			statusCode:    http.StatusUnauthorized,
			responseBody:  `{"error": {"message": "Invalid API key"}}`,
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("Method = %q, want %q", r.Method, http.MethodGet)
				}
				if r.URL.Path != "/models" {
					t.Errorf("Path = %q, want %q", r.URL.Path, "/models")
				}

				apiKey := r.Header.Get("x-goog-api-key")
				if apiKey == "" {
					t.Error("API key should be in x-goog-api-key header")
				}

				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.responseBody))
			}))
			defer server.Close()

			provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
			provider.SetModelsURL(server.URL)

			resp, err := provider.ListModels(context.Background())

			if tt.expectedError {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tt.checkResponse != nil {
					tt.checkResponse(t, resp)
				}
			}
		})
	}
}

func TestChatCompletionWithContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		w.WriteHeader(http.StatusRequestTimeout)
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := &core.ChatRequest{
		Model: "gemini-2.0-flash",
		Messages: []core.Message{
			{Role: "user", Content: "Hello"},
		},
	}

	_, err := provider.ChatCompletion(ctx, req)
	if err == nil {
		t.Error("expected error when context is cancelled, got nil")
	}
}

func TestResponses(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "false")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"id": "gemini-123",
			"object": "chat.completion",
			"created": 1677652288,
			"model": "gemini-2.0-flash",
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "Hello! How can I help you today?"
				},
				"finish_reason": "stop"
			}],
			"usage": {
				"prompt_tokens": 10,
				"completion_tokens": 20,
				"total_tokens": 30
			}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	req := &core.ResponsesRequest{
		Model: "gemini-2.0-flash",
		Input: "Hello",
	}

	resp, err := provider.Responses(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID != "gemini-123" {
		t.Errorf("ID = %q, want %q", resp.ID, "gemini-123")
	}
	if resp.Object != "response" {
		t.Errorf("Object = %q, want %q", resp.Object, "response")
	}
	if resp.Model != "gemini-2.0-flash" {
		t.Errorf("Model = %q, want %q", resp.Model, "gemini-2.0-flash")
	}
}

func TestResponses_Native(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Method = %q, want %q", r.Method, http.MethodPost)
		}
		if r.URL.Path != "/models/gemini-2.5-flash:generateContent" {
			t.Errorf("Path = %q, want native generateContent endpoint", r.URL.Path)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "test-api-key" {
			t.Errorf("x-goog-api-key = %q, want test-api-key", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty for native Gemini API", got)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}
		if _, ok := payload["messages"]; ok {
			t.Fatal("native request should not contain OpenAI messages")
		}
		systemInstruction, ok := payload["system_instruction"].(map[string]any)
		if !ok {
			t.Fatalf("system_instruction = %#v, want object", payload["system_instruction"])
		}
		systemParts, ok := systemInstruction["parts"].([]any)
		if !ok || len(systemParts) != 1 || systemParts[0].(map[string]any)["text"] != "Be concise." {
			t.Fatalf("system_instruction.parts = %#v, want instruction text", systemInstruction["parts"])
		}
		contents, ok := payload["contents"].([]any)
		if !ok || len(contents) != 1 {
			t.Fatalf("contents = %#v, want one native content", payload["contents"])
		}
		firstContent := contents[0].(map[string]any)
		if firstContent["role"] != "user" {
			t.Fatalf("contents[0].role = %#v, want user", firstContent["role"])
		}
		parts := firstContent["parts"].([]any)
		if len(parts) != 1 || parts[0].(map[string]any)["text"] != "Hello" {
			t.Fatalf("contents[0].parts = %#v, want user text", firstContent["parts"])
		}
		generationConfig, ok := payload["generationConfig"].(map[string]any)
		if !ok {
			t.Fatalf("generationConfig = %#v, want object", payload["generationConfig"])
		}
		if got := generationConfig["maxOutputTokens"]; got != float64(64) {
			t.Fatalf("maxOutputTokens = %#v, want 64", got)
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"responseId": "gemini-native-response",
			"candidates": [{
				"content": {"role": "model", "parts": [{"text": "Native response"}]},
				"finishReason": "STOP"
			}],
			"usageMetadata": {
				"promptTokenCount": 5,
				"candidatesTokenCount": 3,
				"totalTokenCount": 8
			}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	maxOutputTokens := 64
	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model:           "gemini-2.5-flash",
		Instructions:    "Be concise.",
		Input:           "Hello",
		MaxOutputTokens: &maxOutputTokens,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.ID != "gemini-native-response" {
		t.Fatalf("ID = %q, want gemini-native-response", resp.ID)
	}
	if resp.Object != "response" {
		t.Fatalf("Object = %q, want response", resp.Object)
	}
	if resp.Model != "gemini-2.5-flash" {
		t.Fatalf("Model = %q, want gemini-2.5-flash", resp.Model)
	}
	if resp.Provider != "gemini" {
		t.Fatalf("Provider = %q, want gemini", resp.Provider)
	}
	if len(resp.Output) != 1 || len(resp.Output[0].Content) != 1 || resp.Output[0].Content[0].Text != "Native response" {
		t.Fatalf("Output = %+v, want native response text", resp.Output)
	}
}

func TestStreamResponses(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "false")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"id":"gemini-123","object":"chat.completion.chunk","created":1677652288,"model":"gemini-2.0-flash","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: [DONE]
`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetBaseURL(server.URL)

	req := &core.ResponsesRequest{
		Model: "gemini-2.0-flash",
		Input: "Hello",
	}

	body, err := provider.StreamResponses(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body == nil {
		t.Fatal("body should not be nil")
	}
	defer func() { _ = body.Close() }()

	respBody, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	responseStr := string(respBody)
	if !strings.Contains(responseStr, "response.created") {
		t.Error("response should contain response.created event")
	}
	if !strings.Contains(responseStr, "[DONE]") {
		t.Error("response should end with [DONE]")
	}
}

func TestStreamResponses_Native(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "true")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("Method = %q, want %q", r.Method, http.MethodPost)
		}
		if r.URL.Path != "/models/gemini-2.5-flash:streamGenerateContent" {
			t.Errorf("Path = %q, want native streamGenerateContent endpoint", r.URL.Path)
		}
		if got := r.URL.Query().Get("alt"); got != "sse" {
			t.Errorf("alt = %q, want sse", got)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "test-api-key" {
			t.Errorf("x-goog-api-key = %q, want test-api-key", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty for native Gemini API", got)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("failed to unmarshal request: %v", err)
		}
		if _, ok := payload["stream"]; ok {
			t.Fatal("native stream request should not contain OpenAI stream flag")
		}
		systemInstruction, ok := payload["system_instruction"].(map[string]any)
		if !ok {
			t.Fatalf("system_instruction = %#v, want object", payload["system_instruction"])
		}
		systemParts := systemInstruction["parts"].([]any)
		if len(systemParts) != 1 || systemParts[0].(map[string]any)["text"] != "Be concise." {
			t.Fatalf("system_instruction.parts = %#v, want instruction text", systemInstruction["parts"])
		}
		contents, ok := payload["contents"].([]any)
		if !ok || len(contents) != 1 {
			t.Fatalf("contents = %#v, want one native content", payload["contents"])
		}
		parts := contents[0].(map[string]any)["parts"].([]any)
		if len(parts) != 1 || parts[0].(map[string]any)["text"] != "Hello" {
			t.Fatalf("contents[0].parts = %#v, want user text", contents[0].(map[string]any)["parts"])
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"responseId":"gemini-native-stream-response","candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]}}]}

data: {"responseId":"gemini-native-stream-response","candidates":[{"content":{"role":"model","parts":[{"text":"!"}]},"finishReason":"STOP"}]}

`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("test-api-key", nil, llmclient.Hooks{})
	provider.SetModelsURL(server.URL)

	body, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model:        "gemini-2.5-flash",
		Instructions: "Be concise.",
		Input:        "Hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body == nil {
		t.Fatal("body should not be nil")
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("failed to read response stream: %v", err)
	}
	stream := string(raw)
	if !strings.Contains(stream, "response.created") {
		t.Fatalf("stream = %q, want response.created event", stream)
	}
	if !strings.Contains(stream, "response.output_text.delta") {
		t.Fatalf("stream = %q, want response.output_text.delta event", stream)
	}
	if !strings.Contains(stream, `"delta":"Hello"`) || !strings.Contains(stream, `"delta":"!"`) {
		t.Fatalf("stream = %q, want normalized text deltas", stream)
	}
	if !strings.Contains(stream, "data: [DONE]") {
		t.Fatalf("stream = %q, want [DONE]", stream)
	}
}
