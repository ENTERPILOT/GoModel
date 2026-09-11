package streaming

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

// restoreTransformer puts "a@b.c" in place of "<EMAIL_1>" in every delta
// window, tool-call arguments included, skipping matches that end inside
// the overlap.
func restoreTransformer() *funcTransformer {
	return &funcTransformer{onEvent: func(ev *Event) (Decision, error) {
		if !isDelta(ev.Kind) {
			return Decision{Action: ActionPass}, nil
		}
		skip := 0
		for i := 0; i < ev.Overlap && skip < len(ev.Text); i++ {
			skip++
		}
		idx := strings.Index(ev.Text, "<EMAIL_1>")
		if idx < 0 || idx+len("<EMAIL_1>") <= skip {
			return Decision{Action: ActionPass}, nil
		}
		return Decision{Action: ActionReplace, Text: strings.ReplaceAll(ev.Text, "<EMAIL_1>", "a@b.c")}, nil
	}}
}

func TestTransformedSSEStream_ToolCallArgumentsAcrossChunks(t *testing.T) {
	// The first chunk carries the call's id and name with its first
	// arguments; the placeholder is split across the next two.
	input := `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":null},"finish_reason":null}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"send","arguments":"{\"to\":\""}}]},"finish_reason":null}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"<EMA"}}]},"finish_reason":null}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"IL_1>\"}"}}]},"finish_reason":null}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	tr := restoreTransformer()
	stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(input)), ChatCodec(), tr, TransformOptions{LookbehindChars: 12})
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "<EMA") {
		t.Errorf("placeholder fragment leaked:\n%s", got)
	}
	for _, ev := range tr.seen {
		if ev.Kind == KindToolCallDelta && ev.Call != 0 {
			t.Errorf("call index = %d", ev.Call)
		}
	}
	resp, err := AssembleChatResponse(decodeChatEvents(t, got))
	if err != nil {
		t.Fatal(err)
	}
	calls := resp.Choices[0].Message.ToolCalls
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Function.Name != "send" || calls[0].Function.Arguments != `{"to":"a@b.c"}` {
		t.Errorf("assembled tool calls = %+v", calls)
	}
	if resp.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("finish = %q", resp.Choices[0].FinishReason)
	}
	if n := strings.Count(string(got), `"finish_reason":"tool_calls"`); n != 1 {
		t.Errorf("finish_reason emitted %d times", n)
	}
}

func TestTransformedSSEStream_ToolCallsAreSeparateWindows(t *testing.T) {
	input := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c0","function":{"name":"f","arguments":"{\"a\":\"<EMA"}}]}}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"c1","function":{"name":"g","arguments":"{\"b\":\"<EMA"}}]}}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"IL_1>\"}"}}]}}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	tr := restoreTransformer()
	stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(input)), ChatCodec(), tr, TransformOptions{LookbehindChars: 12})
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	// Call 1's first delta flushed call 0's window; call 0 keeps its
	// unfinished fragment, call 1 is restored whole.
	resp, err := AssembleChatResponse(decodeChatEvents(t, got))
	if err != nil {
		t.Fatal(err)
	}
	calls := resp.Choices[0].Message.ToolCalls
	if len(calls) != 2 || calls[0].Function.Arguments != `{"a":"<EMA` || calls[1].Function.Arguments != `{"b":"a@b.c"}` || calls[1].ID != "c1" {
		t.Errorf("assembled tool calls = %+v", calls)
	}
	var seen []string
	for _, ev := range tr.seen {
		if ev.Kind == KindToolCallDelta {
			seen = append(seen, string(rune('0'+ev.Call))+":"+ev.Text)
		}
	}
	// Parallel calls are independent windows: call 1's deltas do not
	// flush call 0. Each is seen on arrival and once more, in order, when
	// the finish chunk flushes them.
	want := []string{`0:{"a":"<EMA`, `1:{"b":"<EMA`, `1:{"b":"<EMAIL_1>"}`, `0:{"a":"<EMA`, `1:"b":"a@b.c"}`}
	if !equalStrings(seen, want) {
		t.Errorf("windows = %q, want %q", seen, want)
	}
}

func TestTransformedSSEStream_ResponsesToolCallArgumentsRestated(t *testing.T) {
	input := "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"r1\",\"model\":\"m\",\"created_at\":1}}\n\n" +
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"send\",\"arguments\":\"\"}}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":2,\"item_id\":\"fc_1\",\"output_index\":0,\"delta\":\"{\\\"to\\\":\\\"<EMA\"}\n\n" +
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":3,\"item_id\":\"fc_1\",\"output_index\":0,\"delta\":\"IL_1>\\\"}\"}\n\n" +
		"event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\",\"sequence_number\":4,\"item_id\":\"fc_1\",\"output_index\":0,\"arguments\":\"{\\\"to\\\":\\\"<EMAIL_1>\\\"}\"}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":5,\"output_index\":0,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"send\",\"arguments\":\"{\\\"to\\\":\\\"<EMAIL_1>\\\"}\",\"status\":\"completed\"}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":6,\"response\":{\"id\":\"r1\",\"model\":\"m\",\"created_at\":1,\"status\":\"completed\",\"output\":[{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"send\",\"arguments\":\"{\\\"to\\\":\\\"<EMAIL_1>\\\"}\",\"status\":\"completed\"}]}}\n\n"
	tr := restoreTransformer()
	stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(input)), ResponsesCodec(), tr, TransformOptions{LookbehindChars: 12})
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "EMAIL_1") || strings.Contains(string(got), "<EMA") {
		t.Errorf("placeholder leaked:\n%s", got)
	}
	if n := strings.Count(string(got), `a@b.c`); n != 4 {
		t.Errorf("restored value appears %d times (delta, done, item.done, completed), want 4:\n%s", n, got)
	}
	resp, err := AssembleResponsesResponse(decodeResponsesEvents(t, got))
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Output) != 1 || resp.Output[0].Arguments != `{"to":"a@b.c"}` || resp.Output[0].CallID != "call_1" {
		t.Errorf("assembled = %+v", resp.Output)
	}
}

func TestCodecs_RewriteToolCallArguments(t *testing.T) {
	chat := ChatCodec()
	ev := chat.Decode(RawEvent{Data: []byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":2,"id":"c","function":{"name":"f","arguments":"{\"x\""}}]},"finish_reason":"tool_calls"}]}`)}, 0)
	if ev.Kind != KindToolCallDelta || ev.Call != 2 || ev.Text != `{"x"` || !ev.ClosesChoice {
		t.Fatalf("decoded = %+v", ev)
	}
	rewritten, err := chat.RewriteText(ev, `{"y"`)
	if err != nil || rewritten.Text != `{"y"` || !strings.Contains(string(rewritten.Data), `"arguments":"{\"y\""`) || !strings.Contains(string(rewritten.Data), `"name":"f"`) {
		t.Errorf("chat rewrite = %s, %v", rewritten.Data, err)
	}
	stripped, ok := chat.StripTerminal(ev)
	if !ok || stripped.ClosesChoice {
		t.Errorf("strip reported no change: %s", stripped.Data)
	}
	if strings.Contains(string(stripped.Data), `"finish_reason":"tool_calls"`) {
		t.Errorf("finish_reason not stripped: %s", stripped.Data)
	}
	if !strings.Contains(string(stripped.Data), `"arguments":"{\"x\""`) {
		t.Errorf("arguments lost by strip: %s", stripped.Data)
	}
	responses := ResponsesCodec()
	ev = responses.Decode(RawEvent{Name: "response.function_call_arguments.delta", Data: []byte(`{"type":"response.function_call_arguments.delta","output_index":3,"delta":"ab"}`)}, 0)
	if ev.Kind != KindToolCallDelta || ev.Call != 3 {
		t.Fatalf("decoded = %+v", ev)
	}
	rewritten, err = responses.RewriteText(ev, "cd")
	if err != nil || !strings.Contains(string(rewritten.Data), `"delta":"cd"`) {
		t.Errorf("responses rewrite = %s, %v", rewritten.Data, err)
	}
}

func TestTransformedSSEStream_ParallelToolCallsInOneDelta(t *testing.T) {
	// One delta announces two tool calls with their first arguments; the
	// second one's placeholder is completed by a later delta. The
	// finish_reason arrives on the multi-call chunk's choice.
	input := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c0","function":{"name":"f","arguments":"{\"a\":\"<EMAIL_1>\"}"}},{"index":1,"id":"c1","function":{"name":"g","arguments":"{\"b\":\"<EMA"}}]},"finish_reason":null}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":"IL_1>\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}` + "\n\n" +
		"data: [DONE]\n\n"
	tr := restoreTransformer()
	stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(input)), ChatCodec(), tr, TransformOptions{LookbehindChars: 12})
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "EMAIL_1") || strings.Contains(string(got), "<EMA") {
		t.Errorf("placeholder leaked:\n%s", got)
	}
	var calls []int
	for _, ev := range tr.seen {
		if ev.Kind == KindToolCallDelta {
			calls = append(calls, ev.Call)
		}
	}
	// Both calls are seen on arrival, call 1 again on its second delta,
	// and both once more at the final flush.
	if strings.Trim(strings.Join(strings.Fields(strings.Trim(fmt.Sprint(calls), "[]")), ","), ",") != "0,1,1,0,1" {
		t.Errorf("transformer saw calls %v", calls)
	}
	resp, err := AssembleChatResponse(decodeChatEvents(t, got))
	if err != nil {
		t.Fatal(err)
	}
	tc := resp.Choices[0].Message.ToolCalls
	if len(tc) != 2 || tc[0].ID != "c0" || tc[0].Function.Arguments != `{"a":"a@b.c"}` || tc[1].ID != "c1" || tc[1].Function.Arguments != `{"b":"a@b.c"}` {
		t.Errorf("assembled = %+v", tc)
	}
	if resp.Choices[0].FinishReason != "tool_calls" || resp.Usage.TotalTokens != 3 {
		t.Errorf("finish = %q usage = %+v", resp.Choices[0].FinishReason, resp.Usage)
	}
	if n := strings.Count(string(got), `"finish_reason":"tool_calls"`); n != 1 {
		t.Errorf("finish_reason emitted %d times", n)
	}
}

func TestTransformedSSEStream_ResponsesInterleavedToolCalls(t *testing.T) {
	// Two function-call items stream their arguments turn by turn. Each
	// flush of the older window must be credited to its own item, so the
	// restating events carry the right arguments.
	item := func(i int, name string) string {
		return "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":" + string(rune('0'+i)) + ",\"item\":{\"id\":\"fc_" + string(rune('0'+i)) + "\",\"type\":\"function_call\",\"call_id\":\"c" + string(rune('0'+i)) + "\",\"name\":\"" + name + "\",\"arguments\":\"\"}}\n\n"
	}
	delta := func(i int, text string) string {
		return "event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":" + string(rune('0'+i)) + ",\"delta\":\"" + text + "\"}\n\n"
	}
	done := func(i int, args string) string {
		return "event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\",\"output_index\":" + string(rune('0'+i)) + ",\"arguments\":\"" + args + "\"}\n\n"
	}
	input := item(0, "f") + item(1, "g") +
		delta(0, "{\\\"a\\\":\\\"<EMA") + delta(1, "{\\\"b\\\":1") + delta(0, "IL_1>\\\"}") + delta(1, "}") +
		done(0, "{\\\"a\\\":\\\"<EMAIL_1>\\\"}") + done(1, "{\\\"b\\\":1}")
	tr := restoreTransformer()
	stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(input)), ResponsesCodec(), tr, TransformOptions{LookbehindChars: 12})
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)
	if strings.Contains(out, "EMAIL_1") {
		t.Errorf("placeholder leaked:\n%s", out)
	}
	if !strings.Contains(out, `"arguments":"{\"a\":\"a@b.c\"}"`) || !strings.Contains(out, `"arguments":"{\"b\":1}"`) {
		t.Errorf("done events restated wrongly:\n%s", out)
	}
}
