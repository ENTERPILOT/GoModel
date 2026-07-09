package anthropicapi

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// drainConverter runs the SSE converter over chatStream and returns the parsed
// sequence of emitted Anthropic events (the decoded data: payloads).
func drainConverter(t *testing.T, chatStream string) []map[string]any {
	t.Helper()
	conv := NewStreamConverter(io.NopCloser(strings.NewReader(chatStream)), "fallback-model")
	defer conv.Close() //nolint:errcheck

	out, err := io.ReadAll(conv)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	var events []map[string]any
	for block := range strings.SplitSeq(string(out), "\n\n") {
		for line := range strings.SplitSeq(block, "\n") {
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(data), &payload); err != nil {
				t.Fatalf("unmarshal event %q: %v", data, err)
			}
			events = append(events, payload)
		}
	}
	return events
}

func eventTypes(events []map[string]any) []string {
	types := make([]string, len(events))
	for i, e := range events {
		types[i], _ = e["type"].(string)
	}
	return types
}

func TestStreamConverterText(t *testing.T) {
	chatStream := strings.Join([]string{
		`data: {"id":"chatcmpl-1","model":"gpt","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: {"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")

	events := drainConverter(t, chatStream)
	want := []string{
		"message_start", "content_block_start",
		"content_block_delta", "content_block_delta",
		"content_block_stop", "message_delta", "message_stop",
	}
	got := eventTypes(events)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("event sequence = %v, want %v", got, want)
	}

	start := events[0]["message"].(map[string]any)
	if start["id"] != "msg_chatcmpl-1" || start["model"] != "gpt" {
		t.Errorf("message_start = %+v", start)
	}

	// content_block_delta payloads carry the text deltas.
	d0 := events[2]["delta"].(map[string]any)
	d1 := events[3]["delta"].(map[string]any)
	if d0["type"] != "text_delta" || d0["text"] != "Hel" || d1["text"] != "lo" {
		t.Errorf("text deltas = %v / %v", d0, d1)
	}

	delta := events[5]
	if delta["delta"].(map[string]any)["stop_reason"] != "end_turn" {
		t.Errorf("message_delta stop_reason = %+v", delta["delta"])
	}
	usage := delta["usage"].(map[string]any)
	if usage["input_tokens"] != float64(5) || usage["output_tokens"] != float64(2) {
		t.Errorf("message_delta usage = %+v", usage)
	}
}

func TestStreamConverterToolCall(t *testing.T) {
	chatStream := strings.Join([]string{
		`data: {"id":"chatcmpl-2","model":"gpt","choices":[{"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"paris\"}"}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":8,"completion_tokens":4}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")

	events := drainConverter(t, chatStream)
	want := []string{
		"message_start", "content_block_start",
		"content_block_delta", "content_block_delta",
		"content_block_stop", "message_delta", "message_stop",
	}
	got := eventTypes(events)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("event sequence = %v, want %v", got, want)
	}

	block := events[1]["content_block"].(map[string]any)
	if block["type"] != "tool_use" || block["id"] != "call_1" || block["name"] != "get_weather" {
		t.Errorf("tool_use content_block = %+v", block)
	}

	args := events[2]["delta"].(map[string]any)
	if args["type"] != "input_json_delta" || args["partial_json"] != `{"city":` {
		t.Errorf("input_json_delta = %+v", args)
	}

	if events[5]["delta"].(map[string]any)["stop_reason"] != "tool_use" {
		t.Errorf("message_delta = %+v", events[5]["delta"])
	}
}

// closeTracker records whether Close was called on the underlying stream.
type closeTracker struct {
	io.Reader
	closed bool
}

func (c *closeTracker) Close() error {
	c.closed = true
	return nil
}

// TestStreamConverterCloseClosesUnderlying guards a regression where Read
// marked the converter closed on EOF, making the deferred Close a no-op that
// skipped the underlying stream — which suppressed audit/usage OnStreamClose
// and leaked the provider connection.
func TestStreamConverterCloseClosesUnderlying(t *testing.T) {
	body := &closeTracker{Reader: strings.NewReader("data: [DONE]\n\n")}
	conv := NewStreamConverter(body, "m")

	if _, err := io.ReadAll(conv); err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if err := conv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !body.closed {
		t.Fatal("Close did not propagate to the underlying stream")
	}
}

func TestStreamConverterEmptyStream(t *testing.T) {
	// Even with no chunks the converter must emit a well-formed envelope.
	events := drainConverter(t, "data: [DONE]\n\n")
	got := eventTypes(events)
	want := []string{"message_start", "message_delta", "message_stop"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("event sequence = %v, want %v", got, want)
	}
}
