package llamacpp

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ core.PassthroughProvider = (*Provider)(nil)

func TestChatCompletion_UsesOptionalBearerAuthAndChatEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		apiKey   string
		wantAuth string
	}{
		{name: "with api key", apiKey: "llamacpp-key", wantAuth: "Bearer llamacpp-key"},
		{name: "without api key"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, `{
				"id":"chatcmpl-llamacpp",
				"created":1677652288,
				"model":"gemma-3-4b-it",
				"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]
			}`)

			provider := NewWithHTTPClient(tt.apiKey, server.URL+"/v1", server.Client(), llmclient.Hooks{})

			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:    "gemma-3-4b-it",
				Messages: []core.Message{{Role: "user", Content: "hi"}},
			})
			require.NoError(t, err)
			assert.Equal(t, "gemma-3-4b-it", resp.Model)

			req := capture.Last(t)
			assert.Equal(t, "/v1/chat/completions", req.Path)
			assert.Equal(t, tt.wantAuth, req.Header.Get("Authorization"))
		})
	}
}

func TestEmbeddings_DelegatesToCompatibleProvider(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"object":"list",
		"model":"nomic-embed-text-v1.5",
		"data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],
		"usage":{"prompt_tokens":3,"total_tokens":3}
	}`)

	provider := NewWithHTTPClient("", server.URL+"/v1", server.Client(), llmclient.Hooks{})

	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "nomic-embed-text-v1.5",
		Input: "hello",
	})
	require.NoError(t, err)
	assert.Equal(t, "nomic-embed-text-v1.5", resp.Model)
	assert.Equal(t, "/v1/embeddings", capture.Last(t).Path)
}

func TestProvider_DoesNotExposeOptionalNativeInterfaces(t *testing.T) {
	provider := NewWithHTTPClient("", "", nil, llmclient.Hooks{})
	providertest.AssertNoNativeSurfaces(t, provider)
	_, ok := any(provider).(core.NativeResponseLifecycleProvider)
	require.False(t, ok)
}

func TestPassthrough_RoutesNativeEndpointsToServerRoot(t *testing.T) {
	tests := []struct {
		name      string
		endpoint  string
		wantPath  string
		wantQuery string
	}{
		{name: "rerank", endpoint: "rerank", wantPath: "/rerank"},
		{name: "health", endpoint: "health", wantPath: "/health"},
		{name: "tokenize", endpoint: "tokenize", wantPath: "/tokenize"},
		{name: "explicit v1 rerank", endpoint: "v1/rerank", wantPath: "/v1/rerank"},
		{name: "chat completions", endpoint: "chat/completions", wantPath: "/v1/chat/completions"},
		{name: "embeddings", endpoint: "embeddings", wantPath: "/v1/embeddings"},
		{name: "chat completions with query", endpoint: "chat/completions?stream=true", wantPath: "/v1/chat/completions", wantQuery: "stream=true"},
		{name: "native endpoint with query", endpoint: "health?include_slots=true", wantPath: "/health", wantQuery: "include_slots=true"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, `{"ok":true}`)

			provider := NewWithHTTPClient("llamacpp-key", server.URL+"/v1", server.Client(), llmclient.Hooks{})

			resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
				Method:   http.MethodPost,
				Endpoint: tt.endpoint,
				Body:     io.NopCloser(strings.NewReader("{}")),
				Headers:  http.Header{"Content-Type": []string{"application/json"}},
			})
			require.NoError(t, err)
			defer resp.Body.Close()

			req := capture.Last(t)
			assert.Equal(t, tt.wantPath, req.Path)
			assert.Equal(t, tt.wantQuery, req.Query.Encode())
			assert.Equal(t, "Bearer llamacpp-key", req.Header.Get("Authorization"))
		})
	}
}

func TestPassthrough_NativeEndpointRotatesConfiguredKeys(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"ok":true}`)

	provider := New(providers.ProviderConfig{
		APIKey:  "key-1",
		APIKeys: []string{"key-1", "key-2"},
		BaseURL: server.URL + "/v1",
	}, providers.ProviderOptions{
		Keys: providers.NewKeyringWithSessionStickiness(false, "key-1", "key-2"),
	}).(*Provider)

	for range 2 {
		resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
			Method:   http.MethodGet,
			Endpoint: "health",
			Headers:  http.Header{},
		})
		require.NoError(t, err)
		resp.Body.Close()
	}

	var gotAuths []string
	for _, req := range capture.All() {
		gotAuths = append(gotAuths, req.Header.Get("Authorization"))
	}
	assert.ElementsMatch(t, []string{"Bearer key-1", "Bearer key-2"}, gotAuths)
}

func TestNew_DefaultOptionsAuthenticatesAndRelaysNativeErrors(t *testing.T) {
	server, capture := providertest.RouteServer(t, map[string]http.HandlerFunc{
		"/health": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		},
		"/slots": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotImplemented)
			_, _ = w.Write([]byte(`{"error":{"message":"slots endpoint is disabled"}}`))
		},
	})

	provider := New(providers.ProviderConfig{
		APIKey:  "llamacpp-key",
		BaseURL: server.URL + "/v1",
	}, providers.ProviderOptions{}).(*Provider)

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodGet,
		Endpoint: "health",
		Headers:  http.Header{},
	})
	require.NoError(t, err)

	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, string(body), `"ok"`)

	// Provider-native errors relay status and body verbatim instead of being
	// converted into gateway errors.
	resp, err = provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodGet,
		Endpoint: "slots",
		Headers:  http.Header{},
	})
	require.NoError(t, err)

	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	assert.Equal(t, http.StatusNotImplemented, resp.StatusCode)
	assert.Contains(t, string(body), "slots endpoint is disabled")

	for _, req := range capture.All() {
		assert.Equal(t, "Bearer llamacpp-key", req.Header.Get("Authorization"), "path %s", req.Path)
	}
}
