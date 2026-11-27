package geminiapi

import (
	"io"
	"strings"
	"testing"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/streaming"
)

func chatSSE(lines ...string) io.ReadCloser {
	var b strings.Builder
	for _, line := range lines {
		b.WriteString("data: " + line + "\n\n")
	}
	return io.NopCloser(strings.NewReader(b.String()))
}

// convert runs the converter to its end and returns the payload of each
// emitted event along with the terminal read error (nil for a clean EOF).
func convert(t *testing.T, body io.ReadCloser, opts ResponseOptions) ([]string, error) {
	t.Helper()
	converter := NewStreamConverter(body, "fallback-model", opts)
	out, err := io.ReadAll(converter)
	require.NoError(t, converter.Close())

	var events []string
	for event := range strings.SplitSeq(string(out), "\n\n") {
		if event == "" {
			continue
		}
		payload, ok := strings.CutPrefix(event, "data: ")
		require.True(t, ok, "event = %q", event)
		require.True(t, json.Valid([]byte(payload)), "payload = %q", payload)
		events = append(events, payload)
	}
	return events, err
}

func TestStreamConverter_Text(t *testing.T) {
	events, err := convert(t, chatSSE(
		`{"id":"resp-1","model":"gemini-test","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"content":"Par"},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"content":"is."},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":14,"completion_tokens":2,"total_tokens":16}}`,
		`[DONE]`,
	), ResponseOptions{})
	require.NoError(t, err)

	assert.Equal(t, []string{
		`{"candidates":[{"content":{"parts":[{"text":"Par"}],"role":"model"},"index":0}],"modelVersion":"gemini-test","responseId":"resp-1"}`,
		`{"candidates":[{"content":{"parts":[{"text":"is."}],"role":"model"},"index":0}],"modelVersion":"gemini-test","responseId":"resp-1"}`,
		// The finish reason waits for the usage chunk that follows it, so the
		// last event carries both, as Gemini's does.
		`{"candidates":[{"content":{"parts":[{"text":""}],"role":"model"},"finishReason":"STOP","index":0}],` +
			`"usageMetadata":{"promptTokenCount":14,"candidatesTokenCount":2,"totalTokenCount":16},"modelVersion":"gemini-test","responseId":"resp-1"}`,
	}, events)
}

func TestStreamConverter_FunctionCallsAreSentWhole(t *testing.T) {
	events, err := convert(t, chatSSE(
		`{"id":"resp-2","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"get_weather","arguments":""},"extra_content":{"google":{"thought_signature":"sig-1"}}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","function":{"name":"now","arguments":""}},{"index":0,"function":{"arguments":"\"Paris\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	), ResponseOptions{})
	require.NoError(t, err)

	// Chat streams argument fragments; Gemini clients expect each call whole.
	assert.Equal(t, []string{
		`{"candidates":[{"content":{"parts":[` +
			`{"functionCall":{"id":"call_a","name":"get_weather","args":{"city":"Paris"}},"thoughtSignature":"sig-1"},` +
			`{"functionCall":{"id":"call_b","name":"now","args":{}}}` +
			`],"role":"model"},"finishReason":"STOP","index":0}],"modelVersion":"fallback-model","responseId":"resp-2"}`,
	}, events)
}

func TestStreamConverter_Thoughts(t *testing.T) {
	stream := func() io.ReadCloser {
		return chatSSE(
			`{"id":"resp-3","choices":[{"index":0,"delta":{"reasoning_content":"pondering"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"391","extra_content":{"google":{"thought_signature":"sig-text"}}}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":9,"completion_tokens_details":{"reasoning_tokens":6}}}`,
			`[DONE]`,
		)
	}
	final := `{"candidates":[{"content":{"parts":[{"text":"","thoughtSignature":"sig-text"}],"role":"model"},"finishReason":"MAX_TOKENS","index":0}],` +
		`"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":3,"thoughtsTokenCount":6,"totalTokenCount":14},"modelVersion":"fallback-model","responseId":"resp-3"}`
	answer := `{"candidates":[{"content":{"parts":[{"text":"391"}],"role":"model"},"index":0}],"modelVersion":"fallback-model","responseId":"resp-3"}`
	thought := `{"candidates":[{"content":{"parts":[{"text":"pondering","thought":true}],"role":"model"},"index":0}],"modelVersion":"fallback-model","responseId":"resp-3"}`

	events, err := convert(t, stream(), ResponseOptions{})
	require.NoError(t, err)
	assert.Equal(t, []string{answer, final}, events)

	events, err = convert(t, stream(), ResponseOptions{IncludeThoughts: true})
	require.NoError(t, err)
	assert.Equal(t, []string{thought, answer, final}, events)
}

func TestStreamConverter_FinishWithoutDoneIsComplete(t *testing.T) {
	events, err := convert(t, chatSSE(
		`{"id":"resp-4","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":"stop"}]}`,
	), ResponseOptions{})
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Contains(t, events[1], `"finishReason":"STOP"`)
}

func TestStreamConverter_TruncatedStreamEndsWithAnError(t *testing.T) {
	events, err := convert(t, chatSSE(
		`{"id":"resp-5","choices":[{"index":0,"delta":{"content":"Par"}}]}`,
	), ResponseOptions{})

	// A stream that stops before its finish must not look like a finished turn.
	require.ErrorIs(t, err, streaming.ErrStreamIncomplete)
	require.Len(t, events, 2)
	assert.Equal(t, `{"error":{"code":500,"message":"provider stream ended before completion","status":"INTERNAL"}}`, events[1])
}

func TestStreamConverter_UpstreamErrorEndsWithItsMessage(t *testing.T) {
	events, err := convert(t, chatSSE(
		`{"id":"resp-6","choices":[{"index":0,"delta":{"content":"Par"},"finish_reason":"stop"}]}`,
		`{"error":{"message":"upstream overloaded","type":"server_error"}}`,
		`[DONE]`,
	), ResponseOptions{})

	require.ErrorIs(t, err, streaming.ErrStreamIncomplete)
	require.Len(t, events, 2)
	assert.Equal(t, `{"error":{"code":500,"message":"upstream overloaded","status":"INTERNAL"}}`, events[1])
}

func TestStreamConverter_CloseIsIdempotent(t *testing.T) {
	converter := NewStreamConverter(chatSSE(`[DONE]`), "m", ResponseOptions{})
	require.NoError(t, converter.Close())
	require.NoError(t, converter.Close())
	n, err := converter.Read(make([]byte, 8))
	assert.Zero(t, n)
	assert.Equal(t, io.EOF, err)
}
