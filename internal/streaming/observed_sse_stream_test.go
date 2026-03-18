package streaming

import (
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
