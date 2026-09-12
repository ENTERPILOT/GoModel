package streaming

import (
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rawData(name, data string) RawEvent {
	return RawEvent{Name: name, Data: []byte(data), Raw: (&Event{Name: name, Data: []byte(data)}).Encode()}
}

func TestChatCodec_Decode(t *testing.T) {
	tests := []struct {
		name   string
		data   string
		kind   EventKind
		choice int
		text   string
	}{
		{"content", `{"id":"c1","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}`, KindTextDelta, 0, "Hi"},
		{"second choice", `{"choices":[{"index":1,"delta":{"content":"B"}}]}`, KindTextDelta, 1, "B"},
		{"role only", `{"choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`, KindOther, 0, ""},
		{"null content", `{"choices":[{"index":0,"delta":{"content":null}}]}`, KindOther, 0, ""},
		{"reasoning_content", `{"choices":[{"index":0,"delta":{"reasoning_content":"think"}}]}`, KindReasoningDelta, 0, "think"},
		{"reasoning", `{"choices":[{"index":0,"delta":{"reasoning":"deep"}}]}`, KindReasoningDelta, 0, "deep"},
		{"reasoning object is not text", `{"choices":[{"index":0,"delta":{"reasoning":{"x":1}}}]}`, KindOther, 0, ""},
		{"tool call", `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"f","arguments":"{\"a\":"}}]}}]}`, KindToolCallDelta, 0, `{"a":`},
		{"finish", `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, KindFinish, 0, ""},
		{"content with finish is text", `{"choices":[{"index":0,"delta":{"content":"end"},"finish_reason":"stop"}]}`, KindTextDelta, 0, "end"},
		{"usage only", `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`, KindUsage, 0, ""},
		{"usage null without choices", `{"choices":[],"usage":null}`, KindOther, 0, ""},
		{"not json", `hello`, KindOther, 0, ""},
		{"done", `[DONE]`, KindDone, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := ChatCodec().Decode(rawData("", tt.data), 3)
			assert.Equal(t, tt.kind, ev.Kind)
			assert.Equal(t, tt.choice, ev.Choice)
			assert.Equal(t, tt.text, ev.Text)
			assert.Equal(t, 3, ev.Seq)
			assert.Equal(t, tt.data, string(ev.Data))
		})
	}
}

func TestResponsesCodec_Decode(t *testing.T) {
	tests := []struct {
		name string
		data string
		kind EventKind
		text string
	}{
		{"delta", `{"type":"response.output_text.delta","item_id":"m","output_index":0,"content_index":0,"delta":"Hi"}`, KindTextDelta, "Hi"},
		{"reasoning summary", `{"type":"response.reasoning_summary_text.delta","delta":"why"}`, KindReasoningDelta, "why"},
		{"function args", `{"type":"response.function_call_arguments.delta","delta":"{\"q\""}`, KindToolCallDelta, `{"q"`},
		{"text done", `{"type":"response.output_text.done","text":"Hi"}`, KindOther, ""},
		{"completed", `{"type":"response.completed","response":{"id":"r","status":"completed"}}`, KindFinish, ""},
		{"incomplete", `{"type":"response.incomplete","response":{"id":"r"}}`, KindFinish, ""},
		{"failed", `{"type":"response.failed","response":{"id":"r"}}`, KindFinish, ""},
		{"created", `{"type":"response.created","response":{"id":"r"}}`, KindOther, ""},
		{"done", `[DONE]`, KindDone, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := ResponsesCodec().Decode(rawData("x", tt.data), 0)
			assert.Equal(t, tt.kind, ev.Kind)
			assert.Equal(t, tt.text, ev.Text)
			assert.Equal(t, 0, ev.Choice)
			assert.Equal(t, "x", ev.Name)
		})
	}
}

func decodeMap(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	err := json.Unmarshal(data, &m)
	require.NoError(t, err, "decode %q", data)

	return m
}

func TestCodec_RewriteText(t *testing.T) {
	tests := []struct {
		name  string
		codec Codec
		data  string
		text  string
		want  string
	}{
		{
			name:  "chat content keeps every other member",
			codec: ChatCodec(),
			data:  `{"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","x_extra":{"k":[1,2]},"choices":[{"index":0,"delta":{"role":"assistant","content":"secret","extra":true},"logprobs":null,"finish_reason":null}]}`,
			text:  "[redacted]",
			want:  `{"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","x_extra":{"k":[1,2]},"choices":[{"index":0,"delta":{"role":"assistant","content":"[redacted]","extra":true},"logprobs":null,"finish_reason":null}]}`,
		},
		{
			name:  "chat rewrites the matching choice index",
			codec: ChatCodec(),
			data:  `{"choices":[{"index":2,"delta":{"role":"assistant"}},{"index":1,"delta":{"content":"one"}}]}`,
			text:  "uno",
			want:  `{"choices":[{"index":2,"delta":{"role":"assistant"}},{"index":1,"delta":{"content":"uno"}}]}`,
		},
		{
			name:  "chat reasoning_content",
			codec: ChatCodec(),
			data:  `{"choices":[{"index":0,"delta":{"reasoning_content":"a"}}]}`,
			text:  "b",
			want:  `{"choices":[{"index":0,"delta":{"reasoning_content":"b"}}]}`,
		},
		{
			name:  "chat reasoning member",
			codec: ChatCodec(),
			data:  `{"choices":[{"index":0,"delta":{"reasoning":"a"}}]}`,
			text:  "b",
			want:  `{"choices":[{"index":0,"delta":{"reasoning":"b"}}]}`,
		},
		{
			name:  "responses delta",
			codec: ResponsesCodec(),
			data:  `{"type":"response.output_text.delta","sequence_number":4,"item_id":"m","output_index":0,"content_index":0,"delta":"secret","logprobs":[]}`,
			text:  "***",
			want:  `{"type":"response.output_text.delta","sequence_number":4,"item_id":"m","output_index":0,"content_index":0,"delta":"***","logprobs":[]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := tt.codec.Decode(rawData("", tt.data), 0)
			got, err := tt.codec.RewriteText(ev, tt.text)
			require.NoError(t, err)
			assert.Equal(t, tt.text, got.Text)
			assert.Equal(t, decodeMap(t, []byte(tt.want)), decodeMap(t, got.Data))
		})
	}
}

func TestCodec_RewriteTextRejectsNonText(t *testing.T) {
	for _, codec := range []Codec{ChatCodec(), ResponsesCodec()} {
		ev := codec.Decode(rawData("", `{"type":"response.completed","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`), 0)
		_, err := codec.RewriteText(ev, "x")
		assert.Equal(t, ErrNotTextEvent, err)
	}
}

func decodeStreamEvents(t *testing.T, stream []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range scanAll(t, &EventScanner{}, string(stream)) {
		if raw.Comment || string(raw.Data) == "[DONE]" {
			continue
		}
		out = append(out, decodeMap(t, raw.Data))
	}
	return out
}

func TestChatCodec_Terminate(t *testing.T) {
	codec := ChatCodec()
	codec.Track(codec.Decode(rawData("", `{"id":"chatcmpl-1","model":"gpt","created":42,"choices":[{"index":0,"delta":{"content":"a"}}]}`), 0))
	codec.Track(codec.Decode(rawData("", `{"id":"chatcmpl-1","choices":[{"index":1,"delta":{},"finish_reason":"stop"}]}`), 1))

	out := joinChunks(codec.Terminate(Termination{Text: "blocked"}))
	require.True(t, strings.HasSuffix(string(out), "data: [DONE]\n\n"), "terminate must end with [DONE]: %q", out)

	events := decodeStreamEvents(t, out)
	require.Len(t, events, 2)

	textChoice := events[0]["choices"].([]any)[0].(map[string]any)
	assert.Equal(t, "blocked", textChoice["delta"].(map[string]any)["content"])
	assert.Equal(t, "chatcmpl-1", events[0]["id"])
	assert.Equal(t, "gpt", events[0]["model"])
	assert.Equal(t, float64(42), events[0]["created"], "text chunk = %v", events[0])

	finishChoices := events[1]["choices"].([]any)
	require.Len(t, finishChoices, 1, "finish chunk should only close open choice 0: %v", events[1])

	finish := finishChoices[0].(map[string]any)
	assert.Equal(t, float64(0), finish["index"])
	assert.Equal(t, "content_filter", finish["finish_reason"])
	assert.Empty(t, finish["delta"].(map[string]any), "finish choice = %v", finish)
}

func TestChatCodec_TerminateWithError(t *testing.T) {
	out := joinChunks(ChatCodec().Terminate(Termination{ErrorCode: "plugin_failure", ErrorMessage: "boom", FinishReason: "error"}))
	events := decodeStreamEvents(t, out)
	require.Len(t, events, 2)

	errObj := events[0]["error"].(map[string]any)
	assert.Equal(t, "plugin_failure", errObj["code"])
	assert.Equal(t, "boom", errObj["message"], "error payload = %v", errObj)

	finish := events[1]["choices"].([]any)[0].(map[string]any)
	assert.Equal(t, "error", finish["finish_reason"])
}

func TestResponsesCodec_Terminate(t *testing.T) {
	codec := ResponsesCodec()
	feed := func(seq int, data string) {
		codec.Track(codec.Decode(rawData("", data), seq))
	}
	feed(0, `{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","model":"gpt","created_at":7}}`)
	feed(1, `{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","status":"in_progress","content":[]}}`)
	feed(2, `{"type":"response.content_part.added","sequence_number":2,"item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`)
	feed(3, `{"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"Hel"}`)

	out := joinChunks(codec.Terminate(Termination{Text: "[cut]"}))
	require.True(t, strings.HasSuffix(string(out), "data: [DONE]\n\n"), "terminate must end with [DONE]: %q", out)

	events := decodeStreamEvents(t, out)
	var types []string
	for i, ev := range events {
		types = append(types, ev["type"].(string))
		assert.Equal(t, float64(4+i), ev["sequence_number"], "event %d", i)
	}
	want := []string{
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.incomplete",
	}
	require.Equal(t, want, types)
	assert.Equal(t, "Hel[cut]", events[1]["text"])

	resp := events[4]["response"].(map[string]any)
	assert.Equal(t, "resp_1", resp["id"])
	assert.Equal(t, "gpt", resp["model"])
	assert.Equal(t, "incomplete", resp["status"])
	assert.Equal(t, float64(7), resp["created_at"], "response = %v", resp)
	assert.Equal(t, "content_filter", resp["incomplete_details"].(map[string]any)["reason"], "incomplete_details = %v", resp["incomplete_details"])

	item := resp["output"].([]any)[0].(map[string]any)
	assert.Equal(t, "incomplete", item["status"])
	assert.Equal(t, "Hel[cut]", item["content"].([]any)[0].(map[string]any)["text"], "output item = %v", item)
}

func TestResponsesCodec_TerminateFailedWithoutItems(t *testing.T) {
	out := joinChunks(ResponsesCodec().Terminate(Termination{ErrorCode: "plugin_failure", ErrorMessage: "boom"}))
	events := decodeStreamEvents(t, out)
	require.Len(t, events, 1)
	require.Equal(t, "response.failed", events[0]["type"])

	resp := events[0]["response"].(map[string]any)
	assert.Equal(t, "failed", resp["status"])
	assert.Equal(t, "plugin_failure", resp["error"].(map[string]any)["code"], "response = %v", resp)
	assert.Empty(t, resp["output"].([]any), "output should be empty: %v", resp["output"])
}

func TestCodecs_TerminateCarryUsage(t *testing.T) {
	usage := map[string]any{"prompt_tokens": 3, "completion_tokens": 5, "total_tokens": 8}
	chat := decodeStreamEvents(t, joinChunks(ChatCodec().Terminate(Termination{ErrorCode: "guardrail_blocked", ErrorMessage: "no", Usage: usage})))
	finish := chat[len(chat)-1]
	got, ok := finish["usage"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(8), got["total_tokens"], "chat finish chunk usage = %v", finish["usage"])
	_, present := chat[0]["usage"]
	require.False(t, present, "error chunk must not carry usage: %v", chat[0])

	responses := decodeStreamEvents(t, joinChunks(ResponsesCodec().Terminate(Termination{ErrorCode: "guardrail_blocked", ErrorMessage: "no", Usage: usage})))
	resp := responses[len(responses)-1]["response"].(map[string]any)
	got, ok = resp["usage"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(8), got["total_tokens"], "responses terminal usage = %v", resp["usage"])
	_, present = decodeStreamEvents(t, joinChunks(ChatCodec().Terminate(Termination{})))[0]["usage"]
	require.False(t, present)
}
