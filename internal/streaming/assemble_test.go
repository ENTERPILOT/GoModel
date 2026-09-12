package streaming

import (
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func chatFixtureResponses() map[string]*core.ChatResponse {
	return map[string]*core.ChatResponse{
		"text": {
			ID: "chatcmpl-1", Object: "chat.completion", Model: "gpt-4o", Provider: "openai", SystemFingerprint: "fp_1", Created: 1700000000,
			Choices: []core.Choice{{Index: 0, Message: core.ResponseMessage{Role: "assistant", Content: "Hello \"world\"\nline two"}, FinishReason: "stop"}},
			Usage:   core.Usage{PromptTokens: 5, CompletionTokens: 7, TotalTokens: 12},
		},
		"tool call": {
			ID: "chatcmpl-2", Object: "chat.completion", Model: "gpt-4o", Provider: "openai", Created: 1700000001,
			Choices: []core.Choice{{Index: 0, Message: core.ResponseMessage{Role: "assistant", ToolCalls: []core.ToolCall{
				{ID: "call_1", Type: "function", Function: core.FunctionCall{Name: "get_weather", Arguments: `{"city":"Paris"}`}},
				{ID: "call_2", Type: "function", Function: core.FunctionCall{Name: "get_time", Arguments: `{}`}},
			}}, FinishReason: "tool_calls"}},
			Usage: core.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
		},
		"multi choice with reasoning": {
			ID: "chatcmpl-3", Object: "chat.completion", Model: "r1", Provider: "deepseek", Created: 1700000002,
			Choices: []core.Choice{
				{Index: 0, Message: core.ResponseMessage{Role: "assistant", Content: "A", ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"reasoning_content": json.RawMessage(`"because"`)})}, FinishReason: "stop"},
				{Index: 1, Message: core.ResponseMessage{Role: "assistant", Content: "B"}, FinishReason: "length"},
			},
			Usage: core.Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
		},
	}
}

func TestChatStreamRoundTrip(t *testing.T) {
	for name, resp := range chatFixtureResponses() {
		t.Run(name, func(t *testing.T) {
			stream := SynthesizeChatStream(resp, true)
			require.True(t, strings.HasSuffix(string(stream), "data: [DONE]\n\n"), "stream must end with [DONE]:\n%s", stream)

			got, err := AssembleChatResponse(decodeChatEvents(t, stream))
			require.NoError(t, err)

			wantJSON, _ := json.Marshal(resp)
			gotJSON, _ := json.Marshal(got)
			assert.Equal(t, decodeMap(t, gotJSON), decodeMap(t, wantJSON), "round trip mismatch:\n got %s\nwant %s", gotJSON, wantJSON)
		})
	}
}

func TestSynthesizeChatStream_Shape(t *testing.T) {
	resp := chatFixtureResponses()["tool call"]
	events := decodeStreamEvents(t, SynthesizeChatStream(resp, false))
	// role, two tool calls, finish; no usage chunk.
	require.Len(t, events, 4)

	for i, ev := range events {
		assert.Equal(t, "chat.completion.chunk", ev["object"])
		assert.Equal(t, "chatcmpl-2", ev["id"])
		assert.Equal(t, "openai", ev["provider"], "chunk %d envelope = %v", i, ev)
		_, ok := ev["usage"]
		assert.False(t, ok, "chunk %d carries usage without include_usage", i)
	}
	first := events[0]["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
	assert.Equal(t, "assistant", first["role"], "first chunk delta = %v", first)

	last := events[3]["choices"].([]any)[0].(map[string]any)
	assert.Equal(t, "tool_calls", last["finish_reason"], "last chunk = %v", last)
}

func TestAssembleChatResponse_ProviderShapes(t *testing.T) {
	tests := []struct {
		name    string
		stream  string
		want    string
		finish  string
		wantErr bool
	}{
		{
			name: "content with finish on the same chunk and missing index",
			stream: `data: {"id":"x","choices":[{"delta":{"content":"a"}}]}` + "\n\n" +
				`data: {"id":"x","choices":[{"delta":{"content":"b"},"finish_reason":"stop"}]}` + "\n\n",
			want:   "ab",
			finish: "stop",
		},
		{
			name:    "no decodable payload",
			stream:  "data: [DONE]\n\n",
			wantErr: true,
		},
		{
			name:   "unparseable chunks are skipped",
			stream: "data: nope\n\n" + `data: {"choices":[{"index":0,"delta":{"content":"ok"}}]}` + "\n\n",
			want:   "ok",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := AssembleChatResponse(decodeChatEvents(t, []byte(tt.stream)))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, resp.Choices[0].Message.Content)
			assert.Equal(t, tt.finish, resp.Choices[0].FinishReason, "choice = %+v", resp.Choices[0])
		})
	}
}

func responsesFixtures() map[string]*core.ResponsesResponse {
	return map[string]*core.ResponsesResponse{
		"text": {
			ID: "resp_1", Object: "response", CreatedAt: 1700000000, Model: "gpt-4.1", Provider: "openai", Status: "completed",
			Output: []core.ResponsesOutputItem{{ID: "msg_1", Type: "message", Role: "assistant", Status: "completed",
				Content: []core.ResponsesContentItem{{Type: "output_text", Text: "Hello there"}}}},
			Usage: &core.ResponsesUsage{InputTokens: 3, OutputTokens: 4, TotalTokens: 7},
		},
		"function call and reasoning": {
			ID: "resp_2", Object: "response", CreatedAt: 1700000001, Model: "o4-mini", Provider: "openai", Status: "completed",
			Output: []core.ResponsesOutputItem{
				{ID: "rs_1", Type: "reasoning", Status: "completed", ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{"summary": json.RawMessage(`[]`)})},
				{ID: "fc_1", Type: "function_call", Status: "completed", CallID: "call_1", Name: "lookup", Arguments: `{"q":"x"}`},
			},
		},
		"incomplete": {
			ID: "resp_3", Object: "response", CreatedAt: 1, Model: "m", Provider: "p", Status: "incomplete",
			Output: []core.ResponsesOutputItem{{ID: "msg", Type: "message", Role: "assistant", Status: "incomplete",
				Content: []core.ResponsesContentItem{{Type: "output_text", Text: "partial"}}}},
		},
	}
}

func TestResponsesStreamRoundTrip(t *testing.T) {
	for name, resp := range responsesFixtures() {
		t.Run(name, func(t *testing.T) {
			stream := SynthesizeResponsesStream(resp)
			got, err := AssembleResponsesResponse(decodeResponsesEvents(t, stream))
			require.NoError(t, err)

			wantJSON, _ := json.Marshal(resp)
			gotJSON, _ := json.Marshal(got)
			assert.Equal(t, decodeMap(t, gotJSON), decodeMap(t, wantJSON), "round trip mismatch:\n got %s\nwant %s", gotJSON, wantJSON)
		})
	}
}

func TestSynthesizeResponsesStream_Shape(t *testing.T) {
	resp := responsesFixtures()["text"]
	stream := SynthesizeResponsesStream(resp)
	require.True(t, strings.HasSuffix(string(stream), "data: [DONE]\n\n"), "stream must end with [DONE]:\n%s", stream)

	events := decodeStreamEvents(t, stream)
	var types []string
	for i, ev := range events {
		types = append(types, ev["type"].(string))
		assert.Equal(t, float64(i), ev["sequence_number"], "event %d", i)
	}
	want := []string{
		"response.created", "response.in_progress",
		"response.output_item.added", "response.content_part.added",
		"response.output_text.delta", "response.output_text.done",
		"response.content_part.done", "response.output_item.done",
		"response.completed",
	}
	assert.Equal(t, want, types)
	assert.Equal(t, "Hello there", events[4]["delta"], "delta event = %v", events[4])

	created := events[0]["response"].(map[string]any)
	assert.Equal(t, "in_progress", created["status"])
	assert.Empty(t, created["output"].([]any), "response.created = %v", created)

	for _, raw := range scanAll(t, &EventScanner{}, string(stream)) {
		if raw.Comment || string(raw.Data) == "[DONE]" {
			continue
		}
		assert.NotEmpty(t, raw.Name, "event without event: line: %s", raw.Raw)
	}
}

func TestAssembleResponsesResponse_FromDeltasOnly(t *testing.T) {
	tests := []struct {
		name   string
		stream string
		text   string
		status string
	}{
		{
			name: "items without a terminal event",
			stream: "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"model\":\"m\",\"created_at\":5}}\n\n" +
				"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg\",\"type\":\"message\",\"role\":\"assistant\"}}\n\n" +
				"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"ab\"}\n\n" +
				"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"cd\"}\n\n",
			text:   "abcd",
			status: "incomplete",
		},
		{
			name:   "bare deltas become one message item",
			stream: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"y\"}\n\n",
			text:   "xy",
			status: "incomplete",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := AssembleResponsesResponse(decodeResponsesEvents(t, []byte(tt.stream)))
			require.NoError(t, err)
			assert.Equal(t, tt.status, resp.Status)
			assert.Len(t, resp.Output, 1)
			assert.Equal(t, tt.text, resp.Output[0].Content[0].Text, "assembled = %+v", resp)
		})
	}
	_, err := AssembleResponsesResponse(nil)
	assert.Equal(t, ErrNoEvents, err)
}

// content_index is provider JSON: a negative value must not panic and a huge
// one must not drive allocation; the text lands in the nearest part.
func TestAppendOutputText_BoundsContentIndex(t *testing.T) {
	item := &core.ResponsesOutputItem{Type: "message"}
	appendOutputText(item, -1, "neg")
	require.Len(t, item.Content, 1)
	require.Equal(t, "neg", item.Content[0].Text)

	appendOutputText(item, 1<<30, "far")
	require.Equal(t, maxAssembledContentParts, len(item.Content))
	require.Equal(t, "far", item.Content[len(item.Content)-1].Text)

	appendOutputText(item, 1, "one")
	require.Equal(t, "one", item.Content[1].Text, "after index 1: %+v", item.Content[:2])
}

// A synthesized stream is indistinguishable from a real one to the client, so
// it must carry the provider replay state the response holds; without it an
// Anthropic thinking turn cannot be continued.
func TestSynthesizeChatStream_CarriesReplayState(t *testing.T) {
	const replay = `{"anthropic":{"thinking_blocks":[{"type":"thinking","thinking":"hm","signature":"sig-1"}]}}`
	resp := &core.ChatResponse{
		ID:    "chatcmpl-3",
		Model: "claude-sonnet-4-5",
		Choices: []core.Choice{{
			Index: 0,
			Message: core.ResponseMessage{
				Role:    "assistant",
				Content: "done",
				ExtraFields: core.UnknownJSONFieldsFromMap(map[string]json.RawMessage{
					"reasoning_content":    json.RawMessage(`"hm"`),
					core.ExtraContentField: json.RawMessage(replay),
				}),
			},
			FinishReason: "stop",
		}},
	}

	var got string
	extraAt, textAt := -1, -1
	for i, event := range decodeStreamEvents(t, SynthesizeChatStream(resp, false)) {
		delta := event["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)
		if extra, ok := delta[core.ExtraContentField]; ok {
			encoded, _ := json.Marshal(extra)
			got = string(encoded)
			if extraAt < 0 {
				extraAt = i
			}
		}
		if _, ok := delta["content"]; ok && textAt < 0 {
			textAt = i
		}
	}
	require.NotEmpty(t, got)

	// The Anthropic Messages converter closes the thinking block when the
	// first text arrives, so a signature that came after it would be dropped.
	if textAt >= 0 && extraAt > textAt {
		t.Errorf("extra_content at chunk %d, first content at %d; want the replay state first", extraAt, textAt)
	}
	var want, have any
	err := json.Unmarshal([]byte(replay), &want)
	require.NoError(t, err)
	err = json.Unmarshal([]byte(got), &have)
	require.NoError(t, err)
	assert.Equal(t, want, have)
}

// A buffered plugin run assembles the stream, hands the response to the
// plugin, and re-synthesizes it. Replay state has to survive that round trip
// or a response-phase plugin silently strips the Anthropic thinking signature
// from an otherwise untouched turn.
func TestAssembleChatResponse_KeepsReplayState(t *testing.T) {
	const replay = `{"anthropic":{"thinking_blocks":[{"type":"thinking","thinking":"hm","signature":"sig-1"}]}}`
	stream := strings.Join([]string{
		`data: {"id":"chatcmpl-4","model":"claude-sonnet-4-5","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"hm"}}]}`,
		`data: {"choices":[{"index":0,"delta":{"extra_content":` + replay + `}}]}`,
		`data: {"choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`,
		"",
	}, "\n\n")

	resp, err := AssembleChatResponse(decodeChatEvents(t, []byte(stream)))
	require.NoError(t, err)

	got := resp.Choices[0].Message.ExtraFields.Lookup(core.ExtraContentField)
	require.NotEmpty(t, got)

	var want, have any
	err = json.Unmarshal([]byte(replay), &want)
	require.NoError(t, err)
	err = json.Unmarshal(got, &have)
	require.NoError(t, err)
	assert.Equal(t, want, have)
	reasoning := resp.Choices[0].Message.ExtraFields.Lookup("reasoning_content")
	assert.Equal(t, `"hm"`, string(reasoning))
}
