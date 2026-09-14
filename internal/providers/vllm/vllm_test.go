package vllm

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
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
		{name: "with api key", apiKey: "vllm-key", wantAuth: "Bearer vllm-key"},
		{name: "without api key"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)

			provider := NewWithHTTPClient(tt.apiKey, server.URL, server.Client(), llmclient.Hooks{})
			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:    providertest.Model,
				Messages: []core.Message{{Role: "user", Content: "hi"}},
			})
			require.NoError(t, err)
			assert.Equal(t, providertest.Model, resp.Model)

			req := capture.Last(t)
			assert.Equal(t, "/chat/completions", req.Path)
			assert.Equal(t, tt.wantAuth, req.Header.Get("Authorization"))
		})
	}
}

func TestEmbeddings_DelegatesToCompatibleProvider(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"object":"list",
		"model":"BAAI/bge-small-en-v1.5",
		"data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}],
		"usage":{"prompt_tokens":3,"total_tokens":3}
	}`)

	provider := NewWithHTTPClient("", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.Embeddings(context.Background(), &core.EmbeddingRequest{
		Model: "BAAI/bge-small-en-v1.5",
		Input: "hello",
	})
	require.NoError(t, err)
	assert.Equal(t, "BAAI/bge-small-en-v1.5", resp.Model)
	assert.Equal(t, "/embeddings", capture.Last(t).Path)
}

// vLLM serves passthrough and the OpenAI-compatible surface, but nothing
// that would need native batch, file, audio, or response-lifecycle support.
func TestProvider_DoesNotExposeOptionalNativeInterfaces(t *testing.T) {
	provider := NewWithHTTPClient("", "", nil, llmclient.Hooks{})
	providertest.AssertNoNativeSurfaces(t, provider)
	_, ok := any(provider).(core.NativeResponseLifecycleProvider)
	assert.False(t, ok, "provider should not implement core.NativeResponseLifecycleProvider")
}

func TestPassthrough_ForwardsProviderNativeEndpoint(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"tokens":[1,2,3]}`)

	provider := NewWithHTTPClient("vllm-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "tokenize",
		Body:     io.NopCloser(strings.NewReader("{}")),
		Headers:  http.Header{"Content-Type": []string{"application/json"}},
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	req := capture.Last(t)
	assert.Equal(t, "/tokenize", req.Path)
	assert.Equal(t, "Bearer vllm-key", req.Header.Get("Authorization"))
}

func TestPassthrough_RoutesByEndpointWhenBaseURLIncludesV1(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		body     string
		wantPath string
	}{
		{name: "native endpoint uses the server root", endpoint: "tokenize", body: "{}", wantPath: "/tokenize"},
		{
			name:     "OpenAI-compatible endpoint keeps /v1",
			endpoint: "chat/completions",
			body:     `{"model":"Qwen/Qwen2.5-0.5B-Instruct","messages":[{"role":"user","content":"hi"}]}`,
			wantPath: "/v1/chat/completions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, `{}`)

			provider := NewWithHTTPClient("", server.URL+"/v1", server.Client(), llmclient.Hooks{})
			resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
				Method:   http.MethodPost,
				Endpoint: tt.endpoint,
				Body:     io.NopCloser(strings.NewReader(tt.body)),
				Headers:  http.Header{"Content-Type": []string{"application/json"}},
			})
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, tt.wantPath, capture.Last(t).Path)
		})
	}
}
