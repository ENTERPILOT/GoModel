package streaming

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// BenchmarkNextEventBoundary walks an LF-only body event by event, the way
// cache validation and replays consume a whole stored stream, so the cost per
// op must grow linearly with the number of events.
func BenchmarkNextEventBoundary(b *testing.B) {
	event := `data: {"type":"response.output_text.delta","delta":"` + strings.Repeat("x", 120) + `"}` + "\n\n"
	for _, events := range []int{100, 1000, 5000} {
		body := bytes.Repeat([]byte(event), events)
		b.Run(fmt.Sprintf("events=%d", events), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			for b.Loop() {
				data := body
				seen := 0
				for {
					idx, sepLen := NextEventBoundary(data)
					if idx == -1 {
						break
					}
					seen++
					data = data[idx+sepLen:]
				}
				if seen != events {
					b.Fatalf("saw %d events, want %d", seen, events)
				}
			}
		})
	}
}

// BenchmarkObservedSSEStreamSplitEvent feeds one large Responses terminal
// event in 4 KiB reads, the shape of a response.completed carrying a long
// answer. The observer declines decoding so only framing cost is measured.
func BenchmarkObservedSSEStreamSplitEvent(b *testing.B) {
	payload := `{"type":"response.completed","response":{"id":"resp_1","output_text":"` + strings.Repeat("y", 200*1024) + `"}}`
	body := []byte("data: " + payload + "\n\n")
	const readSize = 4 * 1024
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		seen := 0
		observer := &filteringObserver{wants: func(raw []byte) bool {
			if len(raw) == len(payload) {
				seen++
			}
			return false
		}}
		s := &ObservedSSEStream{observers: []Observer{observer}, filters: []EventFilter{observer}}
		for start := 0; start < len(body); start += readSize {
			s.processChunk(body[start:min(start+readSize, len(body))])
		}
		if seen != 1 {
			b.Fatalf("framed %d complete events, want 1", seen)
		}
	}
}

// BenchmarkObservedSSEStreamUnwantedEvents measures the framing cost of
// multi-line Responses events that no observer decodes.
func BenchmarkObservedSSEStreamUnwantedEvents(b *testing.B) {
	event := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"
	body := []byte(strings.Repeat(event, 100))
	observer := &filteringObserver{wants: func([]byte) bool { return false }}
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		s := &ObservedSSEStream{observers: []Observer{observer}, filters: []EventFilter{observer}}
		s.processChunk(body)
	}
}
