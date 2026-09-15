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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBufferedSSEStream_ReplayUnchanged(t *testing.T) {
	var got []Event
	finish := func(events []Event, raw []byte) ([]byte, error) {
		got = events
		assert.Equal(t, chatFixture, string(raw))

		return nil, nil
	}
	stream := NewBufferedSSEStream(context.Background(), io.NopCloser(strings.NewReader(chatFixture)), ChatCodec(), finish, BufferOptions{})
	out, err := readAllSmall(t, stream)
	require.NoError(t, err)
	assert.Equal(t, chatFixture, string(out))

	want := []EventKind{KindOther, KindTextDelta, KindTextDelta, KindTextDelta, KindFinish, KindUsage, KindDone}
	assert.Equal(t, kindStrings(want), kindStrings(kinds(got)))

	for i, ev := range got {
		assert.Equal(t, i, ev.Seq)
	}
	err = stream.Close()
	require.NoError(t, err)
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
	require.NoError(t, err)
	assert.NotContains(t, string(out), "555-")
	assert.Contains(t, string(out), `"content":"replaced"`, "synthesized replay not served:\n%s", out)

	resp, err := AssembleChatResponse(decodeChatEvents(t, out))
	require.NoError(t, err)
	assert.Equal(t, "replaced", resp.Choices[0].Message.Content)
	assert.Equal(t, 3, resp.Usage.TotalTokens)
	assert.Equal(t, "c1", resp.ID, "assembled replay = %+v", resp)
}

func TestBufferedSSEStream_KeepAliveWhileDraining(t *testing.T) {
	pr, pw := io.Pipe()
	stream := NewBufferedSSEStream(context.Background(), pr, ChatCodec(), nil, BufferOptions{KeepAliveInterval: 5 * time.Millisecond, KeepAliveComment: ": hold"})

	buf := make([]byte, 64)
	n, err := stream.Read(buf)
	require.NoError(t, err)
	require.Equal(t, ": hold\n\n", string(buf[:n]), "first Read: want keep-alive comment")

	go func() {
		_, _ = pw.Write([]byte(chatFixture))
		_ = pw.Close()
	}()
	out, err := io.ReadAll(stream)
	require.NoError(t, err)

	trimmed := strings.TrimPrefix(string(out), ": hold\n\n")
	for strings.HasPrefix(trimmed, ": hold\n\n") {
		trimmed = strings.TrimPrefix(trimmed, ": hold\n\n")
	}
	assert.Equal(t, chatFixture, trimmed)
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
	require.NoError(t, err)
	assert.False(t, finisherCalled)
	assert.ErrorIs(t, reported, ErrBufferLimit)
	assert.Contains(t, string(out), `"code":"response_too_large"`)
	assert.True(t, strings.HasSuffix(string(out), "data: [DONE]\n\n"), "fail-closed replay = %s", out)
	assert.NotContains(t, string(out), "Hello", "buffered content leaked: %s", out)
	assert.True(t, upstream.closed)
}

func TestBufferedSSEStream_FinisherErrorFailsClosed(t *testing.T) {
	boom := errors.New("boom")
	var reported error
	stream := NewBufferedSSEStream(context.Background(), io.NopCloser(strings.NewReader(chatFixture)), ChatCodec(), func([]Event, []byte) ([]byte, error) {
		return nil, boom
	}, BufferOptions{OnError: func(err error) { reported = err }})
	out, err := io.ReadAll(stream)
	require.NoError(t, err)
	assert.ErrorIs(t, reported, boom)
	assert.Contains(t, string(out), `"code":"plugin_failure"`)
	assert.NotContains(t, string(out), "Hello", "fail-closed replay = %s", out)
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
	require.ErrorIs(t, err, context.Canceled)

	select {
	case <-upstream.closed:
	case <-time.After(time.Second):
		t.Fatal("upstream was not closed after cancellation")
	}
	assert.False(t, finisherCalled)
	err = stream.Close()
	require.NoError(t, err)
}

func TestBufferedSSEStream_CloseDuringDrain(t *testing.T) {
	upstream := newBlockingReader()
	stream := NewBufferedSSEStream(context.Background(), upstream, ChatCodec(), nil, BufferOptions{KeepAliveInterval: 5 * time.Millisecond})
	buf := make([]byte, 64)
	_, err := stream.Read(buf)
	require.NoError(t, err)
	err = stream.Close()
	require.NoError(t, err)
	require.NoError(t, stream.Close())

	select {
	case <-upstream.closed:
	case <-time.After(time.Second):
		t.Fatal("upstream was not closed")
	}
	_, err = stream.Read(buf)
	assert.Equal(t, ErrStreamClosed, err)
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
	assert.Equal(t, io.ErrUnexpectedEOF, err)
	assert.Equal(t, partial, string(out))
	assert.Len(t, seen, 1)
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
	require.NoError(t, err)

	got, err := AssembleResponsesResponse(decodeResponsesEvents(t, out))
	require.NoError(t, err)
	assert.Equal(t, "bye", got.Output[0].Content[0].Text)
	assert.Equal(t, "r1", got.ID, "assembled replay = %+v", got)
}

// Close may run on another goroutine while the first Read, which starts
// the drain, is in progress; it must see the ticker and context hook it has
// to stop. Meaningful under -race.
func TestBufferedSSEStream_CloseRacesFirstRead(t *testing.T) {
	upstream := newBlockingReader()
	stream := NewBufferedSSEStream(context.Background(), upstream, ChatCodec(), nil, BufferOptions{KeepAliveInterval: time.Hour})
	read := make(chan struct{})
	go func() {
		defer close(read)
		_, _ = stream.Read(make([]byte, 64))
	}()
	time.Sleep(10 * time.Millisecond)
	// let Read pass its closed check and start the drain
	err := stream.Close()
	require.NoError(t, err)

	<-read
}
