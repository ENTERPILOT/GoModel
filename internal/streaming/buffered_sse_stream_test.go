package streaming

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestBufferedSSEStream_ReplayUnchanged(t *testing.T) {
	var got []Event
	finish := func(events []Event, raw []byte) ([]byte, error) {
		got = events
		if string(raw) != chatFixture {
			t.Errorf("finisher raw differs:\n%s", raw)
		}
		return nil, nil
	}
	stream := NewBufferedSSEStream(context.Background(), io.NopCloser(strings.NewReader(chatFixture)), ChatCodec(), finish, BufferOptions{})
	out, err := readAllSmall(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != chatFixture {
		t.Errorf("replay differs from upstream:\n%s", out)
	}
	want := []EventKind{KindOther, KindTextDelta, KindTextDelta, KindTextDelta, KindFinish, KindUsage, KindDone}
	if strings.Join(kindStrings(kinds(got)), ",") != strings.Join(kindStrings(want), ",") {
		t.Errorf("finisher saw %v, want %v", kinds(got), want)
	}
	for i, ev := range got {
		if ev.Seq != i {
			t.Errorf("event %d Seq = %d", i, ev.Seq)
		}
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBufferedSSEStream_SynthesizedReplay(t *testing.T) {
	finish := func(events []Event, raw []byte) ([]byte, error) {
		resp, err := AssembleChatResponse(events)
		if err != nil {
			return nil, err
		}
		resp.Choices[0].Message.Content = "replaced"
		return SynthesizeChatStream(resp, true), nil
	}
	stream := NewBufferedSSEStream(context.Background(), io.NopCloser(strings.NewReader(chatFixture)), ChatCodec(), finish, BufferOptions{})
	out, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "555-") || !strings.Contains(string(out), `"content":"replaced"`) {
		t.Errorf("synthesized replay not served:\n%s", out)
	}
	resp, err := AssembleChatResponse(decodeChatEvents(t, out))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Choices[0].Message.Content != "replaced" || resp.Usage.TotalTokens != 3 || resp.ID != "c1" {
		t.Errorf("assembled replay = %+v", resp)
	}
}

func TestBufferedSSEStream_KeepAliveWhileDraining(t *testing.T) {
	pr, pw := io.Pipe()
	stream := NewBufferedSSEStream(context.Background(), pr, ChatCodec(), nil, BufferOptions{KeepAliveInterval: 5 * time.Millisecond, KeepAliveComment: ": hold"})

	buf := make([]byte, 64)
	n, err := stream.Read(buf)
	if err != nil || string(buf[:n]) != ": hold\n\n" {
		t.Fatalf("first Read = %q, %v; want keep-alive comment", buf[:n], err)
	}

	go func() {
		_, _ = pw.Write([]byte(chatFixture))
		_ = pw.Close()
	}()
	out, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimPrefix(string(out), ": hold\n\n")
	for strings.HasPrefix(trimmed, ": hold\n\n") {
		trimmed = strings.TrimPrefix(trimmed, ": hold\n\n")
	}
	if trimmed != chatFixture {
		t.Errorf("replay after keep-alives differs:\n%s", out)
	}
}

func TestBufferedSSEStream_MaxBytesFailsClosed(t *testing.T) {
	finisherCalled := false
	var reported error
	upstream := &trackingCloser{Reader: strings.NewReader(chatFixture)}
	stream := NewBufferedSSEStream(context.Background(), upstream, ChatCodec(), func([]Event, []byte) ([]byte, error) {
		finisherCalled = true
		return nil, nil
	}, BufferOptions{MaxBytes: 200, KeepAliveInterval: -1, OnError: func(err error) { reported = err }})
	out, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if finisherCalled {
		t.Error("finisher must not run when the limit is exceeded")
	}
	if !errors.Is(reported, ErrBufferLimit) {
		t.Errorf("reported = %v", reported)
	}
	if !strings.Contains(string(out), `"code":"response_too_large"`) || !strings.HasSuffix(string(out), "data: [DONE]\n\n") {
		t.Errorf("fail-closed replay = %s", out)
	}
	if strings.Contains(string(out), "Hello") {
		t.Errorf("buffered content leaked: %s", out)
	}
	if !upstream.closed {
		t.Error("upstream not closed")
	}
}

func TestBufferedSSEStream_FinisherErrorFailsClosed(t *testing.T) {
	boom := errors.New("boom")
	var reported error
	stream := NewBufferedSSEStream(context.Background(), io.NopCloser(strings.NewReader(chatFixture)), ChatCodec(), func([]Event, []byte) ([]byte, error) {
		return nil, boom
	}, BufferOptions{OnError: func(err error) { reported = err }})
	out, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(reported, boom) {
		t.Errorf("reported = %v", reported)
	}
	if !strings.Contains(string(out), `"code":"plugin_failure"`) || strings.Contains(string(out), "Hello") {
		t.Errorf("fail-closed replay = %s", out)
	}
}

// blockingReader blocks Read until Close is called.
type blockingReader struct {
	once   sync.Once
	closed chan struct{}
}

func newBlockingReader() *blockingReader {
	return &blockingReader{closed: make(chan struct{})}
}

func (r *blockingReader) Read([]byte) (int, error) {
	<-r.closed
	return 0, io.ErrClosedPipe
}

func (r *blockingReader) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

func TestBufferedSSEStream_ContextCancelStopsDrain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	upstream := newBlockingReader()
	finisherCalled := false
	stream := NewBufferedSSEStream(ctx, upstream, ChatCodec(), func([]Event, []byte) ([]byte, error) {
		finisherCalled = true
		return nil, nil
	}, BufferOptions{KeepAliveInterval: -1})

	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	_, err := stream.Read(make([]byte, 16))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Read err = %v, want context.Canceled", err)
	}
	select {
	case <-upstream.closed:
	case <-time.After(time.Second):
		t.Fatal("upstream was not closed after cancellation")
	}
	if finisherCalled {
		t.Error("finisher must not run for a cancelled request")
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBufferedSSEStream_CloseDuringDrain(t *testing.T) {
	upstream := newBlockingReader()
	stream := NewBufferedSSEStream(context.Background(), upstream, ChatCodec(), nil, BufferOptions{KeepAliveInterval: 5 * time.Millisecond})
	buf := make([]byte, 64)
	if _, err := stream.Read(buf); err != nil {
		t.Fatalf("keep-alive read: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal("second Close must be a no-op")
	}
	select {
	case <-upstream.closed:
	case <-time.After(time.Second):
		t.Fatal("upstream was not closed")
	}
	if _, err := stream.Read(buf); err != ErrStreamClosed {
		t.Errorf("Read after Close err = %v", err)
	}
}

func TestBufferedSSEStream_UpstreamErrorReturnedAfterReplay(t *testing.T) {
	partial := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"
	upstream := io.NopCloser(io.MultiReader(strings.NewReader(partial), &errReader{err: io.ErrUnexpectedEOF}))
	var seen []Event
	stream := NewBufferedSSEStream(context.Background(), upstream, ChatCodec(), func(events []Event, raw []byte) ([]byte, error) {
		seen = events
		return nil, nil
	}, BufferOptions{KeepAliveInterval: -1})
	out, err := io.ReadAll(stream)
	if err != io.ErrUnexpectedEOF {
		t.Errorf("err = %v, want ErrUnexpectedEOF", err)
	}
	if string(out) != partial || len(seen) != 1 {
		t.Errorf("replay = %q, finisher saw %d events", out, len(seen))
	}
}

func TestBufferedSSEStream_ResponsesReplay(t *testing.T) {
	resp := &core.ResponsesResponse{ID: "r1", Model: "m", Status: "completed", Output: []core.ResponsesOutputItem{{
		ID: "msg", Type: "message", Role: "assistant", Status: "completed",
		Content: []core.ResponsesContentItem{{Type: "output_text", Text: "hello"}},
	}}}
	input := SynthesizeResponsesStream(resp)
	stream := NewBufferedSSEStream(context.Background(), io.NopCloser(strings.NewReader(string(input))), ResponsesCodec(), func(events []Event, raw []byte) ([]byte, error) {
		assembled, err := AssembleResponsesResponse(events)
		if err != nil {
			return nil, err
		}
		assembled.Output[0].Content[0].Text = "bye"
		return SynthesizeResponsesStream(assembled), nil
	}, BufferOptions{KeepAliveInterval: -1})
	out, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	got, err := AssembleResponsesResponse(decodeResponsesEvents(t, out))
	if err != nil {
		t.Fatal(err)
	}
	if got.Output[0].Content[0].Text != "bye" || got.ID != "r1" {
		t.Errorf("assembled replay = %+v", got)
	}
}
