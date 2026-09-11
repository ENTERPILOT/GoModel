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

// The signature and the opaque redacted payload of the thinking fixture. A
// signature is bound to the exact thinking text, so a gateway that rebuilds
// the block from reasoning_content alone cannot produce a usable one: it has
// to carry back what the model returned, byte for byte.
const (
	signedThinkingSignature = "ErUBCkYIBRgCIkDPthinkingSignatureFixtureForContractTestsEgxSaW5nU2lnbmF0dXJlGgz//8AB"
	signedThinkingText      = "The user asked for the weather in Paris. I should call get_weather."
	redactedThinkingData    = "EroBCoYBEncKdG9wYXF1ZS1yZWRhY3RlZC10aGlua2luZy1maXh0dXJl"
)

// TestAnthropicThinkingSignatureRoundTrip is the response-side mirror of
// TestAnthropicThinkingBlocksReplay: that test proves a client's thinking
// blocks survive the trip upstream, this one proves the gateway hands the
// client blocks worth replaying in the first place. Anthropic rejects a
// thinking-enabled turn whose thinking block has no signature
// ("messages.N.content.0.thinking.signature: Field required"), so an agent
// loop that echoes the assistant turn back fails on the second request unless
// every dialect carries the signature out and back.
func TestAnthropicThinkingSignatureRoundTrip(t *testing.T) {
	const model = "claude-sonnet-4-5"
	client, captured := newCapturingJSONClient(t, "anthropic/messages_thinking_tool_use.json")
	provider := anthropic.NewWithHTTPClient("sk-ant-test", client, llmclient.Hooks{})
	provider.SetBaseURL("https://replay.local")

	user := core.Message{Role: "user", Content: "What is the weather in Paris?"}
	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: model, Messages: []core.Message{user}, Tools: weatherTools})
	require.NoError(t, err)
	require.Len(t, resp.Choices, 1)
	message := resp.Choices[0].Message
	require.Len(t, message.ToolCalls, 1)
	callID := message.ToolCalls[0].ID

	require.JSONEq(t,
		`{"thinking_blocks":[
			{"type":"thinking","thinking":"`+signedThinkingText+`","signature":"`+signedThinkingSignature+`"},
			{"type":"redacted_thinking","data":"`+redactedThinkingData+`"}
		]}`,
		string(message.ExtraFields.ExtraContent(core.ExtraContentVendorAnthropic)),
		"the chat response must expose the signed thinking blocks under extra_content.anthropic")

	histories := map[string]func(t *testing.T) []core.Message{
		"chat_completions":   func(t *testing.T) []core.Message { return chatHistory(resp, callID) },
		"responses":          func(t *testing.T) []core.Message { return responsesHistory(t, resp, callID) },
		"anthropic_messages": func(t *testing.T) []core.Message { return anthropicHistory(t, resp, callID) },
	}
	for name, build := range histories {
		t.Run(name, func(t *testing.T) {
			messages := append([]core.Message{user}, build(t)...)
			_, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{Model: model, Messages: messages, Tools: weatherTools})
			require.NoError(t, err)

			upstream := captured.jsonBody(t)["messages"].([]any)
			require.Len(t, upstream, 3, "user, assistant, tool result")
			assistant := upstream[1].(map[string]any)["content"].([]any)
			require.GreaterOrEqual(t, len(assistant), 2, "thinking blocks must lead the assistant content")

			thinking, _ := json.Marshal(assistant[0])
			redacted, _ := json.Marshal(assistant[1])
			require.JSONEq(t, `{"type":"thinking","thinking":"`+signedThinkingText+`","signature":"`+signedThinkingSignature+`"}`, string(thinking),
				"the thinking block must be replayed verbatim, signature included")
			require.JSONEq(t, `{"type":"redacted_thinking","data":"`+redactedThinkingData+`"}`, string(redacted))
			for _, block := range assistant {
				require.NotContains(t, block.(map[string]any), "extra_content", "no block may carry the gateway's own member upstream")
			}
		})
	}
}

// TestAnthropicThinkingSignatureSurvivesMessagesDialect pins the client-facing
// half of the round trip: /v1/messages must render the signature on the
// thinking block itself, because that is the only field Anthropic clients know
// to echo back. extra_content is the carrier for dialects that have no
// thinking block; it must not leak into the Anthropic response shape.
func TestAnthropicThinkingSignatureSurvivesMessagesDialect(t *testing.T) {
	client, _ := newCapturingJSONClient(t, "anthropic/messages_thinking_tool_use.json")
	provider := anthropic.NewWithHTTPClient("sk-ant-test", client, llmclient.Hooks{})
	provider.SetBaseURL("https://replay.local")

	resp, err := provider.ChatCompletion(context.Background(), &core.ChatRequest{
		Model:    "claude-sonnet-4-5",
		Messages: []core.Message{{Role: "user", Content: "What is the weather in Paris?"}},
		Tools:    weatherTools,
	})
	require.NoError(t, err)

	rendered, err := json.Marshal(anthropicapi.FromChatResponse(resp).Content)
	require.NoError(t, err)
	var blocks []map[string]any
	require.NoError(t, json.Unmarshal(rendered, &blocks))

	require.GreaterOrEqual(t, len(blocks), 2)
	require.Equal(t, "thinking", blocks[0]["type"])
	require.Equal(t, signedThinkingText, blocks[0]["thinking"])
	require.Equal(t, signedThinkingSignature, blocks[0]["signature"])
	require.NotContains(t, blocks[0], "extra_content", "the Anthropic dialect carries the signature natively")
	require.Equal(t, "redacted_thinking", blocks[1]["type"])
	require.Equal(t, redactedThinkingData, blocks[1]["data"])
	require.NotContains(t, blocks[1], "thinking", "a redacted block has no readable thinking text")
}
