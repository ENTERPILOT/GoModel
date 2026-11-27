package cline

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Cline is a thin wrapper over the shared chat-centric adapter, so the shared
// contract covers its surface. Cline documents no embeddings endpoint, so
// Embeddings must fail fast without an upstream call, and the provider must
// not advertise native batch, file, or audio support.
func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           "cline",
		DefaultBaseURL: "https://api.cline.bot/api/v1",
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			return newTestProvider(apiKey, baseURL, client, hooks)
		},
	})
	providertest.AssertNoNativeSurfaces(t, New(providers.ProviderConfig{APIKey: "cline-key"}, providers.ProviderOptions{}))
}

// A buffered request must say "stream": false and ask for the OpenAI response
// shape: Cline streams by default and otherwise answers in a legacy envelope
// the typed decode cannot read. The reply is the one Cline documents, without
// an object type or a token total, so both must be filled in.
func TestChatCompletion_RequestsBufferedOpenAIShape(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"id":"gen-abc123",
		"model":"anthropic/claude-sonnet-4-6",
		"choices":[{"index":0,"message":{"role":"assistant","content":"Paris."},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":14,"completion_tokens":8}
	}`)

	provider := newTestProvider("cline-key", server.URL, server.Client(), llmclient.Hooks{})
	req := &core.ChatRequest{
		Model:    "anthropic/claude-sonnet-4-6",
		Messages: []core.Message{{Role: "user", Content: "What is the capital of France?"}},
	}
	resp, err := provider.ChatCompletion(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, 1, capture.Count())
	sent := capture.Last(t)
	assert.Equal(t, "openai", sent.Header.Get("X-Response-Format"))
	assert.Equal(t, "Bearer cline-key", sent.Header.Get("Authorization"))
	body := sent.JSON(t)
	assert.Equal(t, false, body["stream"])
	assert.Equal(t, "anthropic/claude-sonnet-4-6", body["model"])
	assert.True(t, req.ExtraFields.IsEmpty(), "the caller's request must not be mutated")

	assert.Equal(t, "chat.completion", resp.Object)
	assert.Equal(t, "anthropic/claude-sonnet-4-6", resp.Model)
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "Paris.", resp.Choices[0].Message.Content)
	assert.Equal(t, 14, resp.Usage.PromptTokens)
	assert.Equal(t, 8, resp.Usage.CompletionTokens)
	assert.Equal(t, 22, resp.Usage.TotalTokens)
}

// An upstream total is authoritative: it can exceed prompt plus completion
// when the model reasons, so it must not be recomputed.
func TestChatCompletion_KeepsUpstreamTotalTokens(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{
		"id":"gen-abc123",
		"object":"chat.completion",
		"model":"anthropic/claude-sonnet-4-6",
		"choices":[{"index":0,"message":{"role":"assistant","content":"Paris."},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":14,"completion_tokens":8,"total_tokens":40}
	}`)

	provider := newTestProvider("cline-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "anthropic/claude-sonnet-4-6",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	assert.Equal(t, 40, resp.Usage.TotalTokens)
}

func TestResponses_CarriesCompletedUsage(t *testing.T) {
	server, _ := providertest.JSONServer(t, http.StatusOK, `{
		"id":"gen-abc123",
		"model":"anthropic/claude-sonnet-4-6",
		"choices":[{"index":0,"message":{"role":"assistant","content":"Paris."},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":14,"completion_tokens":8}
	}`)

	provider := newTestProvider("cline-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model: "anthropic/claude-sonnet-4-6",
		Input: "What is the capital of France?",
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Usage)
	assert.Equal(t, 14, resp.Usage.InputTokens)
	assert.Equal(t, 8, resp.Usage.OutputTokens)
	assert.Equal(t, 22, resp.Usage.TotalTokens)
}

func TestChatCompletion_KeepsClientExtraFields(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)

	var req core.ChatRequest
	require.NoError(t, req.UnmarshalJSON([]byte(`{
		"model":"cline-pass/qwen3.7-max",
		"messages":[{"role":"user","content":"hi"}],
		"top_k":40
	}`)))

	provider := newTestProvider("cline-key", server.URL, server.Client(), llmclient.Hooks{})
	_, err := provider.ChatCompletion(context.Background(), &req)
	require.NoError(t, err)

	body := capture.Last(t).JSON(t)
	assert.Equal(t, false, body["stream"])
	assert.InDelta(t, 40, body["top_k"], 0)
	assert.Equal(t, "cline-pass/qwen3.7-max", body["model"])
}

func TestStreamChatCompletion_SendsStreamTrueOnce(t *testing.T) {
	server, capture := providertest.SSEServer(t, providertest.ChatChunkSSE)

	provider := newTestProvider("cline-key", server.URL, server.Client(), llmclient.Hooks{})
	stream, err := provider.StreamChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "anthropic/claude-sonnet-4-6",
		Messages: []core.Message{{Role: "user", Content: "hi"}},
	})
	require.NoError(t, err)
	defer stream.Close()
	_, err = io.ReadAll(stream)
	require.NoError(t, err)

	sent := capture.Last(t)
	assert.Equal(t, true, sent.JSON(t)["stream"])
	assert.NotContains(t, string(sent.Body), `"stream":false`)
}

// Passthrough is opaque: the gateway asks for the OpenAI shape by default, but
// a caller that sends its own X-Response-Format keeps it.
func TestPassthrough_CallerResponseFormatWins(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"success":true,"data":{}}`)
	provider := newTestProvider("cline-key", server.URL, server.Client(), llmclient.Hooks{})

	for name, tc := range map[string]struct {
		headers http.Header
		want    string
	}{
		"default":         {headers: nil, want: "openai"},
		"caller override": {headers: http.Header{"X-Response-Format": {"legacy"}}, want: "legacy"},
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
				Method:   http.MethodPost,
				Endpoint: "chat/completions",
				Headers:  tc.headers,
			})
			require.NoError(t, err)
			require.NoError(t, resp.Body.Close())
			assert.Equal(t, tc.want, capture.Last(t).Header.Get("X-Response-Format"))
		})
	}
}

// newTestProvider builds the provider through its own constructor on a test transport.
func newTestProvider(apiKey, baseURL string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	opts := providertest.Options(hooks)
	opts.HTTPClient = httpClient
	return New(providers.ProviderConfig{APIKey: apiKey, BaseURL: baseURL}, opts).(*Provider)
}
