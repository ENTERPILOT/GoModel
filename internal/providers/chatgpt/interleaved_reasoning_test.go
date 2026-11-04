package chatgpt

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/anthropicapi"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// messagesWithThinkingAndTool is the Anthropic Messages request a client
// sends: thinking on, a tool declared, and a user turn.
const messagesWithThinkingAndTool = `{
  "model": "gpt-5.6-terra",
  "max_tokens": 4096,
  "stream": true,
  "thinking": {"type": "enabled", "budget_tokens": 10000},
  "tools": [{"name": "get_weather", "description": "Get weather", "input_schema": {"type": "object", "properties": {"city": {"type": "string"}}}}],
  "messages": [{"role": "user", "content": "Weather in Paris?"}]
}`

// codexInterleavedSSE is a Codex Responses stream in which reasoning arrives
// between the argument deltas of a function call.
const codexInterleavedSSE = "event: response.created\n" +
	`data: {"type":"response.created","response":{"id":"resp_1","object":"response","status":"in_progress","model":"gpt-5.6-terra"}}` + "\n\n" +
	"event: response.output_item.added\n" +
	`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"get_weather","arguments":""}}` + "\n\n" +
	"event: response.function_call_arguments.delta\n" +
	`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"ci"}` + "\n\n" +
	"event: response.reasoning_summary_text.delta\n" +
	`data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":1,"summary_index":0,"delta":"Check the city."}` + "\n\n" +
	"event: response.function_call_arguments.delta\n" +
	`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"ty\":\"Paris\"}"}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.6-terra","output":[{"id":"fc_1","type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"},{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"Check the city."}]}],"usage":{"input_tokens":120,"output_tokens":40,"total_tokens":160}}}` + "\n\n" +
	"data: [DONE]\n\n"

// TestChatCompletion_InterleavedReasoningAndToolCall drives the buffered
// half of the same chain.
//
// The canonical chat shape carries the reasoning as one member beside the
// answer text and the tool calls, so the buffered response renders the
// thinking block first, then the text, then the tool calls. Anthropic
// requires the thinking blocks to lead the assistant content, so this
// ordering is the contract the buffered path has to meet; the upstream
// interleaving itself is not representable in that shape and is not claimed.
func TestChatCompletion_InterleavedReasoningAndToolCall(t *testing.T) {
	srv, _ := providertest.SSEServer(t, codexInterleavedSSE)
	provider := newTestProvider(tokenWithAccount(t, "acct-1"), srv.URL, srv.Client(), llmclient.Hooks{})

	decoded, err := anthropicapi.DecodeMessagesRequest([]byte(strings.Replace(messagesWithThinkingAndTool, `"stream": true,`, `"stream": false,`, 1)))
	require.NoError(t, err)
	chatReq, err := anthropicapi.ToChatRequest(decoded)
	require.NoError(t, err)
	chatReq.Stream = false

	resp, err := provider.ChatCompletion(context.Background(), chatReq)
	require.NoError(t, err)

	messages := anthropicapi.FromChatResponse(resp)
	var order []string
	for _, block := range messages.Content {
		order = append(order, block.Type)
	}
	assert.Equal(t, []string{"thinking", "tool_use"}, order, "block order = %v", order)

	require.Len(t, messages.Content, 2)
	thinking := messages.Content[0]
	assert.Equal(t, "Check the city.", thinking.Thinking, "thinking block = %+v", thinking)
	assert.Equal(t, "get_weather", messages.Content[1].Name, "tool_use block = %+v", messages.Content[1])
}

// messagesBlockOrder reduces an Anthropic Messages SSE stream to the order in
// which its content blocks open, and to the deltas each block carries.
func messagesBlockOrder(t *testing.T, body string) ([]string, map[int][]string) {
	t.Helper()
	var order []string
	deltas := map[int][]string{}
	open := map[int]bool{}
	for line := range strings.SplitSeq(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var event map[string]any
		require.NoError(t, json.Unmarshal([]byte(data), &event))
		typ, _ := event["type"].(string)
		index := 0
		if raw, ok := event["index"].(float64); ok {
			index = int(raw)
		}
		switch typ {
		case "content_block_start":
			block, _ := event["content_block"].(map[string]any)
			kind, _ := block["type"].(string)
			order = append(order, "start:"+kind)
			open[index] = true
		case "content_block_delta":
			delta, _ := event["delta"].(map[string]any)
			kind, _ := delta["type"].(string)
			deltas[index] = append(deltas[index], kind)
			assert.True(t, open[index], "delta %q on block %d that is not open", kind, index)
		case "content_block_stop":
			open[index] = false
		}
	}
	return order, deltas
}

// TestStreamChatCompletion_InterleavedReasoningAndToolCall drives the whole
// chain a Claude Code client uses: an Anthropic Messages request, the chat
// translation, the Codex Responses upstream, and the Anthropic Messages SSE
// response.
//
// The upstream interleaves reasoning with the argument deltas of a function
// call. The client must see a valid stream: the tool_use block stays open
// until its arguments are complete, and the reasoning reaches the client as
// a thinking block instead of being dropped or landing on a closed block.
func TestStreamChatCompletion_InterleavedReasoningAndToolCall(t *testing.T) {
	srv, _ := providertest.SSEServer(t, codexInterleavedSSE)
	provider := newTestProvider(tokenWithAccount(t, "acct-1"), srv.URL, srv.Client(), llmclient.Hooks{})

	decoded, err := anthropicapi.DecodeMessagesRequest([]byte(messagesWithThinkingAndTool))
	require.NoError(t, err)
	chatReq, err := anthropicapi.ToChatRequest(decoded)
	require.NoError(t, err)

	stream, err := provider.StreamChatCompletion(context.Background(), chatReq)
	require.NoError(t, err)
	defer func() { _ = stream.Close() }()

	conv := anthropicapi.NewStreamConverter(stream, chatReq.Model, 100)
	defer func() { _ = conv.Close() }()
	body, err := io.ReadAll(conv)
	require.NoError(t, err)

	order, deltas := messagesBlockOrder(t, string(body))

	// The tool_use block opens first, and the reasoning that follows the
	// first argument delta opens a thinking block of its own.
	assert.Equal(t, []string{"start:tool_use", "start:thinking"}, order,
		"block order = %v", order)
	assert.Contains(t, deltas[0], "input_json_delta", "tool arguments = %v", deltas[0])
	assert.Contains(t, deltas[1], "thinking_delta", "thinking deltas = %v", deltas[1])

	// The reasoning text reaches the client.
	assert.Contains(t, string(body), "Check the city.", "the reasoning must reach the client")
	// Both argument halves are delivered.
	assert.Contains(t, string(body), `{\"ci`)
	assert.Contains(t, string(body), `ty\":\"Paris\"}`)
}
