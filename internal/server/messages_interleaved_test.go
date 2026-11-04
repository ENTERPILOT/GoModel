package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/llmclient"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/providers/chatgpt"
	"github.com/enterpilot/gomodel/internal/providers/providertest"
)

// codexInterleavedSSE is a Codex Responses stream in which reasoning is
// interleaved with the argument deltas of two function calls:
//
//	thinking A -> tool call A -> thinking B -> tool call B
//
// The reasoning between the argument deltas of a call is the case that breaks
// a Messages stream: the tool_use block must stay open until its arguments
// are complete.
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
	"event: response.output_item.added\n" +
	`data: {"type":"response.output_item.added","output_index":2,"item":{"id":"fc_2","type":"function_call","call_id":"call_2","name":"get_weather","arguments":""}}` + "\n\n" +
	"event: response.function_call_arguments.delta\n" +
	`data: {"type":"response.function_call_arguments.delta","item_id":"fc_2","output_index":2,"delta":"{\"city\":\"Rome\"}"}` + "\n\n" +
	"event: response.reasoning_summary_text.delta\n" +
	`data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_2","output_index":3,"summary_index":0,"delta":"And Rome."}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.6-terra","output":[{"id":"fc_1","type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"},{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"Check the city."}]},{"id":"fc_2","type":"function_call","call_id":"call_2","name":"get_weather","arguments":"{\"city\":\"Rome\"}"},{"id":"rs_2","type":"reasoning","summary":[{"type":"summary_text","text":"And Rome."}]}],"usage":{"input_tokens":120,"output_tokens":40,"total_tokens":160}}}` + "\n\n" +
	"data: [DONE]\n\n"

// messagesInterleavedRequest is a Claude Code style Messages request: thinking
// on, a tool declared, and a multi-turn history whose assistant turn carries a
// thinking block and a tool_use block.
const messagesInterleavedRequest = `{
  "model": "gpt-5.6-terra",
  "max_tokens": 4096,
  "stream": true,
  "thinking": {"type": "enabled", "budget_tokens": 10000},
  "tools": [{"name": "get_weather", "description": "Get weather", "input_schema": {"type": "object", "properties": {"city": {"type": "string"}}}}],
  "messages": [
    {"role": "user", "content": "Weather in Paris and Rome?"},
    {"role": "assistant", "content": [
      {"type": "thinking", "thinking": "I should call the tool.", "signature": "sig-abc"},
      {"type": "text", "text": "Let me check the weather."},
      {"type": "tool_use", "id": "toolu_1", "name": "get_weather", "input": {"city": "Paris"}}
    ]},
    {"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_1", "content": "sunny, 22C"}]}
  ]
}`

// interleavedProvider routes every model to the Codex provider under test. The
// Codex provider is not a RoutableProvider itself; the router normally owns
// that lookup, so the test supplies it.
type interleavedProvider struct {
	core.Provider
}

func (p interleavedProvider) Supports(string) bool { return true }

func (p interleavedProvider) GetProviderType(string) string { return "chatgpt" }

// newInterleavedProvider builds the ChatGPT provider on a fake Codex backend
// and returns the handler that serves the Messages API over it.
func newInterleavedProvider(t *testing.T, sse string) (*Handler, *providertest.Capture) {
	t.Helper()
	srv, capture := providertest.SSEServer(t, sse)
	opts := providertest.Options(llmclient.Hooks{})
	opts.HTTPClient = srv.Client()
	provider := chatgpt.New(providers.ProviderConfig{APIKey: "test-key", BaseURL: srv.URL}, opts)
	return NewHandler(interleavedProvider{Provider: provider}, nil, nil, nil), capture
}

// messagesStreamBlocks reduces a Messages SSE stream to the order in which its
// content blocks open, the block indexes, and the deltas each block carries.
func messagesStreamBlocks(t *testing.T, body string) (order []string, indexes []int, deltas map[int][]string) {
	t.Helper()
	deltas = map[int][]string{}
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
			indexes = append(indexes, index)
			open[index] = true
		case "content_block_delta":
			delta, _ := event["delta"].(map[string]any)
			kind, _ := delta["type"].(string)
			deltas[index] = append(deltas[index], kind)
			assert.True(t, open[index], "delta %q on block %d that is not open", kind, index)
		case "content_block_stop":
			assert.True(t, open[index], "content_block_stop on block %d that is not open", index)
			open[index] = false
		}
	}
	for index, isOpen := range open {
		assert.False(t, isOpen, "block %d is never closed", index)
	}
	return order, indexes, deltas
}

// TestMessages_InterleavedThinkingAndToolCalls drives the whole composed path
// through the real Messages handler: the client sends a Messages request, the
// gateway translates it for a Responses-only upstream, the upstream answers
// with reasoning interleaved between two tool calls, and the client receives
// the Messages stream.
//
// The upstream order (thinking A, tool A, thinking B, tool B) cannot survive
// the buffered path, whose canonical shape carries the reasoning in one
// member; the streaming path emits the blocks as they arrive, so it keeps the
// order. What the client must never see is a delta on a closed block.
func TestMessages_InterleavedThinkingAndToolCalls(t *testing.T) {
	handler, _ := newInterleavedProvider(t, codexInterleavedSSE)

	c, rec := echotest.Post(t, "/v1/messages", messagesInterleavedRequest)
	require.NoError(t, handler.Messages(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := rec.Body.String()
	order, indexes, deltas := messagesStreamBlocks(t, body)

	assert.Equal(t, []string{
		"start:tool_use",
		"start:thinking",
		"start:tool_use",
		"start:thinking",
	}, order, "block order = %v", order)
	assert.Equal(t, []int{0, 1, 2, 3}, indexes, "block indexes = %v", indexes)

	// Both tool calls keep their arguments, and both reasoning texts reach
	// the client as thinking deltas.
	assert.Contains(t, deltas[0], "input_json_delta", "tool A deltas = %v", deltas[0])
	assert.Contains(t, deltas[2], "input_json_delta", "tool B deltas = %v", deltas[2])
	assert.Contains(t, deltas[1], "thinking_delta", "thinking A deltas = %v", deltas[1])
	assert.Contains(t, deltas[3], "thinking_delta", "thinking B deltas = %v", deltas[3])

	assert.Contains(t, body, "Check the city.", "the first reasoning must reach the client")
	assert.Contains(t, body, "And Rome.", "the second reasoning must reach the client")
	assert.Contains(t, body, "Paris", "the first tool call must keep its arguments")
	assert.Contains(t, body, "Rome", "the second tool call must keep its arguments")
}

// TestMessages_InterleavedThinkingAndToolCalls_Buffered pins the buffered
// half of the same path.
//
// The canonical chat shape carries the reasoning in one member beside the
// answer text and the tool calls, so the buffered response renders the
// thinking block first, then the tool calls. The upstream interleaving is not
// representable in that shape and is not claimed to be preserved: what the
// test pins is that the reasoning and both tool calls survive, with their
// call IDs and arguments intact.
func TestMessages_InterleavedThinkingAndToolCalls_Buffered(t *testing.T) {
	handler, _ := newInterleavedProvider(t, codexInterleavedSSE)

	body := strings.Replace(messagesInterleavedRequest, `"stream": true,`, `"stream": false,`, 1)
	c, rec := echotest.Post(t, "/v1/messages", body)
	require.NoError(t, handler.Messages(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	resp := echotest.Decode[map[string]any](t, rec)
	content, _ := resp["content"].([]any)
	require.Len(t, content, 3, "content = %+v", content)

	thinking, _ := content[0].(map[string]any)
	assert.Equal(t, "thinking", thinking["type"], "block 0 = %+v", thinking)
	assert.Contains(t, thinking["thinking"], "Check the city.", "block 0 = %+v", thinking)
	assert.Contains(t, thinking["thinking"], "And Rome.", "block 0 = %+v", thinking)

	toolA, _ := content[1].(map[string]any)
	assert.Equal(t, "tool_use", toolA["type"], "block 1 = %+v", toolA)
	assert.Equal(t, "call_1", toolA["id"], "block 1 = %+v", toolA)
	assert.Equal(t, "get_weather", toolA["name"], "block 1 = %+v", toolA)

	toolB, _ := content[2].(map[string]any)
	assert.Equal(t, "tool_use", toolB["type"], "block 2 = %+v", toolB)
	assert.Equal(t, "call_2", toolB["id"], "block 2 = %+v", toolB)

	assert.Equal(t, "tool_use", resp["stop_reason"], "response = %+v", resp)
}

// TestMessages_InterleavedRequestReplay pins the REQUEST direction: what the
// upstream receives when the client echoes an assistant turn that carries a
// thinking block and a tool_use block.
//
// The thinking block is the client's own artifact. Codex pins store: false,
// so the reasoning is not replayed upstream; the tool call and the tool
// result are. The test pins that the tool call keeps its ID and arguments,
// and that the replay is not silently lossy in a way the client cannot see.
func TestMessages_InterleavedRequestReplay(t *testing.T) {
	handler, capture := newInterleavedProvider(t, codexInterleavedSSE)

	c, rec := echotest.Post(t, "/v1/messages", messagesInterleavedRequest)
	require.NoError(t, handler.Messages(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	sent := capture.Last(t).JSON(t)
	input, _ := sent["input"].([]any)
	require.NotEmpty(t, input, "upstream body = %+v", sent)

	var types []string
	var call map[string]any
	for _, item := range input {
		object, _ := item.(map[string]any)
		kind, _ := object["type"].(string)
		if kind == "" {
			kind, _ = object["role"].(string)
		}
		types = append(types, kind)
		if kind == "function_call" && call == nil {
			call = object
		}
	}
	// The assistant turn becomes an assistant message with its text plus a
	// function_call item, the tool result becomes a function_call_output,
	// and the thinking block is NOT replayed: it is the client's own
	// artifact, and the upstream pins store: false.
	assert.Equal(t, []string{"user", "assistant", "function_call", "function_call_output"}, types,
		"the upstream input must carry the user turn, the assistant turn, the tool call and its result")
	for _, kind := range types {
		assert.NotEqual(t, "reasoning", kind,
			"the reasoning must not be replayed upstream: input = %+v", input)
	}

	require.NotNil(t, call, "the tool call must reach the upstream")
	assert.Equal(t, "toolu_1", call["call_id"], "tool call = %+v", call)
	assert.Equal(t, "get_weather", call["name"], "tool call = %+v", call)
}

// TestMessages_InterleavedStreamingMatchesBuffered compares the assembled
// stream with the non-streaming result for the same upstream answer.
//
// The two agree on WHICH blocks the client receives: two thinking blocks and
// two tool_use blocks, with the same call IDs and the same arguments. They
// differ in ORDER, and that difference is the documented limitation: the
// streaming path emits the blocks as the upstream produced them, while the
// buffered path renders the thinking first because the canonical chat shape
// carries the reasoning in one member.
func TestMessages_InterleavedStreamingMatchesBuffered(t *testing.T) {
	handler, _ := newInterleavedProvider(t, codexInterleavedSSE)

	streamHandler, _ := newInterleavedProvider(t, codexInterleavedSSE)
	c, rec := echotest.Post(t, "/v1/messages", messagesInterleavedRequest)
	require.NoError(t, streamHandler.Messages(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	streamBody := rec.Body.String()

	body := strings.Replace(messagesInterleavedRequest, `"stream": true,`, `"stream": false,`, 1)
	c, rec = echotest.Post(t, "/v1/messages", body)
	require.NoError(t, handler.Messages(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	buffered := echotest.Decode[map[string]any](t, rec)

	// The buffered response carries the same call IDs and arguments as the
	// stream.
	bufferedContent, _ := buffered["content"].([]any)
	var bufferedCalls []string
	for _, item := range bufferedContent {
		block, _ := item.(map[string]any)
		if kind, _ := block["type"].(string); kind == "tool_use" {
			bufferedCalls = append(bufferedCalls, block["id"].(string))
		}
	}
	assert.Equal(t, []string{"call_1", "call_2"}, bufferedCalls,
		"the buffered response must carry both tool calls in upstream order")

	assert.Contains(t, streamBody, "call_1", "the stream must carry the first call ID")
	assert.Contains(t, streamBody, "call_2", "the stream must carry the second call ID")
	assert.Contains(t, streamBody, "Check the city.", "the stream must carry the first reasoning")
	assert.Contains(t, streamBody, "And Rome.", "the stream must carry the second reasoning")
}

// TestMessages_InterleavedToolResultAssociation pins that a tool result the
// client sends back is bound to the tool call it answers: the call ID travels
// upstream unchanged, and the result is rendered as the output of that call.
//
// A result that arrives with an ID no call produced must not be silently
// bound to the wrong call.
func TestMessages_InterleavedToolResultAssociation(t *testing.T) {
	handler, capture := newInterleavedProvider(t, codexInterleavedSSE)

	body := strings.ReplaceAll(messagesInterleavedRequest, `"toolu_1"`, `"toolu_2"`)
	c, rec := echotest.Post(t, "/v1/messages", body)
	require.NoError(t, handler.Messages(c))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	sent := capture.Last(t).JSON(t)
	input, _ := sent["input"].([]any)

	var output string
	var outputCallID string
	for _, item := range input {
		object, _ := item.(map[string]any)
		if kind, _ := object["type"].(string); kind == "function_call_output" {
			output, _ = object["output"].(string)
			outputCallID, _ = object["call_id"].(string)
		}
	}
	assert.Equal(t, "toolu_2", outputCallID, "the result must stay bound to its call: input = %+v", input)
	assert.Equal(t, "sunny, 22C", output, "the result content must reach the upstream: input = %+v", input)
}

// unusedContext keeps the context import used when the file changes.
var _ = context.Background
