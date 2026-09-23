package providers

import (
	"errors"
	"io"
	"strings"
	"testing"

	"encoding/json"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/streaming"
)

// responsesStream builds an SSE stream from JSON payloads, one data: line
// each. No event: lines are emitted: the converter must classify on the
// payload's type field alone.
func chatViaResponsesStreamOf(events ...string) string {
	var b strings.Builder
	for _, event := range events {
		b.WriteString("data: ")
		b.WriteString(event)
		b.WriteString("\n\n")
	}
	return b.String()
}

const (
	chatViaResponsesCreated = `{"type":"response.created","sequence_number":0,"response":{"id":"resp_abc123","object":"response","status":"in_progress","model":"gpt-5.1-codex","created_at":1700000000,"output":[]}}`
	chatViaResponsesMessage = `{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}}`
)

func chatViaResponsesTextStream() string {
	return chatViaResponsesStreamOf(
		chatViaResponsesCreated,
		`{"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_abc123","object":"response","status":"in_progress","model":"gpt-5.1-codex","created_at":1700000000,"output":[]}}`,
		chatViaResponsesMessage,
		`{"type":"response.content_part.added","sequence_number":3,"item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
		`{"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"Hello"}`,
		`{"type":"response.output_text.delta","sequence_number":5,"item_id":"msg_1","output_index":0,"content_index":0,"delta":" world"}`,
		`{"type":"response.output_text.done","sequence_number":6,"item_id":"msg_1","output_index":0,"content_index":0,"text":"Hello world"}`,
		`{"type":"response.content_part.done","sequence_number":7,"item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":"Hello world","annotations":[]}}`,
		`{"type":"response.output_item.done","sequence_number":8,"output_index":0,"item":{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello world","annotations":[]}]}}`,
		`{"type":"response.completed","sequence_number":9,"response":{"id":"resp_abc123","object":"response","status":"completed","model":"gpt-5.1-codex","created_at":1700000000,"output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello world","annotations":[]}]}],"usage":{"input_tokens":12,"output_tokens":5,"total_tokens":17,"input_tokens_details":{"cached_tokens":4},"output_tokens_details":{"reasoning_tokens":2}}}}`,
	)
}

func readChatViaResponsesStream(t *testing.T, stream string, includeUsage bool) ([]testSSEEvent, string, error) {
	t.Helper()
	reader := io.NopCloser(strings.NewReader(stream))
	converter := NewOpenAIChatStreamConverter(reader, "fallback-model", "test-provider", includeUsage)
	defer func() { _ = converter.Close() }()

	raw, err := io.ReadAll(converter)
	return parseTestSSEEvents(t, string(raw)), string(raw), err
}

// chatChunkDelta returns the delta object of a chunk's first choice.
func chatChunkDelta(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	choices, ok := payload["choices"].([]any)
	require.True(t, ok, "chunk has no choices array: %v", payload)
	require.NotEmpty(t, choices, "chunk has empty choices: %v", payload)
	choice, ok := choices[0].(map[string]any)
	require.True(t, ok)
	delta, ok := choice["delta"].(map[string]any)
	require.True(t, ok, "choice has no delta object: %v", choice)
	return delta
}

// chatChunkFinishReason returns the finish_reason of a chunk's first choice;
// nil means JSON null.
func chatChunkFinishReason(t *testing.T, payload map[string]any) any {
	t.Helper()
	choices, ok := payload["choices"].([]any)
	require.True(t, ok, "chunk has no choices array: %v", payload)
	require.NotEmpty(t, choices, "chunk has empty choices: %v", payload)
	choice, ok := choices[0].(map[string]any)
	require.True(t, ok)
	return choice["finish_reason"]
}

// chatChunkToolCalls returns the tool_calls array of a chunk's first delta.
func chatChunkToolCalls(t *testing.T, payload map[string]any) []any {
	t.Helper()
	calls, ok := chatChunkDelta(t, payload)["tool_calls"].([]any)
	require.True(t, ok, "delta has no tool_calls array: %v", payload)
	return calls
}

func assertStableChatChunkEnvelope(t *testing.T, events []testSSEEvent, raw string) {
	t.Helper()
	require.NotEmpty(t, events)
	id, ok := events[0].Payload["id"].(string)
	require.True(t, ok, "first chunk has no id")
	assert.True(t, strings.HasPrefix(id, "chatcmpl-"), "id %q lacks chatcmpl- prefix", id)
	created := events[0].Payload["created"]
	for _, event := range events {
		if event.Done {
			continue
		}
		assert.Equal(t, id, event.Payload["id"], "chunk id changed mid-stream")
		assert.Equal(t, created, event.Payload["created"], "created changed mid-stream")
		assert.Equal(t, "chat.completion.chunk", event.Payload["object"])
	}
	assert.NotContains(t, raw, "resp_abc123", "upstream resp_ id leaked into chat chunks")
}

func TestOpenAIChatStreamConverter_TextDeltas(t *testing.T) {
	events, raw, err := readChatViaResponsesStream(t, chatViaResponsesTextStream(), false)
	require.NoError(t, err)
	// role chunk + two content chunks + finish chunk + [DONE]; the terminal
	// event's aggregated output must not be re-emitted as a third content
	// chunk.
	require.Len(t, events, 5)

	role := chatChunkDelta(t, events[0].Payload)
	assert.Equal(t, "assistant", role["role"])
	assert.Len(t, role, 1)
	assert.Nil(t, chatChunkFinishReason(t, events[0].Payload))

	assert.Equal(t, "Hello", chatChunkDelta(t, events[1].Payload)["content"])
	assert.Equal(t, " world", chatChunkDelta(t, events[2].Payload)["content"])

	finish := events[3].Payload
	assert.Equal(t, "stop", chatChunkFinishReason(t, finish))
	assert.Empty(t, chatChunkDelta(t, finish))

	assert.True(t, events[4].Done)

	assertStableChatChunkEnvelope(t, events, raw)
	// model and created came from the response.created payload, not the
	// constructor fallback.
	assert.Equal(t, "gpt-5.1-codex", events[0].Payload["model"])
	assert.Equal(t, float64(1700000000), events[0].Payload["created"])
	assert.Equal(t, "test-provider", events[0].Payload["provider"])
	// includeUsage was false: no chunk carries a usage object.
	for _, event := range events {
		assert.Nil(t, event.Payload["usage"])
	}
}

func TestOpenAIChatStreamConverter_SingleToolCall(t *testing.T) {
	stream := chatViaResponsesStreamOf(
		chatViaResponsesCreated,
		`{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_abc","name":"get_weather","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":3,"item_id":"fc_1","output_index":0,"delta":"{\"city\":\"War"}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":4,"item_id":"fc_1","output_index":0,"delta":"saw\"}"}`,
		`{"type":"response.function_call_arguments.done","sequence_number":5,"item_id":"fc_1","output_index":0,"arguments":"{\"city\":\"Warsaw\"}"}`,
		`{"type":"response.output_item.done","sequence_number":6,"output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_abc","name":"get_weather","arguments":"{\"city\":\"Warsaw\"}"}}`,
		`{"type":"response.completed","sequence_number":7,"response":{"id":"resp_abc123","object":"response","status":"completed","model":"gpt-5.1-codex","created_at":1700000000,"output":[{"id":"fc_1","type":"function_call","call_id":"call_abc","name":"get_weather","arguments":"{\"city\":\"Warsaw\"}"}],"usage":{"input_tokens":9,"output_tokens":4,"total_tokens":13}}}`,
	)

	events, _, err := readChatViaResponsesStream(t, stream, false)
	require.NoError(t, err)
	// role + start chunk + two argument deltas + finish + [DONE]
	require.Len(t, events, 6)

	start := chatChunkToolCalls(t, events[1].Payload)
	require.Len(t, start, 1)
	call, ok := start[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(0), call["index"])
	assert.Equal(t, "call_abc", call["id"])
	assert.Equal(t, "function", call["type"])
	function, ok := call["function"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "get_weather", function["name"])
	assert.Equal(t, "", function["arguments"])

	first := chatChunkToolCalls(t, events[2].Payload)[0].(map[string]any)
	assert.Equal(t, float64(0), first["index"])
	assert.Nil(t, first["id"], "argument deltas must not repeat the id")
	assert.Equal(t, "{\"city\":\"War", first["function"].(map[string]any)["arguments"])

	second := chatChunkToolCalls(t, events[3].Payload)[0].(map[string]any)
	assert.Equal(t, float64(0), second["index"])
	assert.Equal(t, "saw\"}", second["function"].(map[string]any)["arguments"])

	assert.Equal(t, "tool_calls", chatChunkFinishReason(t, events[4].Payload))
	assert.True(t, events[5].Done)
}

func TestOpenAIChatStreamConverter_ParallelToolCalls(t *testing.T) {
	// Two function_call items whose argument deltas interleave; each delta
	// carries its item_id, and the converter must route them to the dense
	// chat indices assigned in arrival order.
	stream := chatViaResponsesStreamOf(
		chatViaResponsesCreated,
		`{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_a","name":"fn_a","arguments":""}}`,
		`{"type":"response.output_item.added","sequence_number":3,"output_index":1,"item":{"id":"fc_2","type":"function_call","call_id":"call_b","name":"fn_b","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":4,"item_id":"fc_2","output_index":1,"delta":"{\"b\":"}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":5,"item_id":"fc_1","output_index":0,"delta":"{\"a\":"}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":6,"item_id":"fc_2","output_index":1,"delta":"1}"}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":7,"item_id":"fc_1","output_index":0,"delta":"2}"}`,
		`{"type":"response.completed","sequence_number":8,"response":{"id":"resp_abc123","object":"response","status":"completed","model":"gpt-5.1-codex","created_at":1700000000,"output":[{"id":"fc_1","type":"function_call","call_id":"call_a","name":"fn_a","arguments":"{\"a\":2}"},{"id":"fc_2","type":"function_call","call_id":"call_b","name":"fn_b","arguments":"{\"b\":1}"}]}}`,
	)

	events, _, err := readChatViaResponsesStream(t, stream, false)
	require.NoError(t, err)
	// role + two start chunks + four argument deltas + finish + [DONE]
	require.Len(t, events, 9)

	startA := chatChunkToolCalls(t, events[1].Payload)[0].(map[string]any)
	assert.Equal(t, float64(0), startA["index"])
	assert.Equal(t, "call_a", startA["id"])
	assert.Equal(t, "fn_a", startA["function"].(map[string]any)["name"])

	startB := chatChunkToolCalls(t, events[2].Payload)[0].(map[string]any)
	assert.Equal(t, float64(1), startB["index"])
	assert.Equal(t, "call_b", startB["id"])
	assert.Equal(t, "fn_b", startB["function"].(map[string]any)["name"])

	wantIndices := []float64{1, 0, 1, 0}
	wantArgs := []string{"{\"b\":", "{\"a\":", "1}", "2}"}
	for i := range wantIndices {
		call := chatChunkToolCalls(t, events[3+i].Payload)[0].(map[string]any)
		assert.Equal(t, wantIndices[i], call["index"], "argument delta %d routed to the wrong tool call", i)
		assert.Equal(t, wantArgs[i], call["function"].(map[string]any)["arguments"])
	}

	assert.Equal(t, "tool_calls", chatChunkFinishReason(t, events[7].Payload))
	assert.True(t, events[8].Done)
}

func TestOpenAIChatStreamConverter_ReasoningThenText(t *testing.T) {
	stream := chatViaResponsesStreamOf(
		chatViaResponsesCreated,
		`{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}`,
		`{"type":"response.reasoning_summary_text.delta","sequence_number":3,"item_id":"rs_1","output_index":0,"summary_index":0,"delta":"thinking "}`,
		`{"type":"response.reasoning_text.delta","sequence_number":4,"item_id":"rs_1","output_index":0,"content_index":0,"delta":"hard"}`,
		`{"type":"response.output_item.done","sequence_number":5,"output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"thinking hard"}]}}`,
		`{"type":"response.output_item.added","sequence_number":6,"output_index":1,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
		`{"type":"response.output_text.delta","sequence_number":7,"item_id":"msg_1","output_index":1,"content_index":0,"delta":"Answer"}`,
		`{"type":"response.completed","sequence_number":8,"response":{"id":"resp_abc123","object":"response","status":"completed","model":"gpt-5.1-codex","created_at":1700000000,"output":[{"id":"rs_1","type":"reasoning"},{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Answer","annotations":[]}]}]}}`,
	)

	events, _, err := readChatViaResponsesStream(t, stream, false)
	require.NoError(t, err)
	// role + two reasoning deltas + one content delta + finish + [DONE]
	require.Len(t, events, 6)

	assert.Equal(t, "thinking ", chatChunkDelta(t, events[1].Payload)["reasoning_content"])
	assert.Equal(t, "hard", chatChunkDelta(t, events[2].Payload)["reasoning_content"])
	assert.Equal(t, "Answer", chatChunkDelta(t, events[3].Payload)["content"])
	assert.Equal(t, "stop", chatChunkFinishReason(t, events[4].Payload))
	assert.True(t, events[5].Done)
}

func TestOpenAIChatStreamConverter_RefusalDeltas(t *testing.T) {
	stream := chatViaResponsesStreamOf(
		chatViaResponsesCreated,
		chatViaResponsesMessage,
		`{"type":"response.content_part.added","sequence_number":3,"item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"refusal","refusal":""}}`,
		`{"type":"response.refusal.delta","sequence_number":4,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"I can't"}`,
		`{"type":"response.refusal.delta","sequence_number":5,"item_id":"msg_1","output_index":0,"content_index":0,"delta":" help with that"}`,
		`{"type":"response.completed","sequence_number":6,"response":{"id":"resp_abc123","object":"response","status":"completed","model":"gpt-5.1-codex","created_at":1700000000,"output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"refusal","refusal":"I can't help with that"}]}]}}`,
	)

	events, _, err := readChatViaResponsesStream(t, stream, false)
	require.NoError(t, err)
	require.Len(t, events, 5)

	assert.Equal(t, "I can't", chatChunkDelta(t, events[1].Payload)["refusal"])
	assert.Equal(t, " help with that", chatChunkDelta(t, events[2].Payload)["refusal"])
	// A refusal is not content.
	assert.Nil(t, chatChunkDelta(t, events[1].Payload)["content"])
	assert.Equal(t, "stop", chatChunkFinishReason(t, events[3].Payload))
	assert.True(t, events[4].Done)
}

func TestOpenAIChatStreamConverter_FinishReasons(t *testing.T) {
	tests := []struct {
		name     string
		terminal string
		want     any // expected finish_reason; nil means JSON null
	}{
		{
			name:     "completed text",
			terminal: `{"type":"response.completed","response":{"id":"resp_abc123","status":"completed","output":[{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"hi"}]}]}}`,
			want:     "stop",
		},
		{
			name:     "completed with function call",
			terminal: `{"type":"response.completed","response":{"id":"resp_abc123","status":"completed","output":[{"id":"fc_1","type":"function_call","call_id":"call_a","name":"fn_a","arguments":"{}"}]}}`,
			want:     "tool_calls",
		},
		{
			name:     "incomplete max_output_tokens",
			terminal: `{"type":"response.incomplete","response":{"id":"resp_abc123","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"id":"msg_1","type":"message"}]}}`,
			want:     "length",
		},
		{
			name:     "incomplete content_filter",
			terminal: `{"type":"response.incomplete","response":{"id":"resp_abc123","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}}`,
			want:     "content_filter",
		},
		{
			name:     "incomplete reason without chat equivalent",
			terminal: `{"type":"response.incomplete","response":{"id":"resp_abc123","status":"incomplete","incomplete_details":{"reason":"max_messages"},"output":[]}}`,
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := chatViaResponsesStreamOf(chatViaResponsesCreated, tt.terminal)
			events, _, err := readChatViaResponsesStream(t, stream, false)
			require.NoError(t, err)
			// role chunk + finish chunk + [DONE]
			require.Len(t, events, 3)
			if tt.want == nil {
				assert.Nil(t, chatChunkFinishReason(t, events[1].Payload))
			} else {
				assert.Equal(t, tt.want, chatChunkFinishReason(t, events[1].Payload))
			}
			assert.True(t, events[2].Done)
		})
	}
}

func TestOpenAIChatStreamConverter_RoleChunkWithoutCreated(t *testing.T) {
	// No response.created and no deltas: the terminal event alone must still
	// emit the role chunk ahead of the finish chunk.
	stream := chatViaResponsesStreamOf(
		`{"type":"response.completed","response":{"id":"resp_abc123","status":"completed","output":[{"id":"msg_1","type":"message"}]}}`,
	)

	events, _, err := readChatViaResponsesStream(t, stream, false)
	require.NoError(t, err)
	// role chunk + finish chunk + [DONE]
	require.Len(t, events, 3)
	assert.Equal(t, "assistant", chatChunkDelta(t, events[0].Payload)["role"])
	assert.Equal(t, "stop", chatChunkFinishReason(t, events[1].Payload))
	assert.True(t, events[2].Done)
}

func TestOpenAIChatStreamConverter_UsageChunk(t *testing.T) {
	t.Run("emitted when includeUsage", func(t *testing.T) {
		events, _, err := readChatViaResponsesStream(t, chatViaResponsesTextStream(), true)
		require.NoError(t, err)
		// role + two content chunks + finish + usage chunk + [DONE]
		require.Len(t, events, 6)

		usageChunk := events[4].Payload
		choices, ok := usageChunk["choices"].([]any)
		require.True(t, ok)
		assert.Empty(t, choices, "usage chunk must carry an empty choices array")

		usage, ok := usageChunk["usage"].(map[string]any)
		require.True(t, ok, "usage chunk carries no usage object")
		assert.Equal(t, float64(12), usage["prompt_tokens"])
		assert.Equal(t, float64(5), usage["completion_tokens"])
		assert.Equal(t, float64(17), usage["total_tokens"])
		assert.Nil(t, usage["input_tokens"], "Responses field names must not leak")
		assert.Nil(t, usage["output_tokens"])
		promptDetails, ok := usage["prompt_tokens_details"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, float64(4), promptDetails["cached_tokens"])
		completionDetails, ok := usage["completion_tokens_details"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, float64(2), completionDetails["reasoning_tokens"])

		assert.True(t, events[5].Done)
	})

	t.Run("omitted without includeUsage", func(t *testing.T) {
		events, _, err := readChatViaResponsesStream(t, chatViaResponsesTextStream(), false)
		require.NoError(t, err)
		for _, event := range events {
			assert.Nil(t, event.Payload["usage"])
		}
	})
}

func TestOpenAIChatStreamConverter_FailedMidStream(t *testing.T) {
	stream := chatViaResponsesStreamOf(
		chatViaResponsesCreated,
		chatViaResponsesMessage,
		`{"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"partial"}`,
		`{"type":"response.failed","sequence_number":4,"response":{"id":"resp_abc123","object":"response","status":"failed","model":"gpt-5.1-codex","created_at":1700000000,"output":[],"error":{"code":"server_error","message":"boom"}}}`,
	)

	events, raw, err := readChatViaResponsesStream(t, stream, false)
	require.Error(t, err)
	assert.True(t, errors.Is(err, streaming.ErrStreamIncomplete), "err = %v", err)

	// role + content delta + in-band error; no finish chunk, no [DONE].
	require.Len(t, events, 3)
	assert.Equal(t, "partial", chatChunkDelta(t, events[1].Payload)["content"])

	errorPayload, ok := events[2].Payload["error"].(map[string]any)
	require.True(t, ok, "expected an in-band error event, got %v", events[2].Payload)
	assert.Equal(t, "boom", errorPayload["message"])
	assert.Equal(t, "server_error", errorPayload["code"])

	for _, event := range events {
		assert.False(t, event.Done, "a failed stream must not end with [DONE]")
	}
	assert.NotContains(t, raw, `"finish_reason":"stop"`)
}

func TestOpenAIChatStreamConverter_TopLevelErrorEvent(t *testing.T) {
	stream := chatViaResponsesStreamOf(
		chatViaResponsesCreated,
		`{"type":"error","sequence_number":1,"code":"rate_limit_error","message":"slow down","param":null}`,
	)

	events, _, err := readChatViaResponsesStream(t, stream, false)
	require.Error(t, err)
	assert.True(t, errors.Is(err, streaming.ErrStreamIncomplete), "err = %v", err)

	require.Len(t, events, 2)
	errorPayload, ok := events[1].Payload["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "slow down", errorPayload["message"])
	assert.Equal(t, "rate_limit_error", errorPayload["code"])
	assert.False(t, events[1].Done)
}

func TestOpenAIChatStreamConverter_EndsWithoutTerminalEvent(t *testing.T) {
	stream := chatViaResponsesStreamOf(
		chatViaResponsesCreated,
		chatViaResponsesMessage,
		`{"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"truncated"}`,
	)

	events, raw, err := readChatViaResponsesStream(t, stream, false)
	require.Error(t, err)
	assert.True(t, errors.Is(err, streaming.ErrStreamIncomplete), "err = %v", err)

	// role + content delta + in-band truncation error; never a stop finish.
	require.Len(t, events, 3)
	assert.Equal(t, "truncated", chatChunkDelta(t, events[1].Payload)["content"])

	errorPayload, ok := events[2].Payload["error"].(map[string]any)
	require.True(t, ok, "expected an in-band error event, got %v", events[2].Payload)
	assert.Equal(t, "stream_incomplete", errorPayload["code"])
	assert.Equal(t, streaming.ErrStreamIncomplete.Error(), errorPayload["message"])

	assert.NotContains(t, raw, `"finish_reason":"stop"`)
	assert.NotContains(t, raw, "[DONE]")
}

// TestOpenAIChatStreamConverter_DataOnlyParsing feeds CRLF framing and SSE
// event: lines; classification must come from the payload's type field, so
// the output matches the plain-LF, data-only stream exactly.
func TestOpenAIChatStreamConverter_DataOnlyParsing(t *testing.T) {
	plain := chatViaResponsesTextStream()
	var decorated strings.Builder
	for _, block := range strings.Split(strings.TrimRight(plain, "\n"), "\n\n") {
		payload := strings.TrimPrefix(block, "data: ")
		var head struct {
			Type string `json:"type"`
		}
		require.NoError(t, json.Unmarshal([]byte(payload), &head))
		decorated.WriteString("event: " + head.Type + "\r\n" + block + "\r\n\r\n")
	}

	plainEvents, _, err := readChatViaResponsesStream(t, plain, false)
	require.NoError(t, err)
	crlfEvents, _, err := readChatViaResponsesStream(t, decorated.String(), false)
	require.NoError(t, err)

	require.Equal(t, len(plainEvents), len(crlfEvents))
	for i := range plainEvents {
		assert.Equal(t, plainEvents[i].Done, crlfEvents[i].Done)
		if plainEvents[i].Done {
			continue
		}
		assert.Equal(t, plainEvents[i].Payload["choices"], crlfEvents[i].Payload["choices"], "chunk %d differs under CRLF framing", i)
	}
}
