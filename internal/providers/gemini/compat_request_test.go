package gemini

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

func TestCompatReasoningEffort(t *testing.T) {
	tests := []struct {
		model, effort, want string
	}{
		{model: "gemini-4-argon", effort: "minimal", want: "low"},
		{model: "gemini-4-argon", effort: "none", want: "low"},
		{model: "gemini-4-argon", effort: "high", want: "high"},
		{model: "gemini-3.8-flash", effort: "minimal", want: "low"},
		{model: "gemini-3.1-pro-preview", effort: "MINIMAL", want: "low"},
		{model: "gemini-3.6-flash", effort: "minimal", want: "minimal"},
		{model: "gemini-3.5-flash-lite", effort: "none", want: "minimal"},
		{model: "gemini-2.5-flash", effort: "none", want: "none"},
		{model: "gemini-2.5-flash", effort: "minimal", want: "minimal"},
		{model: "gemma-4-31b-it", effort: "none", want: "none"},
	}
	for _, tt := range tests {
		t.Run(tt.model+"/"+tt.effort, func(t *testing.T) {
			assert.Equal(t, tt.want, compatReasoningEffort(tt.model, tt.effort))
		})
	}
}

// Google's OpenAI-compatible endpoint rejects several Chat Completions members
// outright and the "minimal" level on Gemini 4; the adapter sends what it
// accepts.
func TestChatCompletion_CompatDropsRejectedFieldsAndClampsEffort(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "false")
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"id":"x","object":"chat.completion","model":"gemini-4-argon","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
	provider := New(providers.ProviderConfig{APIKey: "key", BaseURL: server.URL}, providertest.Options(llmclient.Hooks{})).(*Provider)

	var req core.ChatRequest
	body := `{"model":"gemini-4-argon","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"minimal",` +
		`"presence_penalty":0.5,"frequency_penalty":0.3,"seed":7,"logprobs":true,"top_logprobs":2,"store":false,` +
		`"metadata":{"a":"b"},"logit_bias":{"1":1},"top_k":40,"prediction":{"type":"content","content":"x"},` +
		`"prompt_cache_key":"k","safety_identifier":"u","verbosity":"low","web_search_options":{},` +
		`"max_completion_tokens":200,"user":"u1","stop":["END"],"n":1}`
	require.NoError(t, json.Unmarshal([]byte(body), &req))

	_, err := provider.ChatCompletion(context.Background(), &req)
	require.NoError(t, err)

	sent := capture.Last(t).JSON(t)
	for _, key := range []string{
		"presence_penalty", "frequency_penalty", "seed", "logprobs", "top_logprobs",
		"store", "metadata", "logit_bias", "top_k", "prediction", "prompt_cache_key",
		"safety_identifier", "verbosity", "web_search_options",
	} {
		assert.NotContains(t, sent, key)
	}
	assert.Equal(t, "low", sent["reasoning_effort"])
	assert.NotContains(t, sent, "reasoning")
	assert.Equal(t, float64(200), sent["max_completion_tokens"])
	assert.Equal(t, "u1", sent["user"])
	assert.Equal(t, []any{"END"}, sent["stop"])
	assert.Equal(t, float64(1), sent["n"])
}

func TestChatCompletion_CompatNestedReasoningIsClamped(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "false")
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"id":"x","object":"chat.completion","model":"gemini-2.5-flash","choices":[]}`)
	provider := New(providers.ProviderConfig{APIKey: "key", BaseURL: server.URL}, providertest.Options(llmclient.Hooks{})).(*Provider)

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:     "gemini-2.5-flash",
		Messages:  []core.Message{{Role: "user", Content: "hi"}},
		Reasoning: &core.Reasoning{Effort: "none"},
	})
	require.NoError(t, err)
	sent := capture.Last(t).JSON(t)
	assert.Equal(t, "none", sent["reasoning_effort"], "2.5 keeps none: it disables thinking there")
	assert.NotContains(t, sent, "reasoning")
}

func TestWithCompatThoughtSignatures(t *testing.T) {
	signed := []core.Message{
		{Role: "user", Content: "weather?"},
		{Role: "assistant", ToolCalls: []core.ToolCall{toolCallWithSignature("call_1", "lookup_weather", "sig-1"), toolCallWithSignature("call_2", "lookup_weather", "")}},
		{Role: "tool", ToolCallID: "call_1", Content: "{}"},
	}
	unsigned := []core.Message{
		{Role: "user", Content: "weather?"},
		{Role: "assistant", ToolCalls: []core.ToolCall{toolCallWithSignature("call_1", "lookup_weather", "")}},
		{Role: "tool", ToolCallID: "call_1", Content: "{}"},
	}

	t.Run("signed parallel batch is left alone", func(t *testing.T) {
		out := withCompatThoughtSignatures("gemini-4-argon", signed)
		assert.Equal(t, "sig-1", toolCallThoughtSignature(out[1].ToolCalls[0]))
		assert.Empty(t, toolCallThoughtSignature(out[1].ToolCalls[1]), "only the first call of a batch is signed")
	})
	t.Run("unsigned calls on Gemini 4 get the placeholder", func(t *testing.T) {
		out := withCompatThoughtSignatures("gemini-4-argon", unsigned)
		assert.Equal(t, skipThoughtSignatureValidator, toolCallThoughtSignature(out[1].ToolCalls[0]))
		assert.Empty(t, toolCallThoughtSignature(unsigned[1].ToolCalls[0]), "input is not mutated")
	})
	t.Run("Gemini 2.5 does not validate signatures", func(t *testing.T) {
		out := withCompatThoughtSignatures("gemini-2.5-flash", unsigned)
		assert.Empty(t, toolCallThoughtSignature(out[1].ToolCalls[0]))
	})
	t.Run("placeholder keeps other extra fields on the call", func(t *testing.T) {
		call := core.ToolCall{ID: "call_1", Type: "function", Function: core.FunctionCall{Name: "f", Arguments: "{}"}, ExtraFields: rawFields(map[string]string{"cache_control": `{"type":"ephemeral"}`})}
		out := withCompatThoughtSignatures("gemini-3.8-flash", []core.Message{{Role: "assistant", ToolCalls: []core.ToolCall{call}}})
		assert.Equal(t, skipThoughtSignatureValidator, toolCallThoughtSignature(out[0].ToolCalls[0]))
		assert.JSONEq(t, `{"type":"ephemeral"}`, string(out[0].ToolCalls[0].ExtraFields.Lookup("cache_control")))
	})
}

func TestChatCompletion_CompatNarrowsAllowedTools(t *testing.T) {
	t.Setenv(useNativeAPIEnvVar, "false")
	tools := []map[string]any{
		{"type": "function", "function": map[string]any{"name": "get_weather"}},
		{"type": "function", "function": map[string]any{"name": "get_time"}},
	}
	allowed := func(mode string, names ...string) map[string]any {
		subset := make([]any, 0, len(names))
		for _, name := range names {
			subset = append(subset, map[string]any{"type": "function", "function": map[string]any{"name": name}})
		}
		return map[string]any{"type": "allowed_tools", "allowed_tools": map[string]any{"mode": mode, "tools": subset}}
	}
	server, capture := providertest.JSONServer(t, http.StatusOK, `{"id":"x","object":"chat.completion","model":"gemini-4-argon","choices":[]}`)
	provider := New(providers.ProviderConfig{APIKey: "key", BaseURL: server.URL}, providertest.Options(llmclient.Hooks{})).(*Provider)
	messages := []core.Message{{Role: "user", Content: "time?"}}

	_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: "gemini-4-argon", Messages: messages, Tools: tools, ToolChoice: allowed("required", "get_time")})
	require.NoError(t, err)
	sent := capture.Last(t).JSON(t)
	assert.Equal(t, "required", sent["tool_choice"])
	require.Len(t, sent["tools"], 1)
	assert.Equal(t, "get_time", sent["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"])

	_, err = provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: "gemini-4-argon", Messages: messages, Tools: tools, ToolChoice: allowed("auto", "missing")})
	require.Error(t, err, "a subset naming no declared tool is rejected locally")
	assert.Equal(t, 1, capture.Count())
}
