//go:build contract

// Round-trip contract for provider replay state (extra_content): what a
// provider returns must reach it again, verbatim, after the client echoed the
// assistant turn back through any ingress dialect. A one-directional test
// cannot catch a translator that drops the state on the way out or in.
package contract

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/anthropicapi"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/anthropic"
	"github.com/enterpilot/gomodel/internal/providers/gemini"
)

// newCapturingJSONClient answers every request with the JSON fixture and
// records the last request, so a test can assert what the adapter sent upstream.
func newCapturingJSONClient(t *testing.T, fixture string) (*http.Client, *capturedRequest) {
	t.Helper()
	captured := &capturedRequest{}
	route := jsonFixtureRoute(t, fixture)
	client := &http.Client{Transport: &capturingTransport{t: t, captured: captured, respType: route.contentType, respBody: route.body}}
	return client, captured
}

func (c *capturedRequest) jsonBody(t *testing.T) map[string]any {
	t.Helper()
	require.NotEmpty(t, c.body, "no upstream request captured")
	var body map[string]any
	require.NoError(t, json.Unmarshal(c.body, &body))
	return body
}

const signedThoughtSignature = "CoUBAdHtim9Zf3Q4b2N0ZXQtc2lnbmF0dXJlLWZpeHR1cmUtZm9yLWNvbnRyYWN0LXRlc3RzAQ=="

var weatherTools = []map[string]any{{
	"type": "function",
	"function": map[string]any{
		"name":       "get_weather",
		"parameters": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}},
	},
}}

func TestGeminiThoughtSignatureRoundTrip(t *testing.T) {
	const model = "gemini-3.5-flash"
	t.Setenv("USE_GOOGLE_GEMINI_NATIVE_API", "true")
	client, captured := newCapturingJSONClient(t, "gemini/native_tool_call_signed.json")
	provider := gemini.NewWithHTTPClient("test-api-key", client, llmclient.Hooks{})
	provider.SetBaseURL("https://replay.local")

	user := core.Message{Role: "user", Content: "What is the weather in Paris?"}
	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: model, Messages: []core.Message{user}, Tools: weatherTools})
	require.NoError(t, err)
	require.Len(t, resp.Choices, 1)
	require.Len(t, resp.Choices[0].Message.ToolCalls, 1)
	call := resp.Choices[0].Message.ToolCalls[0]
	require.JSONEq(t, `{"thought_signature":"`+signedThoughtSignature+`"}`, string(call.ExtraFields.ExtraContent(core.ExtraContentVendorGoogle)),
		"the chat response must expose the signature under extra_content.google")

	histories := map[string]func(t *testing.T) []core.Message{
		"chat_completions":   func(t *testing.T) []core.Message { return chatHistory(resp, call.ID) },
		"responses":          func(t *testing.T) []core.Message { return responsesHistory(t, resp, call.ID) },
		"anthropic_messages": func(t *testing.T) []core.Message { return anthropicHistory(t, resp, call.ID) },
	}
	for name, build := range histories {
		t.Run(name, func(t *testing.T) {
			messages := append([]core.Message{user}, build(t)...)
			_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: model, Messages: messages, Tools: weatherTools})
			require.NoError(t, err)

			contents := captured.jsonBody(t)["contents"].([]any)
			require.Len(t, contents, 3, "user, model, function response")
			parts := contents[1].(map[string]any)["parts"].([]any)
			require.Len(t, parts, 1)
			part := parts[0].(map[string]any)
			require.Equal(t, signedThoughtSignature, part["thoughtSignature"], "functionCall part must replay the signature verbatim")
			require.Equal(t, "get_weather", part["functionCall"].(map[string]any)["name"])
		})
	}
}

// chatHistory echoes the assistant turn the way an OpenAI SDK client does.
func chatHistory(resp *core.ChatResponse, callID string) []core.Message {
	msg := resp.Choices[0].Message
	return []core.Message{
		{Role: "assistant", Content: msg.Content, ToolCalls: msg.ToolCalls, ExtraFields: msg.ExtraFields},
		{Role: "tool", ToolCallID: callID, Content: "sunny"},
	}
}

// responsesHistory echoes the Responses output items plus a function_call_output.
func responsesHistory(t *testing.T, resp *core.ChatResponse, callID string) []core.Message {
	t.Helper()
	encoded, err := json.Marshal(providers.ConvertChatResponseToResponses(resp).Output)
	require.NoError(t, err)
	var input []any
	require.NoError(t, json.Unmarshal(encoded, &input))
	input = append(input, map[string]any{"type": "function_call_output", "call_id": callID, "output": "sunny"})
	messages, err := providers.ConvertResponsesInputToMessages(input)
	require.NoError(t, err)
	return messages
}

// anthropicHistory echoes the Messages API content blocks plus a tool_result.
func anthropicHistory(t *testing.T, resp *core.ChatResponse, callID string) []core.Message {
	t.Helper()
	blocks, err := json.Marshal(anthropicapi.FromChatResponse(resp).Content)
	require.NoError(t, err)
	body, err := json.Marshal(map[string]any{
		"model": "m", "max_tokens": 16,
		"messages": []map[string]any{
			{"role": "assistant", "content": json.RawMessage(blocks)},
			{"role": "user", "content": []map[string]any{{"type": "tool_result", "tool_use_id": callID, "content": "sunny"}}},
		},
	})
	require.NoError(t, err)
	decoded, err := anthropicapi.DecodeMessagesRequest(body)
	require.NoError(t, err)
	chat, err := anthropicapi.ToChatRequest(decoded)
	require.NoError(t, err)
	return chat.Messages
}

func TestAnthropicThinkingBlocksReplay(t *testing.T) {
	client, captured := newCapturingJSONClient(t, "anthropic/messages.json")
	provider := anthropic.NewWithHTTPClient("sk-ant-test", client, llmclient.Hooks{})
	provider.SetBaseURL("https://replay.local")

	thinking := `{"type":"thinking","thinking":"","signature":"sig1"}`
	redacted := `{"type":"redacted_thinking","data":"opaque"}`
	decoded, err := anthropicapi.DecodeMessagesRequest([]byte(`{
		"model":"claude-sonnet-4-5","max_tokens":16,
		"messages":[
			{"role":"user","content":"weather?"},
			{"role":"assistant","content":[` + thinking + `,` + redacted + `,{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Paris"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"boom","is_error":true}]}
		]
	}`))
	require.NoError(t, err)
	chat, err := anthropicapi.ToChatRequest(decoded)
	require.NoError(t, err)

	_, err = provider.ChatCompletion(context.Background(), chat)
	require.NoError(t, err)

	messages := captured.jsonBody(t)["messages"].([]any)
	require.Len(t, messages, 3)
	assistant := messages[1].(map[string]any)["content"].([]any)
	require.Len(t, assistant, 3, "thinking, redacted_thinking, tool_use")
	gotThinking, _ := json.Marshal(assistant[0])
	gotRedacted, _ := json.Marshal(assistant[1])
	require.JSONEq(t, thinking, string(gotThinking))
	require.JSONEq(t, redacted, string(gotRedacted))
	require.NotContains(t, assistant[2].(map[string]any), "extra_content", "tool_use must not carry the gateway's own member")
	toolResult := messages[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	require.Equal(t, true, toolResult["is_error"])
}
