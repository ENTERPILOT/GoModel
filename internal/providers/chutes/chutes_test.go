package chutes

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

// Chutes wraps the shared OpenAI-compatible adapter and translates Responses
// through chat completions, so the shared contract covers its surface. The
// shared LLM endpoint has no embeddings route, so Embeddings must fail fast
// without an upstream call, and the provider must not advertise native
// batch, file, audio, or response-lifecycle support.
func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           "chutes",
		DefaultBaseURL: "https://llm.chutes.ai/v1",
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			return NewWithHTTPClient(apiKey, baseURL, client, hooks)
		},
	})

	provider := NewWithHTTPClient("cpk_test", "", nil, llmclient.Hooks{})
	providertest.AssertNoNativeSurfaces(t, provider)
	_, ok := any(provider).(core.NativeResponseLifecycleProvider)
	assert.False(t, ok, "provider should not implement core.NativeResponseLifecycleProvider")
}

func TestSetBaseURL_ChangesRequestTarget(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"object":"list","data":[]}`)

	provider := NewWithHTTPClient("cpk_test", "https://unused.example/v1", server.Client(), llmclient.Hooks{})
	provider.SetBaseURL(server.URL)
	_, err := provider.ListModels(context.Background())
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, http.MethodGet, req.Method)
	assert.Equal(t, "/models", req.Path)
}

func TestChatCompletion_ReturnsUpstreamError(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusTooManyRequests, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)

	provider := NewWithHTTPClient("cpk_test", server.URL, server.Client(), llmclient.Hooks{})
	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "Qwen/Qwen3-32B-TEE",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.Error(t, err)

	var gatewayErr *core.GatewayError
	require.ErrorAs(t, err, &gatewayErr)
	assert.Equal(t, http.StatusTooManyRequests, gatewayErr.StatusCode)
	assert.Equal(t, core.ErrorTypeRateLimit, gatewayErr.Type)
}

func TestPassthrough_ForwardsOpaqueRequest(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusAccepted, `{"accepted":true}`)

	provider := NewWithHTTPClient("cpk_test", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "chat/completions?trace=true",
		Body:     io.NopCloser(strings.NewReader(`{"model":"Qwen/Qwen3-32B-TEE"}`)),
		Headers: http.Header{
			"Content-Type":  {"application/json"},
			"X-Chutes-Beta": {"test"},
		},
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	assert.Equal(t, `{"accepted":true}`, string(responseBody))

	req := capture.Last(t)
	assert.Equal(t, "/chat/completions", req.Path)
	assert.Equal(t, "true", req.Query.Get("trace"))
	assert.Equal(t, "Bearer cpk_test", req.Header.Get("Authorization"))
	assert.Equal(t, "test", req.Header.Get("X-Chutes-Beta"))
	assert.Equal(t, `{"model":"Qwen/Qwen3-32B-TEE"}`, string(req.Body))
}

func TestStreamResponses_TranslatesToChatCompletions(t *testing.T) {
	server, capture := providertest.SSEServer(t, "data: {\"id\":\"chatcmpl-chutes\",\"object\":\"chat.completion.chunk\",\"created\":1677652288,\"model\":\"Qwen/Qwen3-32B-TEE\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")

	provider := NewWithHTTPClient("cpk_test", server.URL, server.Client(), llmclient.Hooks{})
	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "Qwen/Qwen3-32B-TEE",
		Input: "hi",
	})
	require.NoError(t, err)
	defer stream.Close()

	body, err := io.ReadAll(stream)
	require.NoError(t, err)
	raw := string(body)
	assert.Contains(t, raw, "response.output_text.delta")
	assert.Contains(t, raw, "data: [DONE]")

	req := capture.Last(t)
	assert.Equal(t, http.MethodPost, req.Method)
	assert.Equal(t, "/chat/completions", req.Path)
	assert.Equal(t, "Bearer cpk_test", req.Header.Get("Authorization"))
	sent := req.JSON(t)
	assert.Equal(t, "Qwen/Qwen3-32B-TEE", sent["model"])
	assert.Equal(t, true, sent["stream"])
}
