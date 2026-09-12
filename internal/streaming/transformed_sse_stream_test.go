package streaming

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const chatFixture = `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

: keep-alive

data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"Hello, "},"finish_reason":null}]}

data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"call 555-"},"finish_reason":null}]}

data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"1234 now"},"finish_reason":null}]}

data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}

data: [DONE]

`

// funcTransformer adapts closures to Transformer.
type funcTransformer struct {
	onEvent func(ev *Event) (Decision, error)
	onEnd   func() (*Termination, error)
	seen    []Event
}

func (f *funcTransformer) OnEvent(ev *Event) (Decision, error) {
	copied := *ev
	copied.Data = append([]byte(nil), ev.Data...)
	f.seen = append(f.seen, copied)
	if f.onEvent == nil {
		return Decision{Action: ActionPass}, nil
	}
	return f.onEvent(ev)
}

func (f *funcTransformer) OnEnd() (*Termination, error) {
	if f.onEnd == nil {
		return nil, nil
	}
	return f.onEnd()
}

// trackingCloser records Close calls and can hand out bytes in small reads.
type trackingCloser struct {
	io.Reader
	closed bool
}

func (c *trackingCloser) Close() error {
	c.closed = true
	return nil
}

// chunkedReader returns at most n bytes per Read to exercise boundaries.
type chunkedReader struct {
	data []byte
	n    int
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := min(r.n, len(p), len(r.data))
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

func readAllSmall(t *testing.T, r io.Reader) ([]byte, error) {
	t.Helper()
	var out []byte
	buf := make([]byte, 7)
	for {
		n, err := r.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			if err == io.EOF {
				return out, nil
			}
			return out, err
		}
	}
}

func kinds(events []Event) []EventKind {
	out := make([]EventKind, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.Kind)
	}
	return out
}

func texts(events []Event) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		if ev.Kind == KindTextDelta {
			out = append(out, ev.Text)
		}
	}
	return out
}

func TestTransformedSSEStream_PassThroughIsByteIdentical(t *testing.T) {
	for _, readSize := range []int{1, 5, 64, 4096} {
		upstream := &trackingCloser{Reader: &chunkedReader{data: []byte(chatFixture), n: readSize}}
		tr := &funcTransformer{}
		stream := NewTransformedSSEStream(upstream, ChatCodec(), tr, TransformOptions{})
		got, err := readAllSmall(t, stream)
		require.NoError(t, err, "read size %d", readSize)
		assert.Equal(t, chatFixture, string(got), "read size %d: output differs from upstream", readSize)

		want := []EventKind{KindOther, KindTextDelta, KindTextDelta, KindTextDelta, KindFinish, KindUsage}
		assert.Equal(t, kindStrings(want), kindStrings(kinds(tr.seen)), "read size %d", readSize)
		for i, ev := range tr.seen {
			assert.Equal(t, i, ev.Seq)
		}
		err = stream.Close()
		assert.NoError(t, err)
		assert.True(t, upstream.closed)
	}
}

func kindStrings(k []EventKind) []string {
	out := make([]string, len(k))
	for i := range k {
		out[i] = string(k[i])
	}
	return out
}

func TestTransformedSSEStream_ReplaceAndDrop(t *testing.T) {
	tr := &funcTransformer{onEvent: func(ev *Event) (Decision, error) {
		switch {
		case ev.Kind == KindTextDelta && strings.Contains(ev.Text, "555-"):
			return Decision{Action: ActionReplace, Text: "call [phone]"}, nil
		case ev.Kind == KindUsage:
			return Decision{Action: ActionDrop}, nil
		case ev.Kind == KindFinish:
			// Replacing a non-text event is an error that is reported and treated as pass.
			return Decision{Action: ActionReplace, Text: "x"}, nil
		}
		return Decision{Action: ActionPass}, nil
	}}
	var reported []error
	upstream := &trackingCloser{Reader: strings.NewReader(chatFixture)}
	stream := NewTransformedSSEStream(upstream, ChatCodec(), tr, TransformOptions{OnError: func(err error) { reported = append(reported, err) }})
	got, err := io.ReadAll(stream)
	require.NoError(t, err)

	out := string(got)
	assert.NotContains(t, out, "555-")
	assert.Contains(t, out, `"content":"call [phone]"`)
	assert.NotContains(t, out, `"usage"`, "dropped usage chunk still present:\n%s", out)
	assert.Contains(t, out, `"finish_reason":"stop"`)
	assert.True(t, strings.HasSuffix(out, "data: [DONE]\n\n"))
	assert.Contains(t, out, ": keep-alive\n\n")
	assert.Len(t, reported, 1)
	assert.ErrorIs(t, reported[0], ErrNotTextEvent)

	resp, err := AssembleChatResponse(decodeChatEvents(t, got))
	require.NoError(t, err)
	assert.Equal(t, "Hello, call [phone]1234 now", resp.Choices[0].Message.Content)
}

func decodeChatEvents(t *testing.T, stream []byte) []Event {
	t.Helper()
	codec := ChatCodec()
	var events []Event
	for i, raw := range scanAll(t, &EventScanner{}, string(stream)) {
		if raw.Comment {
			continue
		}
		events = append(events, codec.Decode(raw, i))
	}
	return events
}

func TestTransformedSSEStream_TerminateMidStream(t *testing.T) {
	tr := &funcTransformer{onEvent: func(ev *Event) (Decision, error) {
		if ev.Kind == KindTextDelta && strings.Contains(ev.Text, "555-") {
			return Decision{Action: ActionTerminate, Terminate: &Termination{Text: "[blocked]"}}, nil
		}
		return Decision{Action: ActionPass}, nil
	}}
	upstream := &trackingCloser{Reader: strings.NewReader(chatFixture)}
	stream := NewTransformedSSEStream(upstream, ChatCodec(), tr, TransformOptions{})
	got, err := io.ReadAll(stream)
	require.NoError(t, err)

	out := string(got)
	assert.True(t, upstream.closed)
	assert.NotContains(t, out, "555-")
	assert.NotContains(t, out, "1234 now", "content after the cut leaked:\n%s", out)
	assert.Contains(t, out, `"content":"[blocked]"`)
	assert.Contains(t, out, `"finish_reason":"content_filter"`)
	assert.Equal(t, 1, strings.Count(out, "[DONE]"))
	assert.True(t, strings.HasSuffix(out, "data: [DONE]\n\n"), "exactly one trailing [DONE] expected:\n%s", out)
	assert.Len(t, tr.seen, 3)

	n, err := stream.Read(make([]byte, 8))
	assert.Equal(t, 0, n)
	assert.Equal(t, io.EOF, err)

	resp, err := AssembleChatResponse(decodeChatEvents(t, got))
	require.NoError(t, err)
	assert.Equal(t, "Hello, [blocked]", resp.Choices[0].Message.Content)
	assert.Equal(t, "content_filter", resp.Choices[0].FinishReason, "assembled = %+v", resp.Choices[0])
}

func TestTransformedSSEStream_TransformerErrorFailsClosed(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name string
		tr   *funcTransformer
	}{
		{"OnEvent error", &funcTransformer{onEvent: func(ev *Event) (Decision, error) {
			if ev.Kind == KindTextDelta {
				return Decision{}, boom
			}
			return Decision{Action: ActionPass}, nil
		}}},
		{"OnEnd error", &funcTransformer{onEnd: func() (*Termination, error) { return nil, boom }}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reported []error
			upstream := &trackingCloser{Reader: strings.NewReader(chatFixture)}
			stream := NewTransformedSSEStream(upstream, ChatCodec(), tt.tr, TransformOptions{OnError: func(err error) { reported = append(reported, err) }})
			got, err := io.ReadAll(stream)
			require.NoError(t, err)

			out := string(got)
			assert.Contains(t, out, `"code":"plugin_failure"`)
			assert.True(t, strings.HasSuffix(out, "data: [DONE]\n\n"))
			assert.Equal(t, 1, strings.Count(out, "[DONE]"), "exactly one [DONE] expected:\n%s", out)
			assert.Len(t, reported, 1)
			assert.ErrorIs(t, reported[0], boom)
			assert.True(t, upstream.closed)
		})
	}
}

func TestTransformedSSEStream_OnEndTerminatesBeforeDone(t *testing.T) {
	tr := &funcTransformer{onEnd: func() (*Termination, error) {
		return &Termination{FinishReason: "length"}, nil
	}}
	stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(chatFixture)), ChatCodec(), tr, TransformOptions{})
	got, err := io.ReadAll(stream)
	require.NoError(t, err)

	out := string(got)
	assert.Equal(t, 1, strings.Count(out, "[DONE]"))
	assert.True(t, strings.HasSuffix(out, "data: [DONE]\n\n"), "exactly one trailing [DONE] expected:\n%s", out)

	// The provider already finished the only choice, so the termination adds
	// no second finish_reason.
	assert.NotContains(t, out, `"finish_reason":"length"`)
	assert.Equal(t, 1, strings.Count(out, `"finish_reason":"stop"`), "want the provider's finish once and no termination finish:\n%s", out)
}

func TestTransformedSSEStream_UpstreamWithoutDoneStillCallsOnEnd(t *testing.T) {
	ended := false
	tr := &funcTransformer{onEnd: func() (*Termination, error) { ended = true; return nil, nil }}
	input := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"
	stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(input)), ChatCodec(), tr, TransformOptions{})
	got, err := io.ReadAll(stream)
	assert.NoError(t, err)
	assert.Equal(t, input, string(got))
	assert.True(t, ended)
}

func TestTransformedSSEStream_UpstreamErrorIsPropagatedAfterOutput(t *testing.T) {
	failing := io.MultiReader(strings.NewReader("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"), &errReader{err: io.ErrUnexpectedEOF})
	stream := NewTransformedSSEStream(io.NopCloser(failing), ChatCodec(), &funcTransformer{}, TransformOptions{})
	got, err := io.ReadAll(stream)
	assert.Equal(t, io.ErrUnexpectedEOF, err)
	assert.Contains(t, string(got), `"content":"hi"`, "output before the failure missing: %q", got)
}

type errReader struct{ err error }

func (r *errReader) Read([]byte) (int, error) { return 0, r.err }

func TestTransformedSSEStream_LookbehindJoinsPatternAcrossChunks(t *testing.T) {
	var sawPattern bool
	tr := &funcTransformer{onEvent: func(ev *Event) (Decision, error) {
		if ev.Kind == KindTextDelta && strings.Contains(ev.Text, "555-1234") {
			sawPattern = true
			return Decision{Action: ActionReplace, Text: strings.ReplaceAll(ev.Text, "555-1234", "[phone]")}, nil
		}
		return Decision{Action: ActionPass}, nil
	}}
	stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(chatFixture)), ChatCodec(), tr, TransformOptions{LookbehindChars: 8})
	got, err := io.ReadAll(stream)
	require.NoError(t, err)
	require.True(t, sawPattern, "pattern spanning two chunks was not visible in one event; transformer saw %q", texts(tr.seen))

	out := string(got)
	assert.NotContains(t, out, "555-1234", "pattern leaked:\n%s", out)
	// Three overlapping windows, then the tail flushed before the finish event.
	want := []EventKind{KindOther, KindTextDelta, KindTextDelta, KindTextDelta, KindTextDelta, KindFinish, KindUsage}
	assert.Equal(t, kindStrings(want), kindStrings(kinds(tr.seen)))
	assert.Equal(t, []string{"Hello, ", "Hello, call 555-", "all 555-1234 now", "one] now"}, texts(tr.seen), "windows")
	var overlaps []int
	var finals []bool
	for _, ev := range tr.seen {
		if ev.Kind == KindTextDelta {
			overlaps = append(overlaps, ev.Overlap)
			finals = append(finals, ev.Final)
			assert.Contains(t, string(ev.Data), `"content":"`+jsonEscape(ev.Text)+`"`, "event Data does not carry the window text: %s vs %q", ev.Data, ev.Text)
		}
	}
	assert.Equal(t, []int{0, 7, 8, 8}, overlaps, "overlaps")
	// Only the flush before the finish event closes the window.
	assert.Equal(t, []bool{false, false, false, true}, finals, "finals")
	resp, err := AssembleChatResponse(decodeChatEvents(t, got))
	require.NoError(t, err)
	assert.Equal(t, "Hello, call [phone] now", resp.Choices[0].Message.Content)
	assert.Equal(t, "stop", resp.Choices[0].FinishReason, "assembled = %+v", resp.Choices[0])
	assert.True(t, strings.HasSuffix(out, "data: [DONE]\n\n"), "[DONE] missing:\n%s", out)
}

func jsonEscape(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}

func TestTransformedSSEStream_LookbehindRule(t *testing.T) {
	chunk := func(text string) string {
		return `data: {"choices":[{"index":0,"delta":{"content":"` + text + `"}}]}` + "\n\n"
	}
	tests := []struct {
		name       string
		lookbehind int
		input      string
		drop       string
		wantTexts  []string
		wantEmit   []string
	}{
		{
			name:       "windows overlap by the withheld tail",
			lookbehind: 4,
			input:      chunk("ab") + chunk("cd") + chunk("efg") + "data: [DONE]\n\n",
			wantTexts:  []string{"ab", "abcd", "abcdefg", "defg"},
			wantEmit:   []string{"abc", "defg"},
		},
		{
			name:       "tail flushed at end without DONE",
			lookbehind: 10,
			input:      chunk("hello"),
			wantTexts:  []string{"hello", "hello"},
			wantEmit:   []string{"hello"},
		},
		{
			name:       "tool call flushes pending text first",
			lookbehind: 10,
			input:      chunk("he") + chunk("llo") + `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}` + "\n\n",
			wantTexts:  []string{"he", "hello", "hello"},
			wantEmit:   []string{"hello"},
		},
		{
			name:       "multibyte characters count as one",
			lookbehind: 2,
			input:      chunk("héllo") + chunk("wörld"),
			wantTexts:  []string{"héllo", "lowörld", "ld"},
			wantEmit:   []string{"hél", "lowör", "ld"},
		},
		{
			name:       "choices are held separately",
			lookbehind: 3,
			input:      chunk("aaaa") + `data: {"choices":[{"index":1,"delta":{"content":"bbbb"}}]}` + "\n\n" + chunk("cccc"),
			wantTexts:  []string{"aaaa", "bbbb", "aaacccc", "ccc", "bbb"},
			wantEmit:   []string{"a", "b", "aaac", "ccc", "bbb"},
		},
		{
			name:       "drop discards the window including the tail",
			lookbehind: 2,
			input:      chunk("abc") + chunk("def") + chunk("ghi"),
			drop:       "bcdef",
			wantTexts:  []string{"abc", "bcdef", "ghi", "hi"},
			wantEmit:   []string{"a", "g", "hi"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := &funcTransformer{onEvent: func(ev *Event) (Decision, error) {
				if tt.drop != "" && ev.Text == tt.drop {
					return Decision{Action: ActionDrop}, nil
				}
				return Decision{Action: ActionPass}, nil
			}}
			stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(tt.input)), ChatCodec(), tr, TransformOptions{LookbehindChars: tt.lookbehind})
			got, err := io.ReadAll(stream)
			require.NoError(t, err)
			assert.Equal(t, tt.wantTexts, texts(tr.seen), "transformer texts")
			emitted := texts(decodeChatEvents(t, got))
			assert.Equal(t, tt.wantEmit, emitted, "emitted deltas")
		})
	}
}

func TestTransformedSSEStream_LookbehindOrderingWithToolCallAndFinish(t *testing.T) {
	input := `data: {"choices":[{"index":0,"delta":{"content":"abc"}}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"f","arguments":"{}"}}]}}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{"content":"def"}}]}` + "\n\n" +
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	tr := &funcTransformer{}
	stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(input)), ChatCodec(), tr, TransformOptions{LookbehindChars: 16})
	got, err := io.ReadAll(stream)
	require.NoError(t, err)

	// The tool call's arguments are a window of their own: seen once on
	// arrival and once more when the next text delta flushes them.
	want := []EventKind{KindTextDelta, KindTextDelta, KindToolCallDelta, KindToolCallDelta, KindTextDelta, KindTextDelta, KindFinish}
	assert.Equal(t, kindStrings(want), kindStrings(kinds(tr.seen)))

	emitted := decodeChatEvents(t, got)
	wantOut := []EventKind{KindTextDelta, KindToolCallDelta, KindTextDelta, KindFinish, KindDone}
	assert.Equal(t, kindStrings(wantOut), kindStrings(kinds(emitted)), "output order")

	resp, err := AssembleChatResponse(emitted)
	require.NoError(t, err)
	assert.Equal(t, "abcdef", resp.Choices[0].Message.Content)
	assert.Len(t, resp.Choices[0].Message.ToolCalls, 1)
	assert.Equal(t, "tool_calls", resp.Choices[0].FinishReason, "assembled = %+v", resp.Choices[0])
}

func TestTransformedSSEStream_ResponsesDialect(t *testing.T) {
	input := "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"r1\",\"model\":\"m\",\"created_at\":1}}\n\n" +
		"event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"id\":\"msg\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n" +
		"event: response.content_part.added\ndata: {\"type\":\"response.content_part.added\",\"sequence_number\":2,\"item_id\":\"msg\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":3,\"item_id\":\"msg\",\"output_index\":0,\"content_index\":0,\"delta\":\"my key is sk-\"}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":4,\"item_id\":\"msg\",\"output_index\":0,\"content_index\":0,\"delta\":\"abc123 ok\"}\n\n" +
		"event: response.output_text.done\ndata: {\"type\":\"response.output_text.done\",\"sequence_number\":5,\"text\":\"my key is sk-abc123 ok\"}\n\n"
	tr := &funcTransformer{onEvent: func(ev *Event) (Decision, error) {
		if ev.Kind == KindTextDelta && strings.Contains(ev.Text, "sk-abc123") {
			return Decision{Action: ActionTerminate, Terminate: &Termination{Text: "[redacted]"}}, nil
		}
		return Decision{Action: ActionPass}, nil
	}}
	stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(input)), ResponsesCodec(), tr, TransformOptions{LookbehindChars: 6})
	got, err := io.ReadAll(stream)
	require.NoError(t, err)

	out := string(got)
	assert.NotContains(t, out, "sk-abc123", "secret leaked:\n%s", out)
	assert.Contains(t, out, "event: response.incomplete\n")
	assert.True(t, strings.HasSuffix(out, "data: [DONE]\n\n"))

	resp, err := AssembleResponsesResponse(decodeResponsesEvents(t, got))
	require.NoError(t, err)
	assert.Equal(t, "incomplete", resp.Status)
	assert.Len(t, resp.Output, 1)
	assert.Equal(t, "my key [redacted]", resp.Output[0].Content[0].Text, "assembled = %+v", resp)
}

func decodeResponsesEvents(t *testing.T, stream []byte) []Event {
	t.Helper()
	codec := ResponsesCodec()
	var events []Event
	for i, raw := range scanAll(t, &EventScanner{}, string(stream)) {
		if raw.Comment {
			continue
		}
		events = append(events, codec.Decode(raw, i))
	}
	return events
}

func TestTransformedSSEStream_ReadAfterClose(t *testing.T) {
	stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(chatFixture)), ChatCodec(), &funcTransformer{}, TransformOptions{})
	err := stream.Close()
	require.NoError(t, err)
	require.NoError(t, stream.Close())
	_, err = stream.Read(make([]byte, 4))
	assert.Equal(t, ErrStreamClosed, err)
}

func TestTransformedSSEStream_OversizedEventFailsClosed(t *testing.T) {
	big := `data: {"id":"c1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"` + strings.Repeat("secret ", 40) + `"}}]}` + "\n\n"
	input := ": keep-alive\n\n" + big + "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"after\"}}]}\n\ndata: [DONE]\n\n"
	// The limit must not depend on how upstream splits its reads: the event
	// is rejected whether it arrives in small chunks or complete in one read.
	for _, tt := range []struct {
		name   string
		reader io.Reader
	}{
		{"chunked", &chunkedReader{data: []byte(input), n: 64}},
		{"one read", strings.NewReader(input)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var reported error
			tr := &funcTransformer{}
			upstream := &trackingCloser{Reader: tt.reader}
			stream := NewTransformedSSEStream(upstream, ChatCodec(), tr, TransformOptions{MaxEventBytes: 128, OnError: func(err error) { reported = err }})
			got, err := io.ReadAll(stream)
			require.NoError(t, err)

			out := string(got)
			assert.True(t, strings.HasPrefix(out, ": keep-alive\n\n"), "comment not relayed:\n%s", out)
			assert.NotContains(t, out, "secret")
			assert.NotContains(t, out, "after", "uninspected content leaked:\n%s", out)
			assert.Contains(t, out, `"code":"event_too_large"`)
			assert.True(t, strings.HasSuffix(out, "data: [DONE]\n\n"))
			assert.ErrorIs(t, reported, ErrEventTooLarge)
			assert.Empty(t, tr.seen)
			assert.True(t, upstream.closed)
		})
	}
}

// A chunk that carries text together with finish_reason and usage (Gemini
// ends its streams this way) is re-segmented under lookbehind. Those members
// must reach the client once, after the chunk's last text, even when that
// text is replaced with nothing or dropped.
func TestTransformedSSEStream_LookbehindDeliversFinishAndUsageOnce(t *testing.T) {
	input := `data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hello "},"finish_reason":null}],"usage":null}` + "\n\n" +
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"world"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}` + "\n\n" +
		"data: [DONE]\n\n"
	tests := []struct {
		name    string
		onEvent func(ev *Event) (Decision, error)
		content string
	}{
		{name: "pass", content: "hello world"},
		{name: "final text replaced with nothing", onEvent: func(ev *Event) (Decision, error) {
			if strings.Contains(ev.Text, "world") {
				return Decision{Action: ActionReplace, Text: ""}, nil
			}
			return Decision{Action: ActionPass}, nil
		}, content: "hel"},
		{name: "final text dropped", onEvent: func(ev *Event) (Decision, error) {
			if strings.Contains(ev.Text, "world") {
				return Decision{Action: ActionDrop}, nil
			}
			return Decision{Action: ActionPass}, nil
		}, content: "hel"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := &funcTransformer{onEvent: tc.onEvent}
			stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(input)), ChatCodec(), tr, TransformOptions{LookbehindChars: 3})
			got, err := io.ReadAll(stream)
			require.NoError(t, err)

			out := string(got)
			n := strings.Count(out, `"finish_reason":"stop"`)
			assert.Equal(t, 1, n)
			n = strings.Count(out, `"total_tokens":5`)
			assert.Equal(t, 1, n)

			events := strings.Split(strings.TrimSpace(strings.TrimSuffix(out, "data: [DONE]\n\n")), "\n\n")
			last := events[len(events)-1]
			assert.Contains(t, last, `"finish_reason":"stop"`)

			resp, err := AssembleChatResponse(decodeChatEvents(t, got))
			require.NoError(t, err)
			assert.Equal(t, tc.content, resp.Choices[0].Message.Content)
			assert.Equal(t, "stop", resp.Choices[0].FinishReason)
			assert.Equal(t, 5, resp.Usage.TotalTokens, "assembled = %+v usage %+v", resp.Choices[0], resp.Usage)
		})
	}
}

// A truncated upstream is still a failure when OnEnd cuts the stream: the
// termination goes out, and the upstream error is reported after it.
func TestTransformedSSEStream_UpstreamErrorOutranksOnEndTermination(t *testing.T) {
	failing := io.MultiReader(strings.NewReader("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"), &errReader{err: io.ErrUnexpectedEOF})
	tr := &funcTransformer{onEnd: func() (*Termination, error) { return &Termination{FinishReason: "length"}, nil }}
	stream := NewTransformedSSEStream(io.NopCloser(failing), ChatCodec(), tr, TransformOptions{})
	got, err := io.ReadAll(stream)
	assert.Equal(t, io.ErrUnexpectedEOF, err)
	out := string(got)
	assert.Contains(t, out, `"finish_reason":"length"`)
	assert.True(t, strings.HasSuffix(out, "data: [DONE]\n\n"))
}

// A chunk carrying text and finish_reason together closes its choice, so a
// termination afterwards adds no second finish chunk.
func TestTransformedSSEStream_ContentWithFinishNeedsNoSecondFinish(t *testing.T) {
	input := `data: {"choices":[{"index":0,"delta":{"content":"hello world"},"finish_reason":"stop"}]}` + "\n\n"
	for _, lookbehind := range []int{0, 3} {
		t.Run(fmt.Sprintf("lookbehind %d", lookbehind), func(t *testing.T) {
			tr := &funcTransformer{onEnd: func() (*Termination, error) { return &Termination{FinishReason: "length"}, nil }}
			stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(input)), ChatCodec(), tr, TransformOptions{LookbehindChars: lookbehind})
			got, err := io.ReadAll(stream)
			require.NoError(t, err)

			out := string(got)
			n := strings.Count(out, "finish_reason")
			assert.Equal(t, 1, n)
			assert.Contains(t, out, `"finish_reason":"stop"`)
			assert.True(t, strings.HasSuffix(out, "data: [DONE]\n\n"), "stream not closed:\n%s", out)
		})
	}
}

func TestTransformedSSEStream_MinChunkRule(t *testing.T) {
	chunk := func(choice int, text string) string {
		return fmt.Sprintf(`data: {"choices":[{"index":%d,"delta":{"content":"%s"}}]}`+"\n\n", choice, text)
	}
	toolCall := `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}` + "\n\n"
	tests := []struct {
		name         string
		minChunk     int
		lookbehind   int
		input        string
		replace      map[string]string
		wantTexts    []string
		wantOverlaps []int
		wantEmit     []string
	}{
		{
			name:         "deltas are collected into runs of at least the minimum",
			minChunk:     4,
			input:        chunk(0, "ab") + chunk(0, "cd") + chunk(0, "efg") + "data: [DONE]\n\n",
			wantTexts:    []string{"abcd", "efg"},
			wantOverlaps: []int{0, 0},
			wantEmit:     []string{"abcd", "efg"},
		},
		{
			name:         "the lookbehind tail leads each run and counts as overlap",
			minChunk:     4,
			lookbehind:   2,
			input:        chunk(0, "ab") + chunk(0, "cd") + chunk(0, "efg") + chunk(0, "hi") + "data: [DONE]\n\n",
			wantTexts:    []string{"abcd", "cdefghi", "hi"},
			wantOverlaps: []int{0, 2, 2},
			wantEmit:     []string{"ab", "cdefg", "hi"},
		},
		{
			name:         "a non-text event flushes what is pending",
			minChunk:     10,
			input:        chunk(0, "he") + chunk(0, "llo") + toolCall,
			wantTexts:    []string{"hello"},
			wantOverlaps: []int{0, 0},
			wantEmit:     []string{"hello"},
		},
		{
			name:         "characters are counted as runes",
			minChunk:     3,
			input:        chunk(0, "hé") + chunk(0, "l") + chunk(0, "lo"),
			wantTexts:    []string{"hél", "lo"},
			wantOverlaps: []int{0, 0},
			wantEmit:     []string{"hél", "lo"},
		},
		{
			name:         "choices are collected separately",
			minChunk:     4,
			input:        chunk(0, "aa") + chunk(1, "bbbb") + chunk(0, "aaa"),
			wantTexts:    []string{"bbbb", "aaaaa"},
			wantOverlaps: []int{0, 0},
			wantEmit:     []string{"bbbb", "aaaaa"},
		},
		{
			name:         "a replace applies to the whole run",
			minChunk:     8,
			input:        chunk(0, "555-") + chunk(0, "1234") + chunk(0, " ok"),
			replace:      map[string]string{"555-1234": "[phone]"},
			wantTexts:    []string{"555-1234", " ok"},
			wantOverlaps: []int{0, 0},
			wantEmit:     []string{"[phone]", " ok"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := &funcTransformer{onEvent: func(ev *Event) (Decision, error) {
				if out, ok := tt.replace[ev.Text]; ok {
					return Decision{Action: ActionReplace, Text: out}, nil
				}
				return Decision{Action: ActionPass}, nil
			}}
			stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(tt.input)), ChatCodec(), tr, TransformOptions{LookbehindChars: tt.lookbehind, MinChunkChars: tt.minChunk})
			got, err := io.ReadAll(stream)
			require.NoError(t, err)
			assert.Equal(t, tt.wantTexts, texts(tr.seen), "transformer texts")

			var overlaps []int
			for _, ev := range tr.seen {
				overlaps = append(overlaps, ev.Overlap)
			}
			assert.Equal(t, tt.wantOverlaps, overlaps)
			emitted := texts(decodeChatEvents(t, got))
			assert.Equal(t, tt.wantEmit, emitted, "emitted deltas")
		})
	}
}

func TestTransformedSSEStream_MinChunkDeliversFinishAndUsageOnce(t *testing.T) {
	input := `data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hello "},"finish_reason":null}],"usage":null}` + "\n\n" +
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"world"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}` + "\n\n" +
		"data: [DONE]\n\n"
	tests := []struct {
		name    string
		onEvent func(ev *Event) (Decision, error)
		content string
	}{
		{name: "pass", content: "hello world"},
		{name: "run dropped", onEvent: func(ev *Event) (Decision, error) {
			return Decision{Action: ActionDrop}, nil
		}, content: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := &funcTransformer{onEvent: tc.onEvent}
			stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(input)), ChatCodec(), tr, TransformOptions{MinChunkChars: 100})
			got, err := io.ReadAll(stream)
			require.NoError(t, err)
			want := []string{"hello world"}
			assert.Equal(t, want, texts(tr.seen), "transformer texts")

			out := string(got)
			n := strings.Count(out, `"finish_reason":"stop"`)
			assert.Equal(t, 1, n)
			n = strings.Count(out, `"total_tokens":5`)
			assert.Equal(t, 1, n)

			resp, err := AssembleChatResponse(decodeChatEvents(t, got))
			require.NoError(t, err)
			assert.Equal(t, tc.content, resp.Choices[0].Message.Content)
			assert.Equal(t, "stop", resp.Choices[0].FinishReason)
			assert.Equal(t, 5, resp.Usage.TotalTokens, "assembled = %+v usage %+v", resp.Choices[0], resp.Usage)
		})
	}
}

// Coalescing renders a run from its first chunk, so members only that chunk
// carries (a chat delta's role) survive, while the finish that a later chunk
// carries still goes out exactly once after the text.
func TestTransformedSSEStream_MinChunkKeepsRunTemplateMembers(t *testing.T) {
	input := `data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}` + "\n\n" +
		`data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}` + "\n\n" +
		"data: [DONE]\n\n"
	tests := []struct {
		name      string
		minChunk  int
		wantSeen  []string
		wantEmit  []string
		wantRoles int
	}{
		{name: "whole run flushed at the end", minChunk: 100, wantSeen: []string{"Hello world"}, wantEmit: []string{"Hello world"}, wantRoles: 1},
		{name: "run emitted mid-stream", minChunk: 4, wantSeen: []string{"Hello", " world"}, wantEmit: []string{"Hello", " world"}, wantRoles: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tr := &funcTransformer{}
			stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(input)), ChatCodec(), tr, TransformOptions{MinChunkChars: tc.minChunk})
			got, err := io.ReadAll(stream)
			require.NoError(t, err)
			assert.Equal(t, tc.wantSeen, texts(tr.seen), "transformer texts")

			out := string(got)
			n := strings.Count(out, `"role":"assistant"`)
			assert.Equal(t, tc.wantRoles, n)
			n = strings.Count(out, `"finish_reason":"stop"`)
			assert.Equal(t, 1, n)
			n = strings.Count(out, `"total_tokens":5`)
			assert.Equal(t, 1, n)

			events := decodeChatEvents(t, got)
			emitted := texts(events)
			assert.Equal(t, tc.wantEmit, emitted, "emitted deltas")
			first := events[0]
			assert.Contains(t, string(first.Data), `"role":"assistant"`, "first emitted chunk lacks the role: %s", first.Data)

			resp, err := AssembleChatResponse(events)
			require.NoError(t, err)
			assert.Equal(t, "Hello world", resp.Choices[0].Message.Content)
			assert.Equal(t, "stop", resp.Choices[0].FinishReason)
			assert.Equal(t, 5, resp.Usage.TotalTokens, "assembled = %+v usage %+v", resp.Choices[0], resp.Usage)
		})
	}
}

func TestTransformedSSEStream_MinChunkIsCapped(t *testing.T) {
	chunk := strings.Repeat("x", 8000)
	input := ""
	for range 3 {
		input += `data: {"choices":[{"index":0,"delta":{"content":"` + chunk + `"}}]}` + "\n\n"
	}
	tr := &funcTransformer{}
	stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(input)), ChatCodec(), tr, TransformOptions{MinChunkChars: 1 << 30})
	s := stream.(*transformedSSEStream)
	require.Equal(t, MaxMinChunkChars, s.opts.MinChunkChars)
	_, err := io.ReadAll(stream)
	require.NoError(t, err)

	var lengths []int
	for _, ev := range tr.seen {
		lengths = append(lengths, len(ev.Text))
	}
	want := []int{24000}
	assert.Equal(t, want, lengths)
}
