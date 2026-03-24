package streaming

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

type trackingObserver struct {
	eventCount  int
	lastID      string
	lastPayload map[string]any
	closed      bool
}

func (o *trackingObserver) OnJSONEvent(payload map[string]any) {
	o.eventCount++
	o.lastPayload = payload
	if id, _ := payload["id"].(string); id != "" {
		o.lastID = id
	}
}

func (o *trackingObserver) OnStreamClose() {
	o.closed = true
}

func TestObservedSSEStream_PassesThroughAndFansOut(t *testing.T) {
	streamData := `data: {"id":"chatcmpl-1","choices":[{"delta":{"content":"hi"}}]}

data: {"id":"chatcmpl-2","usage":{"total_tokens":3}}

data: [DONE]

`
	first := &trackingObserver{}
	second := &trackingObserver{}
	stream := NewObservedSSEStream(io.NopCloser(strings.NewReader(streamData)), first, second)

	data, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(data) != streamData {
		t.Fatalf("stream passthrough mismatch")
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	for i, observer := range []*trackingObserver{first, second} {
		if observer.eventCount != 2 {
			t.Fatalf("observer %d eventCount = %d, want 2", i, observer.eventCount)
		}
		if observer.lastID != "chatcmpl-2" {
			t.Fatalf("observer %d lastID = %q, want chatcmpl-2", i, observer.lastID)
		}
		if !observer.closed {
			t.Fatalf("observer %d was not closed", i)
		}
	}
}

func TestObservedSSEStream_ParsesFragmentedFinalEventOnClose(t *testing.T) {
	streamData := `data: {"id":"chatcmpl-frag","usage":{"total_tokens":8}}`
	observer := &trackingObserver{}
	stream := NewObservedSSEStream(io.NopCloser(strings.NewReader(streamData)), observer)

	data, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(data) != streamData {
		t.Fatalf("stream passthrough mismatch")
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	if observer.eventCount != 1 {
		t.Fatalf("eventCount = %d, want 1", observer.eventCount)
	}
	if observer.lastID != "chatcmpl-frag" {
		t.Fatalf("lastID = %q, want chatcmpl-frag", observer.lastID)
	}
	if !observer.closed {
		t.Fatal("observer was not closed")
	}
}

func TestObservedSSEStream_ReassemblesMultilineDataEvent(t *testing.T) {
	streamData := "data: {\"id\":\"chatcmpl-multiline\",\n" +
		"data: \"usage\":{\"total_tokens\":3}}\n\n" +
		"data: [DONE]\n\n"
	observer := &trackingObserver{}
	stream := NewObservedSSEStream(io.NopCloser(strings.NewReader(streamData)), observer)

	data, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(data) != streamData {
		t.Fatalf("stream passthrough mismatch")
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	if observer.eventCount != 1 {
		t.Fatalf("eventCount = %d, want 1", observer.eventCount)
	}
	if observer.lastID != "chatcmpl-multiline" {
		t.Fatalf("lastID = %q, want chatcmpl-multiline", observer.lastID)
	}
	usage, ok := observer.lastPayload["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage = %#v, want object", observer.lastPayload["usage"])
	}
	if got := usage["total_tokens"]; got != float64(3) {
		t.Fatalf("usage.total_tokens = %#v, want 3", got)
	}
}

func TestObservedSSEStream_DetectsBoundarySplitAcrossReads(t *testing.T) {
	observer := &trackingObserver{}
	s := &ObservedSSEStream{
		observers: []Observer{observer},
	}

	s.processChunk([]byte("data:{\"id\":\"chatcmpl-1\"}\r\n\r"))
	s.processChunk([]byte("\ndata:{\"id\":\"chatcmpl-2\"}\r\n\r\n"))

	if observer.eventCount != 2 {
		t.Fatalf("eventCount = %d, want 2", observer.eventCount)
	}
	if observer.lastID != "chatcmpl-2" {
		t.Fatalf("lastID = %q, want chatcmpl-2", observer.lastID)
	}
	if len(s.pending) != 0 {
		t.Fatalf("pending length = %d, want 0", len(s.pending))
	}
}

func TestObservedSSEStream_DiscardsOversizedPendingDataWithoutTailCapping(t *testing.T) {
	s := &ObservedSSEStream{
		pending: bytes.Repeat([]byte("a"), maxPendingEventBytes),
	}
	data := bytes.Repeat([]byte("b"), maxPendingEventBytes+1024)

	s.processChunk(data)

	if got := len(s.pending); got != 0 {
		t.Fatalf("pending length = %d, want 0", got)
	}
	if !s.discarding {
		t.Fatal("discarding = false, want true")
	}
}

func TestObservedSSEStream_DropsOversizedBufferedEventAndResumesWithinSameChunk(t *testing.T) {
	observer := &trackingObserver{}
	s := &ObservedSSEStream{
		observers: []Observer{observer},
		pending: append(
			[]byte("data: {\"id\":\"too-big\",\"payload\":\""),
			bytes.Repeat([]byte("a"), maxPendingEventBytes/2)...,
		),
	}
	data := append(
		append(
			append(
				bytes.Repeat([]byte("b"), maxPendingEventBytes/2+1),
				[]byte("\"}\n\ndata: {\"id\":\"fresh\"}\n\n")...,
			),
			bytes.Repeat([]byte("c"), maxPendingEventBytes+1)...,
		),
		[]byte("ignored-trailer")...,
	)

	s.processChunk(data)

	if observer.eventCount != 1 {
		t.Fatalf("eventCount = %d, want 1", observer.eventCount)
	}
	if observer.lastID != "fresh" {
		t.Fatalf("lastID = %q, want fresh", observer.lastID)
	}
}

func TestObservedSSEStream_ResumesAfterDiscardWhenBoundarySplitsAcrossReads(t *testing.T) {
	observer := &trackingObserver{}
	s := &ObservedSSEStream{
		observers: []Observer{observer},
	}

	oversized := append(
		append(
			[]byte("data:{\"id\":\"too-big\",\"payload\":\""),
			bytes.Repeat([]byte("x"), maxPendingEventBytes)...,
		),
		[]byte("\"}\r\n\r")...,
	)

	s.processChunk(oversized)
	s.processChunk([]byte("\ndata:{\"id\":\"fresh\"}\r\n\r\n"))

	if observer.eventCount != 1 {
		t.Fatalf("eventCount = %d, want 1", observer.eventCount)
	}
	if observer.lastID != "fresh" {
		t.Fatalf("lastID = %q, want fresh", observer.lastID)
	}
	if s.discarding {
		t.Fatal("discarding = true, want false")
	}
}

func TestObservedSSEStream_DropsOversizedPendingPrefixBeforeCombining(t *testing.T) {
	observer := &trackingObserver{}
	s := &ObservedSSEStream{
		observers: []Observer{observer},
		pending: append(
			[]byte("data: {\"id\":\"stale\"}\n\n"),
			bytes.Repeat([]byte("x"), maxPendingEventBytes)...,
		),
	}

	s.processChunk([]byte("\n\ndata: {\"id\":\"fresh\"}\n\n"))

	if observer.eventCount != 1 {
		t.Fatalf("eventCount = %d, want 1", observer.eventCount)
	}
	if observer.lastID != "fresh" {
		t.Fatalf("lastID = %q, want fresh", observer.lastID)
	}
	if len(s.pending) != 0 {
		t.Fatalf("pending length = %d, want 0", len(s.pending))
	}
}

func TestObservedSSEStream_HandlesCRLFAndDataWithoutSpace(t *testing.T) {
	streamData := "data:{\"id\":\"chatcmpl-1\"}\r\n\r\ndata: {\"id\":\"chatcmpl-2\"}\r\n\r\ndata:[DONE]\r\n\r\n"
	observer := &trackingObserver{}
	stream := NewObservedSSEStream(io.NopCloser(strings.NewReader(streamData)), observer)

	data, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(data) != streamData {
		t.Fatalf("stream passthrough mismatch")
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	if observer.eventCount != 2 {
		t.Fatalf("eventCount = %d, want 2", observer.eventCount)
	}
	if observer.lastID != "chatcmpl-2" {
		t.Fatalf("lastID = %q, want chatcmpl-2", observer.lastID)
	}
	if !observer.closed {
		t.Fatal("observer was not closed")
	}
}

func TestObservedSSEStream_ParsesCRLFBufferedEventsOnClose(t *testing.T) {
	streamData := "data:{\"id\":\"chatcmpl-1\"}\r\n\r\ndata:{\"id\":\"chatcmpl-2\"}"
	observer := &trackingObserver{}
	stream := NewObservedSSEStream(io.NopCloser(strings.NewReader(streamData)), observer)

	data, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(data) != streamData {
		t.Fatalf("stream passthrough mismatch")
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	if observer.eventCount != 2 {
		t.Fatalf("eventCount = %d, want 2", observer.eventCount)
	}
	if observer.lastID != "chatcmpl-2" {
		t.Fatalf("lastID = %q, want chatcmpl-2", observer.lastID)
	}
	if !observer.closed {
		t.Fatal("observer was not closed")
	}
}

func TestJoinedSuffix(t *testing.T) {
	tests := []struct {
		name   string
		prefix []byte
		data   []byte
		n      int
		want   []byte
	}{
		{
			name: "returns nil for non-positive length",
			data: []byte("abc"),
			n:    0,
			want: nil,
		},
		{
			name: "returns suffix from data when data is long enough",
			data: []byte("abcdef"),
			n:    3,
			want: []byte("def"),
		},
		{
			name:   "combines prefix tail and data",
			prefix: []byte("abcd"),
			data:   []byte("ef"),
			n:      4,
			want:   []byte("cdef"),
		},
		{
			name:   "uses available prefix bytes only",
			prefix: []byte("ab"),
			data:   []byte("cd"),
			n:      5,
			want:   []byte("abcd"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := joinedSuffix(tt.prefix, tt.data, tt.n)
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("joinedSuffix() = %q, want %q", got, tt.want)
			}
		})
	}
}
