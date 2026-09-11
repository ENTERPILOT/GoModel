package edenai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
)

// slashedModel is an Eden AI model ID in provider/model notation. Every
// request test uses it so a regression that splits, rewrites, or filters the
// provider prefix fails immediately.
const slashedModel = "openai/gpt-4"

// TestNew_ReturnsProvider asserts that New returns a non-nil *Provider whose
// composed CompatibleProvider is wired up.
func TestNew_ReturnsProvider(t *testing.T) {
	provider := New(providers.ProviderConfig{APIKey: "test-api-key"}, providers.ProviderOptions{})

	if provider == nil {
		t.Fatal("provider should not be nil")
	}

	concrete, ok := provider.(*Provider)
	if !ok {
		t.Fatalf("New() returned %T, want *edenai.Provider", provider)
	}
	if concrete.compat == nil {
		t.Error("composed CompatibleProvider should not be nil")
	}
}

// TestNew_DefaultsBaseURL asserts that a config without an explicit base URL
// falls back to Eden's public endpoint rather than an empty target.
func TestNew_DefaultsBaseURL(t *testing.T) {
	provider, ok := New(providers.ProviderConfig{APIKey: "test-api-key"}, providers.ProviderOptions{}).(*Provider)
	if !ok {
		t.Fatal("New() did not return *edenai.Provider")
	}
	if got := provider.GetBaseURL(); got != defaultBaseURL {
		t.Errorf("GetBaseURL() = %q, want %q", got, defaultBaseURL)
	}
}

// TestNew_HonoursConfiguredBaseURL asserts that EDENAI_BASE_URL (surfaced here
// as ProviderConfig.BaseURL) overrides the default.
func TestNew_HonoursConfiguredBaseURL(t *testing.T) {
	const custom = "https://eden.internal.example/v3"
	provider, ok := New(providers.ProviderConfig{APIKey: "k", BaseURL: custom}, providers.ProviderOptions{}).(*Provider)
	if !ok {
		t.Fatal("New() did not return *edenai.Provider")
	}
	if got := provider.GetBaseURL(); got != custom {
		t.Errorf("GetBaseURL() = %q, want %q", got, custom)
	}
}

// TestNewWithHTTPClient_ReturnsProvider asserts the explicit HTTP-client constructor
// returns a valid Provider.
func TestNewWithHTTPClient_ReturnsProvider(t *testing.T) {
	provider := NewWithHTTPClient("test-api-key", "http://example.invalid", &http.Client{}, llmclient.Hooks{})

	if provider == nil {
		t.Fatal("provider should not be nil")
	}
	if provider.compat == nil {
		t.Error("composed CompatibleProvider should not be nil")
	}
}

// TestNewWithHTTPClient_NilHTTPClientDoesNotPanic asserts that passing nil for the
// HTTP client falls back to http.DefaultClient without panicking.
func TestNewWithHTTPClient_NilHTTPClientDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewWithHTTPClient(nil, ...) panicked: %v", r)
		}
	}()
	provider := NewWithHTTPClient("test-api-key", "http://example.invalid", nil, llmclient.Hooks{})
	if provider == nil {
		t.Fatal("provider should not be nil")
	}
}

// TestNewWithHTTPClient_ZeroHooksDoesNotPanic asserts that the hooks argument can be
// an empty struct (no hooks registered) without panicking.
func TestNewWithHTTPClient_ZeroHooksDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewWithHTTPClient(..., llmclient.Hooks{}) panicked: %v", r)
		}
	}()
	provider := NewWithHTTPClient("test-api-key", "http://example.invalid", &http.Client{}, llmclient.Hooks{})
	if provider == nil {
		t.Fatal("provider should not be nil")
	}
}

// TestRegistration_TypeAndDiscovery asserts the Registration struct exposes the
// expected type, New function, and default base URL. The type spelling also
// fixes the env prefix the generic discovery derives (EDENAI_API_KEY,
// EDENAI_BASE_URL) and the provider gate in internal/usage, so it is asserted
// exactly.
func TestRegistration_TypeAndDiscovery(t *testing.T) {
	if Registration.Type != "edenai" {
		t.Errorf("Registration.Type = %q, want %q", Registration.Type, "edenai")
	}
	if Registration.New == nil {
		t.Error("Registration.New should not be nil")
	}
	want := "https://api.edenai.run/v3"
	if Registration.Discovery.DefaultBaseURL != want {
		t.Errorf("Registration.Discovery.DefaultBaseURL = %q, want %q", Registration.Discovery.DefaultBaseURL, want)
	}
	if Registration.PassthroughSemanticEnricher == nil {
		t.Error("Registration.PassthroughSemanticEnricher should not be nil")
	}
	if Registration.Discovery.RequireBaseURL {
		t.Error("Discovery.RequireBaseURL should be false: Eden has a public default endpoint")
	}
	if Registration.Discovery.AllowAPIKeyless {
		t.Error("Discovery.AllowAPIKeyless should be false: Eden always requires an API key")
	}
}

// TestProvider_ImplementsCoreProvider is a compile-time check that *Provider
// satisfies the core.Provider interface used by the factory.
func TestProvider_ImplementsCoreProvider(t *testing.T) {
	var _ core.Provider = (*Provider)(nil)
}

// TestChatCompletion_UsesBearerAuthAndForwardsModel asserts that ChatCompletion
// posts to /chat/completions with the Bearer header and forwards Eden's
// provider/model ID unchanged. The bearer assertion matters more than usual
// here: CompatibleProvider sends no credential at all when SetHeaders is nil.
func TestChatCompletion_UsesBearerAuthAndForwardsModel(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			http.Error(w, "decode error", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-edenai",
			"created":1677652288,
			"model":"openai/gpt-4",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("edenai-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    slashedModel,
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer edenai-key" {
		t.Fatalf("authorization = %q, want Bearer edenai-key", gotAuth)
	}
	if gotBody["model"] != slashedModel {
		t.Fatalf("request model = %#v, want %q (provider/model IDs must pass through unchanged)", gotBody["model"], slashedModel)
	}
	if resp.Model != slashedModel {
		t.Fatalf("response model = %q, want %q", resp.Model, slashedModel)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "hello" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

// TestChatCompletion_ForwardsEdenExtraFields asserts that Eden-only request
// fields (routing, fallbacks) survive the round trip through the generic
// unknown-field mechanism, so no Eden-specific request adapter is needed.
func TestChatCompletion_ForwardsEdenExtraFields(t *testing.T) {
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			http.Error(w, "decode error", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-edenai","created":1677652288,"model":"openai/gpt-4",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	var req core.ChatRequest
	raw := `{"model":"openai/gpt-4","messages":[{"role":"user","content":"hi"}],` +
		`"fallbacks":["anthropic/claude-sonnet-latest"],"routing":{"strategy":"cost"}}`
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	provider := NewWithHTTPClient("edenai-key", server.URL, server.Client(), llmclient.Hooks{})
	if _, err := provider.ChatCompletion(context.Background(), &req); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}

	fallbacks, ok := gotBody["fallbacks"].([]any)
	if !ok || len(fallbacks) != 1 || fallbacks[0] != "anthropic/claude-sonnet-latest" {
		t.Fatalf("fallbacks = %#v, want Eden fallback list forwarded unchanged", gotBody["fallbacks"])
	}
	routing, ok := gotBody["routing"].(map[string]any)
	if !ok || routing["strategy"] != "cost" {
		t.Fatalf("routing = %#v, want Eden routing object forwarded unchanged", gotBody["routing"])
	}
}

// TestStreamChatCompletion_UsesSSE asserts that streaming requests go to
// /chat/completions with the Bearer header, set stream=true, and return SSE data
// the adapter normalizes.
func TestStreamChatCompletion_UsesSSE(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			http.Error(w, "decode error", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-edenai\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewWithHTTPClient("edenai-key", server.URL, server.Client(), llmclient.Hooks{})
	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    slashedModel,
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("StreamChatCompletion() error = %v", err)
	}
	defer stream.Close()
	body, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer edenai-key" {
		t.Fatalf("authorization = %q, want Bearer edenai-key", gotAuth)
	}
	if gotBody["model"] != slashedModel || gotBody["stream"] != true {
		t.Fatalf("stream request body = %#v", gotBody)
	}
	if !strings.Contains(string(body), "data: [DONE]") {
		t.Fatalf("stream body = %q, want SSE terminator", body)
	}
}

// TestEmbeddings_ForwardsToEmbeddingsEndpoint asserts that embeddings reach
// Eden's OpenAI-compatible /embeddings route. Unlike hetzner and kilo, Eden
// documents this endpoint, so it is served rather than rejected locally.
func TestEmbeddings_ForwardsToEmbeddingsEndpoint(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			http.Error(w, "decode error", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object":"list",
			"data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],
			"model":"openai/text-embedding-3-small",
			"usage":{"prompt_tokens":4,"total_tokens":4}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("edenai-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "openai/text-embedding-3-small",
		Input: "hello",
	})
	if err != nil {
		t.Fatalf("Embeddings() error = %v", err)
	}
	if gotPath != "/embeddings" {
		t.Fatalf("path = %q, want /embeddings", gotPath)
	}
	if gotAuth != "Bearer edenai-key" {
		t.Fatalf("authorization = %q, want Bearer edenai-key", gotAuth)
	}
	if gotBody["model"] != "openai/text-embedding-3-small" {
		t.Fatalf("request model = %#v, want provider/model ID forwarded unchanged", gotBody["model"])
	}
	// core.EmbeddingRequest.Provider is a gateway routing hint that the router
	// clears on the forwarded clone (providers.forwardEmbeddingRequest) before
	// any provider sees it, so it must not appear on the wire. Eden dispatches
	// the request it is handed, exactly as the shared
	// CompatibleProvider.Embeddings helper does.
	if _, leaked := gotBody["provider"]; leaked {
		t.Errorf("request body carried the gateway-only provider field: %#v", gotBody)
	}
	if len(resp.Data) != 1 || resp.Data[0].Index != 0 || len(resp.Data[0].Embedding) == 0 {
		t.Fatalf("embedding data = %+v, want one populated vector", resp.Data)
	}
	if resp.Model != "openai/text-embedding-3-small" {
		t.Errorf("response model = %q, want openai/text-embedding-3-small", resp.Model)
	}
	if resp.Usage.PromptTokens != 4 || resp.Usage.TotalTokens != 4 {
		t.Errorf("usage = %+v, want prompt 4 / total 4", resp.Usage)
	}
}

// TestResponses_TranslatesToChatCompletions is the load-bearing test for this
// provider's architecture: Eden's own /responses route is not the OpenAI
// Responses API, so a GoModel Responses request must be translated through
// chat completions and must never reach /responses upstream.
func TestResponses_TranslatesToChatCompletions(t *testing.T) {
	var paths []string
	var gotBody struct {
		Model string `json:"model"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			http.Error(w, "decode error", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-edenai",
			"created":1677652288,
			"model":"openai/gpt-4",
			"choices":[{"index":0,"message":{"role":"assistant","content":"translated"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}
		}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("edenai-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: slashedModel,
		Input: "hi",
	})
	if err != nil {
		t.Fatalf("Responses() error = %v", err)
	}
	if len(paths) != 1 || paths[0] != "/chat/completions" {
		t.Fatalf("upstream paths = %v, want exactly [/chat/completions]", paths)
	}
	for _, path := range paths {
		if strings.Contains(path, "/responses") {
			t.Fatalf("request reached %q; Eden's native /responses is not the OpenAI Responses API and must never be used", path)
		}
	}
	if gotBody.Model != slashedModel {
		t.Fatalf("request model = %q, want %q", gotBody.Model, slashedModel)
	}
	if resp.Object != "response" || resp.Status != "completed" {
		t.Fatalf("response metadata = object %q status %q, want response/completed", resp.Object, resp.Status)
	}
}

// TestStreamResponses_TranslatesToChatCompletions asserts the streaming
// Responses surface is translated the same way, and likewise never touches
// Eden's native /responses route.
func TestStreamResponses_TranslatesToChatCompletions(t *testing.T) {
	var paths []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-edenai\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewWithHTTPClient("edenai-key", server.URL, server.Client(), llmclient.Hooks{})
	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: slashedModel,
		Input: "hi",
	})
	if err != nil {
		t.Fatalf("StreamResponses() error = %v", err)
	}
	defer stream.Close()
	if _, err := io.ReadAll(stream); err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if len(paths) != 1 || paths[0] != "/chat/completions" {
		t.Fatalf("upstream paths = %v, want exactly [/chat/completions]", paths)
	}
}

// TestPassthrough_ForwardsOpaqueRequest asserts the provider forwards an opaque
// passthrough request to the given Eden path under the gateway's own credential.
func TestPassthrough_ForwardsOpaqueRequest(t *testing.T) {
	var gotPath string
	var gotAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	provider := NewWithHTTPClient("edenai-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "chat/completions",
		Body:     io.NopCloser(strings.NewReader(`{"model":"openai/gpt-4"}`)),
	})
	if err != nil {
		t.Fatalf("Passthrough() error = %v", err)
	}
	defer resp.Body.Close()
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer edenai-key" {
		t.Fatalf("authorization = %q, want Bearer edenai-key", gotAuth)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// TestProvider_DoesNotExposeOptionalOpenAICompatibleInterfaces guards the
// composition: *Provider delegates only the methods Eden implements, so it
// must not satisfy the optional native interfaces. Eden documents no
// OpenAI-shaped batches API, and its files and audio surfaces are unverified,
// so advertising those capabilities to the router would promise what the
// upstream cannot honour. This matters more under composition than it did
// under embedding: adding a delegation by mistake is all it would take.
func TestProvider_DoesNotExposeOptionalOpenAICompatibleInterfaces(t *testing.T) {
	provider := NewWithHTTPClient("edenai-key", "", nil, llmclient.Hooks{})

	if _, ok := any(provider).(core.NativeBatchProvider); ok {
		t.Fatal("edenai provider should not implement native batch provider")
	}
	if _, ok := any(provider).(core.NativeFileProvider); ok {
		t.Fatal("edenai provider should not implement native file provider")
	}
	if _, ok := any(provider).(core.AudioProvider); ok {
		t.Fatal("edenai provider should not implement audio provider")
	}
	if _, ok := any(provider).(core.ImageProvider); ok {
		t.Fatal("edenai provider should not implement image provider")
	}
}
