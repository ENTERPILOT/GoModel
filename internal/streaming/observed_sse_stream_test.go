package streaming

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

type trackingObserver struct {
	eventCount int
	lastID     string
	closed     bool
}

func (o *trackingObserver) OnJSONEvent(payload map[string]interface{}) {
	o.eventCount++
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

func TestObservedSSEStream_CapsCombinedPendingData(t *testing.T) {
	s := &ObservedSSEStream{
		pending: bytes.Repeat([]byte("a"), maxPendingEventBytes),
	}
	data := bytes.Repeat([]byte("b"), maxPendingEventBytes+1024)

	s.processChunk(data)

	if got := len(s.pending); got != maxPendingEventBytes {
		t.Fatalf("pending length = %d, want %d", got, maxPendingEventBytes)
	}
	if !bytes.Equal(s.pending, data[len(data)-maxPendingEventBytes:]) {
		t.Fatal("pending bytes do not match the capped suffix of the latest chunk")
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
