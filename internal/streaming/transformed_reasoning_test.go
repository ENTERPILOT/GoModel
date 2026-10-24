package streaming

import (
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func chatChunk(delta string) string {
	return `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":` + delta + `,"finish_reason":null}]}` + "\n\n"
}

// replaceAll is a transformer replacing old with new in the deltas of kind.
func replaceAll(kind EventKind, old, new string) *funcTransformer {
	return &funcTransformer{onEvent: func(ev *Event) (Decision, error) {
		if ev.Kind == kind && strings.Contains(ev.Text, old) {
			return Decision{Action: ActionReplace, Text: strings.ReplaceAll(ev.Text, old, new)}, nil
		}
		return Decision{Action: ActionPass}, nil
	}}
}

// Reasoning deltas are a window of their own: a pattern split across two of
// them is seen whole, under the member it came in, and the window is
// flushed before the answer starts.
func TestTransformedSSEStream_ReasoningWindow(t *testing.T) {
	for _, member := range []string{"reasoning_content", "reasoning"} {
		t.Run(member, func(t *testing.T) {
			input := chatChunk(`{"role":"assistant","`+member+`":"Greet <PER"}`) +
				chatChunk(`{"`+member+`":"SON_1> now"}`) +
				chatChunk(`{"content":"Hi <PERSON_1>"}`) +
				`data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
				"data: [DONE]\n\n"
			tr := replaceAll(KindReasoningDelta, "<PERSON_1>", "Ann")
			stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(input)), ChatCodec(), tr, TransformOptions{LookbehindChars: 10})
			got, err := io.ReadAll(stream)
			require.NoError(t, err)

			out := string(got)
			assert.NotContains(t, out, "<PER", "a reasoning fragment leaked:\n%s", out)
			resp, err := AssembleChatResponse(decodeChatEvents(t, got))
			require.NoError(t, err)
			assert.Equal(t, "Greet Ann now", lookupJSONString(t, resp.Choices[0].Message.ExtraFields.Lookup(member)))
			assert.Equal(t, "Hi <PERSON_1>", resp.Choices[0].Message.Content, "content is a window of its own")
			// The reasoning window closes before any content is presented.
			var order []EventKind
			for _, ev := range tr.seen {
				if ev.Kind == KindReasoningDelta || ev.Kind == KindTextDelta {
					order = append(order, ev.Kind)
				}
			}
			first := slices.Index(order, KindTextDelta)
			require.Positive(t, first, "seen = %v", order)
			assert.NotContains(t, kindStrings(order[first:]), string(KindReasoningDelta), "seen = %v", order)
		})
	}
}

// Every argument delta of a tool call carries the tool name the stream
// announced with its first delta.
func TestTransformedSSEStream_ToolNames(t *testing.T) {
	input := chatChunk(`{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"search","arguments":""}}]}`) +
		chatChunk(`{"tool_calls":[{"index":1,"id":"b","type":"function","function":{"name":"ask","arguments":"{\"q\":"}}]}`) +
		chatChunk(`{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":1}"}}]}`) +
		chatChunk(`{"tool_calls":[{"index":1,"function":{"arguments":"2}"}}]}`) +
		"data: [DONE]\n\n"
	tr := &funcTransformer{}
	stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(input)), ChatCodec(), tr, TransformOptions{LookbehindChars: 4})
	_, err := io.ReadAll(stream)
	require.NoError(t, err)
	names := map[int][]string{}
	for _, ev := range tr.seen {
		if ev.Kind == KindToolCallDelta {
			names[ev.Call] = append(names[ev.Call], ev.Tool)
		}
	}
	require.NotEmpty(t, names[0])
	require.NotEmpty(t, names[1])
	for _, name := range names[0] {
		assert.Equal(t, "search", name)
	}
	for _, name := range names[1] {
		assert.Equal(t, "ask", name)
	}

	responses := "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":0,\"output_index\":0,\"item\":{\"id\":\"fc\",\"type\":\"function_call\",\"call_id\":\"c\",\"name\":\"search\",\"arguments\":\"\"}}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":1,\"item_id\":\"fc\",\"output_index\":0,\"delta\":\"{\\\"q\\\":\"}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":2,\"item_id\":\"fc\",\"output_index\":0,\"delta\":\"1}\"}\n\n"
	tr = &funcTransformer{}
	stream = NewTransformedSSEStream(io.NopCloser(strings.NewReader(responses)), ResponsesCodec(), tr, TransformOptions{})
	_, err = io.ReadAll(stream)
	require.NoError(t, err)
	var seen int
	for _, ev := range tr.seen {
		if ev.Kind == KindToolCallDelta {
			seen++
			assert.Equal(t, "search", ev.Tool)
		}
	}
	assert.Equal(t, 2, seen)
}

// In a Responses stream the reasoning summary is windowed too, and the done
// event restating it carries the replaced text.
func TestTransformedSSEStream_ResponsesReasoningWindow(t *testing.T) {
	ev := func(seq int, typ, body string) string {
		return "event: " + typ + "\ndata: {\"type\":\"" + typ + "\",\"sequence_number\":" + string(rune('0'+seq)) + body + "}\n\n"
	}
	input := ev(0, "response.output_item.added", `,"output_index":0,"item":{"id":"rs","type":"reasoning","summary":[]}`) +
		ev(1, "response.reasoning_summary_part.added", `,"item_id":"rs","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}`) +
		ev(2, "response.reasoning_summary_text.delta", `,"item_id":"rs","output_index":0,"summary_index":0,"delta":"Greet <PER"`) +
		ev(3, "response.reasoning_summary_text.delta", `,"item_id":"rs","output_index":0,"summary_index":0,"delta":"SON_1>"`) +
		ev(4, "response.reasoning_summary_text.done", `,"item_id":"rs","output_index":0,"summary_index":0,"text":"Greet <PERSON_1>"`)
	tr := replaceAll(KindReasoningDelta, "<PERSON_1>", "Ann")
	stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(input)), ResponsesCodec(), tr, TransformOptions{LookbehindChars: 10})
	got, err := io.ReadAll(stream)
	require.NoError(t, err)
	out := string(got)
	assert.NotContains(t, out, "PER", "placeholder leaked:\n%s", out)
	assert.Contains(t, out, `"text":"Greet Ann"`)
}

func lookupJSONString(t *testing.T, raw []byte) string {
	t.Helper()
	require.NotEmpty(t, raw)
	var s string
	require.NoError(t, json.Unmarshal(raw, &s))
	return s
}
