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
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// slashedModel is an Eden AI model ID in provider/model notation. Every
// request test uses it so a regression that splits, rewrites, or filters the
// provider prefix fails immediately.
const slashedModel = "openai/gpt-4"

// TestNew_ReturnsProvider asserts that New returns a non-nil *Provider whose
// composed CompatibleProvider is wired up.
func TestNew_ReturnsProvider(t *testing.T) {
	provider := New(providers.ProviderConfig{APIKey: "test-api-key"}, providers.ProviderOptions{})

	require.NotNil(t, provider, "provider should not be nil")

	concrete, ok := provider.(*Provider)
	require.True(t, ok, "New() returned %T, want *edenai.Provider", provider)
	assert.NotNil(t, concrete.compat, "composed CompatibleProvider should not be nil")
}

// TestNew_DefaultsBaseURL asserts that a config without an explicit base URL
// falls back to Eden's public endpoint rather than an empty target.
func TestNew_DefaultsBaseURL(t *testing.T) {
	provider, ok := New(providers.ProviderConfig{APIKey: "test-api-key"}, providers.ProviderOptions{}).(*Provider)
	require.True(t, ok, "New() did not return *edenai.Provider")
	assert.Equal(t, defaultBaseURL, provider.GetBaseURL())
}

// TestNew_HonoursConfiguredBaseURL asserts that EDENAI_BASE_URL (surfaced here
// as ProviderConfig.BaseURL) overrides the default.
func TestNew_HonoursConfiguredBaseURL(t *testing.T) {
	const custom = "https://eden.internal.example/v3"
	provider, ok := New(providers.ProviderConfig{APIKey: "k", BaseURL: custom}, providers.ProviderOptions{}).(*Provider)
	require.True(t, ok, "New() did not return *edenai.Provider")
	assert.Equal(t, custom, provider.GetBaseURL())
}

// TestChatCompatibleContract checks the contract every provider built on the
// shared OpenAI-compatible adapter shares: registration metadata, constructor
// safety with a nil client and zero hooks, the injected transport being the
// one requests travel through, and chat, streaming, model listing, Responses,
// and embeddings reaching the expected upstream paths with the bearer token
// attached. Responses are expected translated through chat completions, since
// Eden's own /responses route is a different API (see the package comment).
func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           providerType,
		DefaultBaseURL: defaultBaseURL,
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			return newTestProvider(apiKey, baseURL, client, hooks)
		},
		Embeddings: true,
	})
}

// TestRegistration_TypeAndDiscovery asserts the Registration struct exposes the
// expected type, New function, and default base URL. The type spelling also
// fixes the env prefix the generic discovery derives (EDENAI_API_KEY,
// EDENAI_BASE_URL) and the provider gate in internal/usage, so it is asserted
// exactly.
func TestRegistration_TypeAndDiscovery(t *testing.T) {
	assert.Equal(t, "edenai", Registration.Type)
	assert.NotNil(t, Registration.New, "Registration.New should not be nil")
	assert.Equal(t, "https://api.edenai.run/v3", Registration.Discovery.DefaultBaseURL)
	assert.NotNil(t, Registration.PassthroughSemanticEnricher, "Registration.PassthroughSemanticEnricher should not be nil")
	assert.False(t, Registration.Discovery.RequireBaseURL, "Discovery.RequireBaseURL should be false: Eden has a public default endpoint")
	assert.False(t, Registration.Discovery.AllowAPIKeyless, "Discovery.AllowAPIKeyless should be false: Eden always requires an API key")
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

	provider := newTestProvider("edenai-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    slashedModel,
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	require.Equal(t, "/chat/completions", gotPath)
	require.Equal(t, "Bearer edenai-key", gotAuth)
	require.Equal(t, slashedModel, gotBody["model"], "provider/model IDs must pass through unchanged")
	require.Equal(t, slashedModel, resp.Model)
	require.Len(t, resp.Choices, 1, "unexpected response: %+v", resp)
	require.Equal(t, "hello", resp.Choices[0].Message.Content, "unexpected response: %+v", resp)
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
	require.NoError(t, json.Unmarshal([]byte(raw), &req))

	provider := newTestProvider("edenai-key", server.URL, server.Client(), llmclient.Hooks{})
	_, err := provider.ChatCompletion(context.Background(), &req)
	require.NoError(t, err)

	fallbacks, ok := gotBody["fallbacks"].([]any)
	require.True(t, ok, "fallbacks = %#v, want Eden fallback list forwarded unchanged", gotBody["fallbacks"])
	require.Equal(t, []any{"anthropic/claude-sonnet-latest"}, fallbacks, "want Eden fallback list forwarded unchanged")
	routing, ok := gotBody["routing"].(map[string]any)
	require.True(t, ok, "routing = %#v, want Eden routing object forwarded unchanged", gotBody["routing"])
	require.Equal(t, "cost", routing["strategy"], "want Eden routing object forwarded unchanged")
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

	provider := newTestProvider("edenai-key", server.URL, server.Client(), llmclient.Hooks{})
	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    slashedModel,
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	defer stream.Close()
	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.Equal(t, "/chat/completions", gotPath)
	require.Equal(t, "Bearer edenai-key", gotAuth)
	require.Equal(t, slashedModel, gotBody["model"], "stream request body = %#v", gotBody)
	streaming, _ := gotBody["stream"].(bool)
	require.True(t, streaming, "stream request body = %#v, want stream=true", gotBody)
	require.Contains(t, string(body), "data: [DONE]", "stream body = %q, want SSE terminator", body)
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

	provider := newTestProvider("edenai-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "openai/text-embedding-3-small",
		Input: "hello",
	})
	require.NoError(t, err)
	require.Equal(t, "/embeddings", gotPath)
	require.Equal(t, "Bearer edenai-key", gotAuth)
	require.Equal(t, "openai/text-embedding-3-small", gotBody["model"], "want provider/model ID forwarded unchanged")
	// core.EmbeddingRequest.Provider is a gateway routing hint that the router
	// clears on the forwarded clone (providers.forwardEmbeddingRequest) before
	// any provider sees it, so it must not appear on the wire. Eden dispatches
	// the request it is handed, exactly as the shared
	// CompatibleProvider.Embeddings helper does.
	assert.NotContains(t, gotBody, "provider", "request body carried the gateway-only provider field: %#v", gotBody)
	require.Len(t, resp.Data, 1, "embedding data = %+v, want one populated vector", resp.Data)
	require.Equal(t, 0, resp.Data[0].Index, "embedding data = %+v, want one populated vector", resp.Data)
	require.NotEmpty(t, resp.Data[0].Embedding, "embedding data = %+v, want one populated vector", resp.Data)
	assert.Equal(t, "openai/text-embedding-3-small", resp.Model)
	assert.Equal(t, 4, resp.Usage.PromptTokens, "usage = %+v, want prompt 4 / total 4", resp.Usage)
	assert.Equal(t, 4, resp.Usage.TotalTokens, "usage = %+v, want prompt 4 / total 4", resp.Usage)
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

	provider := newTestProvider("edenai-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: slashedModel,
		Input: "hi",
	})
	require.NoError(t, err)
	require.Equal(t, []string{"/chat/completions"}, paths, "upstream paths = %v, want exactly [/chat/completions]", paths)
	for _, path := range paths {
		require.NotContains(t, path, "/responses", "request reached %q; Eden's native /responses is not the OpenAI Responses API and must never be used", path)
	}
	require.Equal(t, slashedModel, gotBody.Model)
	require.Equal(t, "response", resp.Object, "response metadata = object %q status %q, want response/completed", resp.Object, resp.Status)
	require.Equal(t, "completed", resp.Status, "response metadata = object %q status %q, want response/completed", resp.Object, resp.Status)
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

	provider := newTestProvider("edenai-key", server.URL, server.Client(), llmclient.Hooks{})
	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: slashedModel,
		Input: "hi",
	})
	require.NoError(t, err)
	defer stream.Close()
	_, err = io.ReadAll(stream)
	require.NoError(t, err)
	require.Equal(t, []string{"/chat/completions"}, paths, "upstream paths = %v, want exactly [/chat/completions]", paths)
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

	provider := newTestProvider("edenai-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "chat/completions",
		Body:     io.NopCloser(strings.NewReader(`{"model":"openai/gpt-4"}`)),
	})
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, "/chat/completions", gotPath)
	require.Equal(t, "Bearer edenai-key", gotAuth)
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

// TestProvider_DoesNotExposeOptionalOpenAICompatibleInterfaces guards the
// composition: *Provider delegates only the methods Eden implements, so it
// must not satisfy the optional native interfaces. Eden documents no
// OpenAI-shaped batches API, and its files and audio surfaces are unverified,
// so advertising those capabilities to the router would promise what the
// upstream cannot honour. This matters more under composition than it did
// under embedding: adding a delegation by mistake is all it would take.
func TestProvider_DoesNotExposeOptionalOpenAICompatibleInterfaces(t *testing.T) {
	provider := newTestProvider("edenai-key", "", nil, llmclient.Hooks{})

	providertest.AssertNoNativeSurfaces(t, provider)
	_, ok := any(provider).(core.ImageProvider)
	assert.False(t, ok, "edenai provider should not implement image provider")
}
