package deepseek

import (
	"context"
	"encoding/json"
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

// DeepSeek is a thin wrapper over the shared chat-centric adapter, so the
// shared contract covers its surface. DeepSeek exposes no embeddings
// endpoint, so Embeddings must fail fast without an upstream call, and the
// provider must not advertise native batch, file, audio, or
// response-lifecycle support.
func TestChatCompatibleContract(t *testing.T) {
	providertest.AssertChatCompatible(t, providertest.ChatCompatible{
		Registration:   Registration,
		Type:           "deepseek",
		DefaultBaseURL: "https://api.deepseek.com",
		New: func(apiKey, baseURL string, client *http.Client, hooks llmclient.Hooks) core.Provider {
			return NewWithHTTPClient(apiKey, baseURL, client, hooks)
		},
	})

	provider := NewWithHTTPClient("deepseek-key", "", nil, llmclient.Hooks{})
	providertest.AssertNoNativeSurfaces(t, provider)
	_, ok := any(provider).(core.NativeResponseLifecycleProvider)
	assert.False(t, ok, "provider should not implement core.NativeResponseLifecycleProvider")
}

func TestChatCompletion_MapsReasoningToDeepSeekReasoningEffort(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)

	provider := NewWithHTTPClient("deepseek-key", server.URL, server.Client(), llmclient.Hooks{})
	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:     "deepseek-v4-pro",
		Messages:  []core.Message{{Role: "user", Content: "hi"}},
		Reasoning: &core.Reasoning{Effort: "medium"},
	})
	require.NoError(t, err)

	sent := capture.Last(t).JSON(t)
	assert.NotContains(t, sent, "reasoning")
	assert.Equal(t, "high", sent["reasoning_effort"])
}

func TestChatCompletion_PadsMissingReasoningContentForAssistantToolCalls(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)

	var req core.ChatRequest
	err := json.Unmarshal([]byte(`{
		"model":"deepseek-v4-pro",
		"messages":[
			{"role":"user","content":"check"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"ok"},
			{"role":"assistant","content":null,"reasoning_content":"client reasoning","tool_calls":[{"id":"call_2","type":"function","function":{"name":"lookup","arguments":"{}"}}]}
		],
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]
	}`), &req)
	require.NoError(t, err)

	provider := NewWithHTTPClient("deepseek-key", server.URL, server.Client(), llmclient.Hooks{})
	_, err = provider.ChatCompletion(context.Background(), &req)
	require.NoError(t, err)

	messages, ok := capture.Last(t).JSON(t)["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 4)
	missingReasoning, _ := messages[1].(map[string]any)
	assert.Equal(t, " ", missingReasoning["reasoning_content"])
	preservedReasoning, _ := messages[3].(map[string]any)
	assert.Equal(t, "client reasoning", preservedReasoning["reasoning_content"])
	assert.Nil(t, req.Messages[1].ExtraFields.Lookup("reasoning_content"), "caller's request must not be mutated")
}

func TestAdaptChatRequest_DoesNotPadWithoutTools(t *testing.T) {
	req := &core.ChatRequest{
		Messages: []core.Message{{
			Role:      "assistant",
			ToolCalls: []core.ToolCall{{ID: "call_1"}},
		}},
	}

	adapted, err := adaptChatRequest(req)
	require.NoError(t, err)
	assert.Equal(t, req, adapted)
	assert.Nil(t, adapted.Messages[0].ExtraFields.Lookup("reasoning_content"))
}

func TestResponses_MapsTokensAndReasoningEffort(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{
		"id":"chatcmpl-deepseek",
		"created":1677652288,
		"model":"deepseek-v4-pro",
		"choices":[{"index":0,"message":{"role":"assistant","content":"translated"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}
	}`)

	provider := NewWithHTTPClient("deepseek-key", server.URL, server.Client(), llmclient.Hooks{})
	maxOutputTokens := 64
	resp, err := provider.Responses(context.Background(), &core.ResponsesRequest{
		Model:           "deepseek-v4-pro",
		Input:           "Reply with exactly ok",
		MaxOutputTokens: &maxOutputTokens,
		Reasoning:       &core.Reasoning{Effort: "xhigh"},
	})
	require.NoError(t, err)

	req := capture.Last(t)
	assert.Equal(t, "/chat/completions", req.Path)
	sent := req.JSON(t)
	assert.NotContains(t, sent, "max_output_tokens")
	assert.Equal(t, float64(64), sent["max_tokens"])
	assert.Equal(t, "max", sent["reasoning_effort"])
	assert.Equal(t, []any{map[string]any{"role": "user", "content": "Reply with exactly ok"}}, sent["messages"])

	assert.Equal(t, "response", resp.Object)
	assert.Equal(t, "completed", resp.Status)
	require.Len(t, resp.Output, 1)
	require.Len(t, resp.Output[0].Content, 1)
	assert.Equal(t, "translated", resp.Output[0].Content[0].Text)
	require.NotNil(t, resp.Usage)
	assert.Equal(t, 5, resp.Usage.TotalTokens)
}

func TestResponses_ReplaysReasoningContentForToolCall(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, providertest.ChatCompletionJSON)

	var req core.ResponsesRequest
	err := json.Unmarshal([]byte(`{
		"model":"deepseek-v4-pro",
		"input":[
			{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"Need the weather."}]},
			{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_1","output":"sunny"}
		]
	}`), &req)
	require.NoError(t, err)

	provider := NewWithHTTPClient("deepseek-key", server.URL, server.Client(), llmclient.Hooks{})
	_, err = provider.Responses(context.Background(), &req)
	require.NoError(t, err)

	messages, ok := capture.Last(t).JSON(t)["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 2)
	assistant, _ := messages[0].(map[string]any)
	assert.Equal(t, "assistant", assistant["role"])
	assert.Equal(t, "Need the weather.", assistant["reasoning_content"])
	assert.Len(t, assistant["tool_calls"], 1)
}

func TestStreamResponses_TranslatesToChatCompletions(t *testing.T) {
	server, capture := providertest.SSEServer(t, "data: {\"id\":\"chatcmpl-deepseek\",\"object\":\"chat.completion.chunk\",\"created\":1677652288,\"model\":\"deepseek-v4-pro\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n")

	provider := NewWithHTTPClient("deepseek-key", server.URL, server.Client(), llmclient.Hooks{})
	stream, err := provider.StreamResponses(context.Background(), &core.ResponsesRequest{
		Model: "deepseek-v4-pro",
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
	assert.Equal(t, "/chat/completions", req.Path)
	assert.Equal(t, true, req.JSON(t)["stream"])
}

func TestNormalizeReasoningEffort(t *testing.T) {
	tests := map[string]string{
		"low":    "high",
		"medium": "high",
		"high":   "high",
		"xhigh":  "max",
		"max":    "max",
		"custom": "custom",
	}
	for input, expected := range tests {
		t.Run(input, func(t *testing.T) {
			assert.Equal(t, expected, normalizeReasoningEffort(input))
		})
	}
}

func TestPassthrough_ForwardsRequestWithBearerAuth(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"object":"fim_completion","choices":[{"text":"world"}]}`)

	provider := NewWithHTTPClient("deepseek-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "/beta/completions",
		Body:     io.NopCloser(strings.NewReader(`{"model":"deepseek-v4-pro","prompt":"hello "}`)),
		Headers:  http.Header{"Content-Type": []string{"application/json"}},
	})
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	req := capture.Last(t)
	assert.Equal(t, http.MethodPost, req.Method)
	assert.Equal(t, "/beta/completions", req.Path)
	assert.Equal(t, "Bearer deepseek-key", req.Header.Get("Authorization"))
	assert.Equal(t, "application/json", req.Header.Get("Content-Type"))
	assert.Contains(t, string(req.Body), "deepseek-v4-pro")
}

func TestPassthrough_NilRequest_ReturnsError(t *testing.T) {
	provider := NewWithHTTPClient("deepseek-key", "", nil, llmclient.Hooks{})
	_, err := provider.Passthrough(context.Background(), nil)
	require.Error(t, err)
}

func TestPassthrough_PreservesNon2xxStatusAndBody(t *testing.T) {
	const upstreamBody = `{"error":{"message":"rate_limit_exceeded","type":"rate_limit_error"}}`
	server, _ := providertest.JSONServer(t, http.StatusTooManyRequests, upstreamBody)

	provider := NewWithHTTPClient("deepseek-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodPost,
		Endpoint: "/beta/completions",
		Body:     io.NopCloser(strings.NewReader(`{}`)),
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, upstreamBody, string(body))
}

func TestPassthrough_ForwardsQueryString(t *testing.T) {
	server, capture := providertest.JSONServer(t, http.StatusOK, `{}`)

	provider := NewWithHTTPClient("deepseek-key", server.URL, server.Client(), llmclient.Hooks{})
	resp, err := provider.Passthrough(context.Background(), &core.PassthroughRequest{
		Method:   http.MethodGet,
		Endpoint: "/beta/completions?stream=true",
		Body:     io.NopCloser(strings.NewReader(``)),
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	req := capture.Last(t)
	assert.Equal(t, "/beta/completions", req.Path)
	assert.Equal(t, "true", req.Query.Get("stream"))
}

func TestResponses_NilRequest_ReturnsError(t *testing.T) {
	provider := NewWithHTTPClient("deepseek-key", "", nil, llmclient.Hooks{})

	_, err := provider.Responses(context.Background(), nil)
	require.Error(t, err)

	_, err = provider.StreamResponses(context.Background(), nil)
	require.Error(t, err)
}
