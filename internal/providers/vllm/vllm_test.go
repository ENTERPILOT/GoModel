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

// vLLM serves the OpenAI-compatible surface natively, including Responses and
// embeddings, and may run without an API key. It must not advertise native
// batch, file, audio, or response-lifecycle support.
func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:    Registration,
		Type:            "vllm",
		DefaultBaseURL:  "http://localhost:8000/v1",
		NativeResponses: true,
		Embeddings:      true,
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			return NewWithHTTPClient(apiKey, baseURL, client, hooks)
		},
	})

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
