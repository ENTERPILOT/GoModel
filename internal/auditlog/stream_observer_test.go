package auditlog

import (
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-json"
)

type streamPreviewEvent struct {
	eventType    string
	responseBody string
}

// streamPreviewLogger captures live preview events published by the stream
// observer, snapshotting the response body as JSON at publish time.
type streamPreviewLogger struct {
	cfg        Config
	subscribed bool
	events     []streamPreviewEvent
	writes     int
}

func (l *streamPreviewLogger) Write(_ *LogEntry) { l.writes++ }
func (l *streamPreviewLogger) Config() Config    { return l.cfg }
func (l *streamPreviewLogger) Close() error      { return nil }

func (l *streamPreviewLogger) PublishLiveEvent(eventType string, entry *LogEntry) {
	body := ""
	if entry != nil && entry.Data != nil && entry.Data.ResponseBody != nil {
		raw, err := json.Marshal(entry.Data.ResponseBody)
		if err == nil {
			body = string(raw)
		}
	}
	l.events = append(l.events, streamPreviewEvent{eventType: eventType, responseBody: body})
}

func (l *streamPreviewLogger) HasLiveSubscribers() bool { return l.subscribed }

func chatContentChunk(content string) map[string]any {
	return map[string]any{
		"id": "chatcmpl-1",
		"choices": []any{map[string]any{
			"index": float64(0),
			"delta": map[string]any{"content": content},
		}},
	}
}

func TestStreamLogObserverPublishesThrottledPartialPreviews(t *testing.T) {
	logger := &streamPreviewLogger{cfg: Config{Enabled: true, LogBodies: true}, subscribed: true}
	entry := &LogEntry{ID: "audit-1", RequestID: "req-1", Timestamp: time.Now(), Data: &LogData{}}
	observer := NewStreamLogObserver(logger, entry, "/v1/chat/completions")
	if observer == nil {
		t.Fatal("NewStreamLogObserver returned nil")
	}

	// The first content chunk publishes immediately.
	observer.OnJSONEvent(chatContentChunk("Hel"))
	if len(logger.events) != 1 {
		t.Fatalf("events after first chunk = %d, want 1", len(logger.events))
	}
	if logger.events[0].eventType != LiveEventAuditStream {
		t.Fatalf("event type = %q, want %q", logger.events[0].eventType, LiveEventAuditStream)
	}
	if !strings.Contains(logger.events[0].responseBody, `"Hel"`) {
		t.Fatalf("preview body = %s, want partial content", logger.events[0].responseBody)
	}

	// Chunks inside the throttle window accumulate without publishing.
	observer.OnJSONEvent(chatContentChunk("lo"))
	if len(logger.events) != 1 {
		t.Fatalf("events inside throttle window = %d, want 1", len(logger.events))
	}

	// Once the window elapses, the next chunk publishes the accumulated body.
	observer.lastPreviewAt = time.Now().Add(-livePreviewInterval)
	observer.OnJSONEvent(chatContentChunk(" world"))
	if len(logger.events) != 2 {
		t.Fatalf("events after window elapsed = %d, want 2", len(logger.events))
	}
	if !strings.Contains(logger.events[1].responseBody, `"Hello world"`) {
		t.Fatalf("preview body = %s, want accumulated content", logger.events[1].responseBody)
	}

	// Closing writes the final entry instead of another preview.
	observer.OnStreamClose()
	if len(logger.events) != 2 {
		t.Fatalf("events after close = %d, want 2", len(logger.events))
	}
	if logger.writes != 1 {
		t.Fatalf("writes after close = %d, want 1", logger.writes)
	}
}

func TestStreamLogObserverSkipsPreviewsWithoutNewContent(t *testing.T) {
	logger := &streamPreviewLogger{cfg: Config{Enabled: true, LogBodies: true}, subscribed: true}
	entry := &LogEntry{ID: "audit-1", Timestamp: time.Now(), Data: &LogData{}}
	observer := NewStreamLogObserver(logger, entry, "/v1/chat/completions")

	// Metadata-only events (usage, finish_reason) never publish previews.
	observer.OnJSONEvent(map[string]any{"usage": map[string]any{"total_tokens": float64(5)}})
	if len(logger.events) != 0 {
		t.Fatalf("events after metadata chunk = %d, want 0", len(logger.events))
	}

	observer.OnJSONEvent(chatContentChunk("Hi"))
	observer.lastPreviewAt = time.Now().Add(-livePreviewInterval)
	observer.OnJSONEvent(map[string]any{"usage": map[string]any{"total_tokens": float64(7)}})
	if len(logger.events) != 1 {
		t.Fatalf("events after trailing metadata chunk = %d, want 1", len(logger.events))
	}
}

func TestStreamLogObserverSkipsPreviewsWithoutSubscribers(t *testing.T) {
	logger := &streamPreviewLogger{cfg: Config{Enabled: true, LogBodies: true}}
	entry := &LogEntry{ID: "audit-1", Timestamp: time.Now(), Data: &LogData{}}
	observer := NewStreamLogObserver(logger, entry, "/v1/chat/completions")

	observer.OnJSONEvent(chatContentChunk("Hi"))
	if len(logger.events) != 0 {
		t.Fatalf("events without subscribers = %d, want 0", len(logger.events))
	}

	// A subscriber connecting mid-stream catches up on the next tick because
	// every preview carries the full accumulated body.
	logger.subscribed = true
	observer.lastPreviewAt = time.Now().Add(-livePreviewInterval)
	observer.OnJSONEvent(chatContentChunk(" there"))
	if len(logger.events) != 1 {
		t.Fatalf("events after subscriber connected = %d, want 1", len(logger.events))
	}
	if !strings.Contains(logger.events[0].responseBody, `"Hi there"`) {
		t.Fatalf("preview body = %s, want full accumulated content", logger.events[0].responseBody)
	}
}

func TestStreamLogObserverPublishesResponsesAPIPreviews(t *testing.T) {
	logger := &streamPreviewLogger{cfg: Config{Enabled: true, LogBodies: true}, subscribed: true}
	entry := &LogEntry{ID: "audit-1", Timestamp: time.Now(), Data: &LogData{}}
	observer := NewStreamLogObserver(logger, entry, "/v1/responses")

	observer.OnJSONEvent(map[string]any{
		"type":  "response.output_text.delta",
		"delta": "Reasoning...",
	})
	if len(logger.events) != 1 {
		t.Fatalf("events after responses delta = %d, want 1", len(logger.events))
	}
	if !strings.Contains(logger.events[0].responseBody, `"Reasoning..."`) {
		t.Fatalf("preview body = %s, want output text", logger.events[0].responseBody)
	}
}
