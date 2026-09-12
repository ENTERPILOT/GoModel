package sglang

import (
	"context"
	"encoding/json"
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

func TestChatCompletionUsesOptionalBearerAuthAndV1Endpoint(t *testing.T) {
	for _, tt := range []struct {
		name     string
		apiKey   string
		wantAuth string
	}{
		{name: "with API key", apiKey: "sglang-key", wantAuth: "Bearer sglang-key"},
		{name: "without API key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)

			provider := NewWithHTTPClient(tt.apiKey, server.URL+"/v1", server.Client(), llmclient.Hooks{})
			resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
				Model:    providertest.Model,
				Messages: []core.Message{{Role: "user", Content: "hi"}},
			})
			require.NoError(t, err)
			assert.Equal(t, providertest.Model, resp.Model)

			req := capture.Last(t)
			assert.Equal(t, "/v1/chat/completions", req.Path)
			assert.Equal(t, tt.wantAuth, req.Header.Get("Authorization"))
		})
	}
}

func TestChatCompletionPreservesSGLangExtensionFields(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"id":"chatcmpl-sglang","model":"test","choices":[]}`)

	var req core.ChatRequest
	err := json.Unmarshal([]byte(`{
		"model":"test",
		"messages":[{"role":"user","content":"hi"}],
		"chat_template_kwargs":{"enable_thinking":false},
		"separate_reasoning":true
	}`), &req)
	require.NoError(t, err)

	provider := NewWithHTTPClient("", server.URL+"/v1", server.Client(), llmclient.Hooks{})
	_, err = provider.ChatCompletion(context.Background(), &req)
	require.NoError(t, err)

	sent := capture.Last(t).JSON(t)
	assert.Equal(t, map[string]any{"enable_thinking": false}, sent["chat_template_kwargs"])
	assert.Equal(t, true, sent["separate_reasoning"])
}

func TestOpenAICompatibleEndpoints(t *testing.T) {
	tests := []struct {
		name     string
		wantPath string
		call     func(*Provider) error
		response string
	}{
		{
			name:     "models",
			wantPath: "/v1/models",
			response: `{"object":"list","data":[]}`,
			call: func(p *Provider) error {
				_, err := p.ListModels(context.Background())
				return err
			},
		},
		{
			name:     "responses",
			wantPath: "/v1/responses",
			response: `{"id":"resp-sglang","object":"response","status":"completed","model":"test","output":[]}`,
			call: func(p *Provider) error {
				_, err := p.Responses(context.Background(), &core.ResponsesRequest{Model: "test"})
				return err
			},
		},
		{
			name:     "embeddings",
			wantPath: "/v1/embeddings",
			response: `{"object":"list","model":"test","data":[{"object":"embedding","embedding":[0.1],"index":0}],"usage":{"prompt_tokens":1,"total_tokens":1}}`,
			call: func(p *Provider) error {
				_, err := p.Embeddings(context.Background(), &core.EmbeddingRequest{Model: "test", Input: "hello"})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, tt.response)

			provider := NewWithHTTPClient("", server.URL+"/v1", server.Client(), llmclient.Hooks{})
			require.NoError(t, tt.call(provider))
			assert.Equal(t, tt.wantPath, capture.Last(t).Path)
		})
	}
}

func TestOpenAICompatibleStreamingEndpoints(t *testing.T) {
	tests := []struct {
		name     string
		wantPath string
		call     func(*Provider) (io.ReadCloser, error)
	}{
		{
			name:     "chat completions",
			wantPath: "/v1/chat/completions",
			call: func(p *Provider) (io.ReadCloser, error) {
				return p.StreamChatCompletion(context.Background(), &core.ChatRequest{Model: "test"})
			},
		},
		{
			name:     "responses",
			wantPath: "/v1/responses",
			call: func(p *Provider) (io.ReadCloser, error) {
				return p.StreamResponses(context.Background(), &core.ResponsesRequest{Model: "test"})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.SSEServer(t, "data: [DONE]\n\n")

			provider := NewWithHTTPClient("", server.URL+"/v1", server.Client(), llmclient.Hooks{})
			body, err := tt.call(provider)
			require.NoError(t, err)
			defer body.Close()
			assert.Equal(t, tt.wantPath, capture.Last(t).Path)
		})
	}
}

// SGLang serves passthrough and the OpenAI-compatible surface, but nothing
// that would need native batch, file, audio, or response-lifecycle support.
func TestProviderExposesOnlyVerifiedOptionalInterfaces(t *testing.T) {
	provider := NewWithHTTPClient("", "", nil, llmclient.Hooks{})
	providertest.AssertNoNativeSurfaces(t, provider)
	_, ok := any(provider).(core.NativeResponseLifecycleProvider)
	assert.False(t, ok, "provider should not implement core.NativeResponseLifecycleProvider")
}

func TestPassthroughRoutesNativeAndOpenAIEndpoints(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		wantPath string
	}{
		{name: "native generate", endpoint: "generate", wantPath: "/generate"},
		{name: "native health", endpoint: "health", wantPath: "/health"},
		{name: "explicit v1 rerank", endpoint: "v1/rerank", wantPath: "/v1/rerank"},
		{name: "normalized v1 rerank alias", endpoint: "rerank", wantPath: "/v1/rerank"},
		{name: "normalized v1 tokenize alias", endpoint: "tokenize", wantPath: "/v1/tokenize"},
		{name: "OpenAI chat", endpoint: "chat/completions", wantPath: "/v1/chat/completions"},
		{name: "OpenAI models with query", endpoint: "models?limit=1", wantPath: "/v1/models"},
		{name: "OpenAI responses lifecycle", endpoint: "responses/resp-1", wantPath: "/v1/responses/resp-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, capture := providertest.JSONServer(t, http.StatusOK, `{}`)

			provider := NewWithHTTPClient("sglang-key", server.URL+"/v1", server.Client(), llmclient.Hooks{})
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
			assert.Equal(t, "Bearer sglang-key", req.Header.Get("Authorization"))
		})
	}
}

func TestNewSharesKeyRotationWithNativePassthrough(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"object":"list","data":[]}`)

	keys := providers.NewKeyring("key-one", "key-two")
	provider := New(providers.ProviderConfig{
		Type:    "sglang",
		APIKey:  "key-one",
		BaseURL: server.URL + "/v1",
	}, providers.ProviderOptions{Keys: keys}).(*Provider)
	_, err := provider.ListModels(context.Background())
	require.NoError(t, err)

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodGet,
		Endpoint: "health",
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	requests := capture.All()
	require.Len(t, requests, 2)
	assert.Equal(t, "Bearer key-one", requests[0].Header.Get("Authorization"))
	assert.Equal(t, "Bearer key-two", requests[1].Header.Get("Authorization"))
}

func TestSetBaseURLUpdatesOpenAIAndNativeClients(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"object":"list","data":[]}`)

	provider := NewWithHTTPClient("", "http://127.0.0.1:1/v1", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL + "/v1")
	_, err := provider.ListModels(context.Background())
	require.NoError(t, err)

	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodGet,
		Endpoint: "health",
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	requests := capture.All()
	require.Len(t, requests, 2)
	assert.Equal(t, "/v1/models", requests[0].Path)
	assert.Equal(t, "/health", requests[1].Path)
}
