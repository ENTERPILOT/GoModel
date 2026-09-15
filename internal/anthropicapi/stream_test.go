package anthropicapi

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// drainConverter runs the SSE converter over chatStream and returns the parsed
// sequence of emitted Anthropic events (the decoded data: payloads).
func drainConverter(t *testing.T, chatStream string) []map[string]any {
	t.Helper()
	conv := NewStreamConverter(io.NopCloser(strings.NewReader(chatStream)), "fallback-model", 0)
	defer conv.Close() //nolint:errcheck

	out, err := io.ReadAll(conv)
	require.NoError(t, err)

	var events []map[string]any
	for block := range strings.SplitSeq(string(out), "\n\n") {
		for line := range strings.SplitSeq(block, "\n") {
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			var payload map[string]any
			err := json.Unmarshal([]byte(data), &payload)
			require.NoError(t, err, "unmarshal event %q: %v", data, err)

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
	require.Equal(t, want, got)

	start := events[0]["message"].(map[string]any)
	assert.Equal(t, "msg_chatcmpl-1", start["id"])
	assert.Equal(t, "gpt", start["model"], "message_start = %+v", start)

	// content_block_delta payloads carry the text deltas.
	d0 := events[2]["delta"].(map[string]any)
	d1 := events[3]["delta"].(map[string]any)
	assert.Equal(t, "text_delta", d0["type"])
	assert.Equal(t, "Hel", d0["text"])
	assert.Equal(t, "lo", d1["text"], "text deltas = %v / %v", d0, d1)

	delta := events[5]
	assert.Equal(t, "end_turn", delta["delta"].(map[string]any)["stop_reason"], "message_delta stop_reason = %+v", delta["delta"])

	usage := delta["usage"].(map[string]any)
	assert.Equal(t, float64(5), usage["input_tokens"])
	assert.Equal(t, float64(2), usage["output_tokens"], "message_delta usage = %+v", usage)
}

func TestStreamConverterToolCall(t *testing.T) {
	chatStream := strings.Join([]string{
		`data: {"id":"chatcmpl-2","model":"gpt","choices":[{"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"get_weather","arguments":""},"extra_content":{"google":{"thought_signature":"sig"}}}]},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"},"extra_content":null}]},"finish_reason":null}]}`,
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
	require.Equal(t, want, got)

	block := events[1]["content_block"].(map[string]any)
	assert.Equal(t, "tool_use", block["type"])
	assert.Equal(t, "call_1", block["id"])
	assert.Equal(t, "get_weather", block["name"], "tool_use content_block = %+v", block)

	extra, _ := json.Marshal(block["extra_content"])
	assert.Equal(t, `{"google":{"thought_signature":"sig"}}`, string(extra), "tool_use extra_content = %s", extra)

	args := events[2]["delta"].(map[string]any)
	assert.Equal(t, "input_json_delta", args["type"])
	assert.Equal(t, `{"city":`, args["partial_json"], "input_json_delta = %+v", args)
	assert.Equal(t, "tool_use", events[5]["delta"].(map[string]any)["stop_reason"], "message_delta = %+v", events[5]["delta"])
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
	conv := NewStreamConverter(body, "m", 0)
	_, err := io.ReadAll(conv)
	require.NoError(t, err)
	err = conv.Close()
	require.NoError(t, err)
	require.True(t, body.closed)
}

func TestStreamConverterEmptyStream(t *testing.T) {
	// Even with no chunks the converter must emit a well-formed envelope.
	events := drainConverter(t, "data: [DONE]\n\n")
	got := eventTypes(events)
	want := []string{"message_start", "message_delta", "message_stop"}
	require.Equal(t, want, got)
}

func TestStreamConverterStopSequence(t *testing.T) {
	// The anthropic provider carries a natively-reported stop sequence as a
	// delta extension field; the converter must surface it per the Anthropic
	// contract instead of collapsing to end_turn.
	chatStream := strings.Join([]string{
		`data: {"id":"chatcmpl-3","model":"claude","choices":[{"delta":{"content":"1 2 3 "},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{"stop_sequence":"7"},"finish_reason":"stop"}],"usage":{"prompt_tokens":6,"completion_tokens":3}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")

	events := drainConverter(t, chatStream)
	final := events[len(events)-2]
	require.Equal(t, "message_delta", final["type"])

	delta := final["delta"].(map[string]any)
	assert.Equal(t, "stop_sequence", delta["stop_reason"])
	assert.Equal(t, "7", delta["stop_sequence"], "message_delta delta = %+v, want stop_reason=stop_sequence stop_sequence=7", delta)
}

func TestStreamConverterMessageStartInputEstimate(t *testing.T) {
	// message_start reports the heuristic input estimate (the upstream only
	// delivers usage in its final chunk); message_delta stays authoritative.
	chatStream := strings.Join([]string{
		`data: {"id":"chatcmpl-4","model":"gpt","choices":[{"delta":{"content":"hi"},"finish_reason":null}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":1}}`,
		`data: [DONE]`,
		"",
	}, "\n\n")

	conv := NewStreamConverter(io.NopCloser(strings.NewReader(chatStream)), "m", 42)
	defer conv.Close() //nolint:errcheck
	out, err := io.ReadAll(conv)
	require.NoError(t, err)

	var start, delta map[string]any
	for block := range strings.SplitSeq(string(out), "\n\n") {
		for line := range strings.SplitSeq(block, "\n") {
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			var payload map[string]any
			err := json.Unmarshal([]byte(data), &payload)
			require.NoError(t, err, "unmarshal %q: %v", data, err)

			switch payload["type"] {
			case "message_start":
				start = payload
			case "message_delta":
				delta = payload
			}
		}
	}

	usage := start["message"].(map[string]any)["usage"].(map[string]any)
	assert.Equal(t, float64(42), usage["input_tokens"], "message_start usage = %+v, want input_tokens=42", usage)

	finalUsage := delta["usage"].(map[string]any)
	assert.Equal(t, float64(11), finalUsage["input_tokens"])
	assert.Equal(t, float64(1), finalUsage["output_tokens"], "message_delta usage = %+v, want real 11/1", finalUsage)
}

// TestStreamConverterThinkingSignature pins the streaming half of the thinking
// round trip: a client that assembles the SSE events into an assistant turn
// must end up with a signature on the thinking block, or its next request is
// rejected by Anthropic.
func TestStreamConverterThinkingSignature(t *testing.T) {
	chatStream := strings.Join([]string{
		`data: {"id":"chatcmpl-1","model":"claude-sonnet-4-5","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"Let me "},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"think."},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"extra_content":{"anthropic":{"thinking_blocks":[{"type":"thinking","thinking":"Let me think.","signature":"sig-1"}]}}},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"content":"Done."},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		"",
	}, "\n\n")

	events := drainConverter(t, chatStream)
	got := eventTypes(events)
	want := []string{
		"message_start",
		"content_block_start", "content_block_delta", "content_block_delta", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_stop",
		"message_delta", "message_stop",
	}
	require.Equal(t, want, got)

	signature := events[4]["delta"].(map[string]any)
	require.Equal(t, "signature_delta", signature["type"])
	require.Equal(t, "sig-1", signature["signature"], "event 4 delta = %v, want a signature_delta carrying sig-1", signature)
	idx, _ := events[4]["index"].(float64)
	assert.Equal(t, 0, int(idx), "signature_delta index = %v, want the open thinking block (0)", events[4]["index"])

	for _, event := range events {
		_, ok := event["extra_content"]
		assert.False(t, ok, "event %v leaks the gateway's extra_content member", event["type"])
	}
}

// A provider that reasons without signing its output (DeepSeek, Fireworks, …)
// still has to produce a schema-valid thinking block: Anthropic opens one with
// "signature": "" and the gateway must do the same, so a strictly typed client
// can accumulate the stream. No signature_delta follows, because there is no
// signature to report.
func TestStreamConverterUnsignedThinkingCarriesEmptySignature(t *testing.T) {
	chatStream := strings.Join([]string{
		`data: {"id":"chatcmpl-1","model":"deepseek-flash","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"Let me think."},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"content":"Done."},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		"",
	}, "\n\n")

	events := drainConverter(t, chatStream)
	block := events[1]["content_block"].(map[string]any)
	require.Equal(t, "content_block_start", events[1]["type"])
	require.Equal(t, "thinking", block["type"], "event 1 = %v, want a thinking content_block_start", events[1])

	signature, ok := block["signature"]
	require.True(t, ok)
	require.Empty(t, signature)

	for _, event := range events {
		if delta, ok := event["delta"].(map[string]any); ok {
			require.NotEqual(t, "signature_delta", delta["type"], "unsigned reasoning emitted %v", delta)
		}
	}
}

// A redacted thinking block has no deltas of its own: it arrives whole, and
// the converter must open and close a content block for it so the client can
// replay the opaque payload.
func TestStreamConverterRedactedThinking(t *testing.T) {
	chatStream := strings.Join([]string{
		`data: {"id":"chatcmpl-1","model":"claude-sonnet-4-5","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"extra_content":{"anthropic":{"thinking_blocks":[{"type":"redacted_thinking","data":"opaque"}]}}},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"content":"Done."},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		"",
	}, "\n\n")

	events := drainConverter(t, chatStream)
	block := events[1]["content_block"].(map[string]any)
	require.Equal(t, "content_block_start", events[1]["type"])
	require.Equal(t, "redacted_thinking", block["type"])
	require.Equal(t, "opaque", block["data"], "event 1 = %v, want a redacted_thinking block carrying the opaque data", events[1])
	require.Equal(t, "content_block_stop", events[2]["type"])
}

// The cumulative extra_content a chunk carries must not re-emit signatures the
// converter already wrote: only blocks it has not seen yet produce events.
func TestStreamConverterThinkingSignatureNotRepeated(t *testing.T) {
	first := `{"type":"thinking","thinking":"a","signature":"sig-a"}`
	second := `{"type":"thinking","thinking":"b","signature":"sig-b"}`
	chatStream := strings.Join([]string{
		`data: {"id":"chatcmpl-1","model":"claude-sonnet-4-5","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"a"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"extra_content":{"anthropic":{"thinking_blocks":[` + first + `]}}},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"b"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"extra_content":{"anthropic":{"thinking_blocks":[` + first + `,` + second + `]}}},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		"",
	}, "\n\n")

	signatures := []string{}
	for _, event := range drainConverter(t, chatStream) {
		delta, ok := event["delta"].(map[string]any)
		if !ok || delta["type"] != "signature_delta" {
			continue
		}
		signatures = append(signatures, delta["signature"].(string))
	}
	require.Equal(t, "sig-a,sig-b", strings.Join(signatures, ","), "signature deltas = %v, want each block signed exactly once", signatures)
}

// Interleaved thinking puts text between two thinking blocks. The second
// block's signature must land in the second thinking block, not the first,
// and the text block in between must be closed before it opens.
func TestStreamConverterThinkingBlocksAroundText(t *testing.T) {
	first := `{"type":"thinking","thinking":"a","signature":"sig-a"}`
	second := `{"type":"thinking","thinking":"b","signature":"sig-b"}`
	chatStream := strings.Join([]string{
		`data: {"id":"chatcmpl-1","model":"claude-sonnet-4-5","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"a"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"extra_content":{"anthropic":{"thinking_blocks":[` + first + `]}}},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"content":"mid"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"b"},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{"extra_content":{"anthropic":{"thinking_blocks":[` + first + `,` + second + `]}}},"finish_reason":null}]}`,
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		"",
	}, "\n\n")

	var got []string
	for _, event := range drainConverter(t, chatStream) {
		switch event["type"] {
		case "content_block_start":
			got = append(got, "start:"+event["content_block"].(map[string]any)["type"].(string))
		case "content_block_delta":
			delta := event["delta"].(map[string]any)
			entry := "delta:" + delta["type"].(string)
			if sig, ok := delta["signature"].(string); ok {
				entry += ":" + sig
			}
			got = append(got, entry)
		case "content_block_stop":
			got = append(got, "stop")
		}
	}
	want := []string{
		"start:thinking", "delta:thinking_delta", "delta:signature_delta:sig-a", "stop",
		"start:text", "delta:text_delta", "stop",
		"start:thinking", "delta:thinking_delta", "delta:signature_delta:sig-b", "stop",
	}
	require.Equal(t, strings.Join(want, "\n"), strings.Join(got, "\n"))
}

// Providers that name the member "reasoning" instead of "reasoning_content"
// (Groq, OpenRouter) must still produce thinking deltas.
func TestStreamConverterVendorReasoningMember(t *testing.T) {
	tests := []struct {
		name  string
		delta string
		want  string
	}{
		{name: "reasoning alone", delta: `{"reasoning":"Let me think."}`, want: "Let me think."},
		{name: "reasoning_content wins", delta: `{"reasoning_content":"Canonical.","reasoning":"Vendor."}`, want: "Canonical."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chatStream := strings.Join([]string{
				`data: {"id":"chatcmpl-1","model":"qwen/qwen3.6-27b","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
				`data: {"choices":[{"index":0,"delta":` + tt.delta + `,"finish_reason":null}]}`,
				`data: {"choices":[{"index":0,"delta":{"content":"391"},"finish_reason":null}]}`,
				`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
				`data: [DONE]`,
				"",
			}, "\n\n")

			events := drainConverter(t, chatStream)
			var thinking []string
			for _, event := range events {
				delta, ok := event["delta"].(map[string]any)
				if !ok || delta["type"] != "thinking_delta" {
					continue
				}
				thinking = append(thinking, delta["thinking"].(string))
			}
			assert.Equal(t, tt.want, strings.Join(thinking, ""))
		})
	}
}
