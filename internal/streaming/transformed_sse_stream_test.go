package streaming

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
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

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestTransformedSSEStream_PassThroughIsByteIdentical(t *testing.T) {
	for _, readSize := range []int{1, 5, 64, 4096} {
		upstream := &trackingCloser{Reader: &chunkedReader{data: []byte(chatFixture), n: readSize}}
		tr := &funcTransformer{}
		stream := NewTransformedSSEStream(upstream, ChatCodec(), tr, TransformOptions{})
		got, err := readAllSmall(t, stream)
		if err != nil {
			t.Fatalf("read size %d: %v", readSize, err)
		}
		if string(got) != chatFixture {
			t.Errorf("read size %d: output differs from upstream:\n%s", readSize, got)
		}
		want := []EventKind{KindOther, KindTextDelta, KindTextDelta, KindTextDelta, KindFinish, KindUsage}
		if got := kinds(tr.seen); len(got) != len(want) || strings.Join(kindStrings(got), ",") != strings.Join(kindStrings(want), ",") {
			t.Errorf("read size %d: transformer saw %v, want %v", readSize, got, want)
		}
		for i, ev := range tr.seen {
			if ev.Seq != i {
				t.Errorf("event %d has Seq %d", i, ev.Seq)
			}
		}
		if err := stream.Close(); err != nil || !upstream.closed {
			t.Errorf("Close: err=%v closed=%v", err, upstream.closed)
		}
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
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)
	if strings.Contains(out, "555-") || !strings.Contains(out, `"content":"call [phone]"`) {
		t.Errorf("replacement missing:\n%s", out)
	}
	if strings.Contains(out, `"usage"`) {
		t.Errorf("dropped usage chunk still present:\n%s", out)
	}
	if !strings.Contains(out, `"finish_reason":"stop"`) || !strings.HasSuffix(out, "data: [DONE]\n\n") {
		t.Errorf("finish chunk or [DONE] missing:\n%s", out)
	}
	if !strings.Contains(out, ": keep-alive\n\n") {
		t.Errorf("comment not relayed:\n%s", out)
	}
	if len(reported) != 1 || !errors.Is(reported[0], ErrNotTextEvent) {
		t.Errorf("reported errors = %v", reported)
	}
	resp, err := AssembleChatResponse(decodeChatEvents(t, got))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Choices[0].Message.Content != "Hello, call [phone]1234 now" {
		t.Errorf("assembled content = %q", resp.Choices[0].Message.Content)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)
	if !upstream.closed {
		t.Error("upstream not closed on terminate")
	}
	if strings.Contains(out, "555-") || strings.Contains(out, "1234 now") {
		t.Errorf("content after the cut leaked:\n%s", out)
	}
	if !strings.Contains(out, `"content":"[blocked]"`) || !strings.Contains(out, `"finish_reason":"content_filter"`) {
		t.Errorf("terminal chunks missing:\n%s", out)
	}
	if strings.Count(out, "[DONE]") != 1 || !strings.HasSuffix(out, "data: [DONE]\n\n") {
		t.Errorf("exactly one trailing [DONE] expected:\n%s", out)
	}
	if len(tr.seen) != 3 {
		t.Errorf("transformer saw %d events after terminate, want 3", len(tr.seen))
	}
	n, err := stream.Read(make([]byte, 8))
	if n != 0 || err != io.EOF {
		t.Errorf("Read after termination = (%d, %v), want (0, EOF)", n, err)
	}
	resp, err := AssembleChatResponse(decodeChatEvents(t, got))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Choices[0].Message.Content != "Hello, [blocked]" || resp.Choices[0].FinishReason != "content_filter" {
		t.Errorf("assembled = %+v", resp.Choices[0])
	}
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
			if err != nil {
				t.Fatal(err)
			}
			out := string(got)
			if !strings.Contains(out, `"code":"plugin_failure"`) || !strings.HasSuffix(out, "data: [DONE]\n\n") {
				t.Errorf("fail-closed terminal events missing:\n%s", out)
			}
			if strings.Count(out, "[DONE]") != 1 {
				t.Errorf("exactly one [DONE] expected:\n%s", out)
			}
			if len(reported) != 1 || !errors.Is(reported[0], boom) {
				t.Errorf("reported = %v", reported)
			}
			if !upstream.closed {
				t.Error("upstream not closed")
			}
		})
	}
}

func TestTransformedSSEStream_OnEndTerminatesBeforeDone(t *testing.T) {
	tr := &funcTransformer{onEnd: func() (*Termination, error) {
		return &Termination{FinishReason: "length"}, nil
	}}
	stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(chatFixture)), ChatCodec(), tr, TransformOptions{})
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)
	if strings.Count(out, "[DONE]") != 1 || !strings.HasSuffix(out, "data: [DONE]\n\n") {
		t.Errorf("exactly one trailing [DONE] expected:\n%s", out)
	}
	if !strings.Contains(out, `"finish_reason":"length"`) {
		t.Errorf("OnEnd termination missing:\n%s", out)
	}
}

func TestTransformedSSEStream_UpstreamWithoutDoneStillCallsOnEnd(t *testing.T) {
	ended := false
	tr := &funcTransformer{onEnd: func() (*Termination, error) { ended = true; return nil, nil }}
	input := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"
	stream := NewTransformedSSEStream(io.NopCloser(strings.NewReader(input)), ChatCodec(), tr, TransformOptions{})
	got, err := io.ReadAll(stream)
	if err != nil || string(got) != input || !ended {
		t.Errorf("got %q err=%v ended=%v", got, err, ended)
	}
}

func TestTransformedSSEStream_UpstreamErrorIsPropagatedAfterOutput(t *testing.T) {
	failing := io.MultiReader(strings.NewReader("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"), &errReader{err: io.ErrUnexpectedEOF})
	stream := NewTransformedSSEStream(io.NopCloser(failing), ChatCodec(), &funcTransformer{}, TransformOptions{})
	got, err := io.ReadAll(stream)
	if err != io.ErrUnexpectedEOF {
		t.Errorf("err = %v, want ErrUnexpectedEOF", err)
	}
	if !strings.Contains(string(got), `"content":"hi"`) {
		t.Errorf("output before the failure missing: %q", got)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if !sawPattern {
		t.Fatalf("pattern spanning two chunks was not visible in one event; transformer saw %q", texts(tr.seen))
	}
	out := string(got)
	if strings.Contains(out, "555-1234") {
		t.Errorf("pattern leaked:\n%s", out)
	}
	// Three overlapping windows, then the tail flushed before the finish event.
	if want := []EventKind{KindOther, KindTextDelta, KindTextDelta, KindTextDelta, KindTextDelta, KindFinish, KindUsage}; strings.Join(kindStrings(kinds(tr.seen)), ",") != strings.Join(kindStrings(want), ",") {
		t.Errorf("transformer saw %v, want %v", kinds(tr.seen), want)
	}
	if want := []string{"Hello, ", "Hello, call 555-", "all 555-1234 now", "one] now"}; !equalStrings(texts(tr.seen), want) {
		t.Errorf("windows = %q, want %q", texts(tr.seen), want)
	}
	var overlaps []int
	for _, ev := range tr.seen {
		if ev.Kind == KindTextDelta {
			overlaps = append(overlaps, ev.Overlap)
			if !strings.Contains(string(ev.Data), `"content":"`+jsonEscape(ev.Text)+`"`) {
				t.Errorf("event Data does not carry the window text: %s vs %q", ev.Data, ev.Text)
			}
		}
	}
	if want := []int{0, 7, 8, 8}; !reflect.DeepEqual(overlaps, want) {
		t.Errorf("overlaps = %v, want %v", overlaps, want)
	}
	resp, err := AssembleChatResponse(decodeChatEvents(t, got))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Choices[0].Message.Content != "Hello, call [phone] now" || resp.Choices[0].FinishReason != "stop" {
		t.Errorf("assembled = %+v", resp.Choices[0])
	}
	if !strings.HasSuffix(out, "data: [DONE]\n\n") {
		t.Errorf("[DONE] missing:\n%s", out)
	}
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
			if err != nil {
				t.Fatal(err)
			}
			if !equalStrings(texts(tr.seen), tt.wantTexts) {
				t.Errorf("transformer saw %q, want %q", texts(tr.seen), tt.wantTexts)
			}
			if emitted := texts(decodeChatEvents(t, got)); !equalStrings(emitted, tt.wantEmit) {
				t.Errorf("emitted deltas = %q, want %q", emitted, tt.wantEmit)
			}
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
	if err != nil {
		t.Fatal(err)
	}
	want := []EventKind{KindTextDelta, KindTextDelta, KindToolCallDelta, KindTextDelta, KindTextDelta, KindFinish}
	if strings.Join(kindStrings(kinds(tr.seen)), ",") != strings.Join(kindStrings(want), ",") {
		t.Errorf("transformer saw %v, want %v", kinds(tr.seen), want)
	}
	emitted := decodeChatEvents(t, got)
	wantOut := []EventKind{KindTextDelta, KindToolCallDelta, KindTextDelta, KindFinish, KindDone}
	if strings.Join(kindStrings(kinds(emitted)), ",") != strings.Join(kindStrings(wantOut), ",") {
		t.Errorf("output order %v, want %v", kinds(emitted), wantOut)
	}
	resp, err := AssembleChatResponse(emitted)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Choices[0].Message.Content != "abcdef" || len(resp.Choices[0].Message.ToolCalls) != 1 || resp.Choices[0].FinishReason != "tool_calls" {
		t.Errorf("assembled = %+v", resp.Choices[0])
	}
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
	if err != nil {
		t.Fatal(err)
	}
	out := string(got)
	if strings.Contains(out, "sk-abc123") {
		t.Errorf("secret leaked:\n%s", out)
	}
	if !strings.Contains(out, "event: response.incomplete\n") || !strings.HasSuffix(out, "data: [DONE]\n\n") {
		t.Errorf("terminal events missing:\n%s", out)
	}
	resp, err := AssembleResponsesResponse(decodeResponsesEvents(t, got))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != "incomplete" || len(resp.Output) != 1 || resp.Output[0].Content[0].Text != "my key [redacted]" {
		t.Errorf("assembled = %+v", resp)
	}
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
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal("second Close must be a no-op")
	}
	if _, err := stream.Read(make([]byte, 4)); err != ErrStreamClosed {
		t.Errorf("Read after Close err = %v", err)
	}
}
