package auditlog

import (
	"testing"
	"time"

	"github.com/goccy/go-json"
	"github.com/stretchr/testify/require"
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
	require.NotNil(t, observer)

	// The first content chunk publishes immediately.
	observer.OnJSONEvent(chatContentChunk("Hel"))
	require.Len(t, logger.events, 1)
	require.Equal(t, LiveEventAuditStream, logger.events[0].eventType)
	require.Contains(t, logger.events[0].responseBody, `"Hel"`)

	// Chunks inside the throttle window accumulate without publishing.
	observer.OnJSONEvent(chatContentChunk("lo"))
	require.Len(t, logger.events, 1)

	// Once the window elapses, the next chunk publishes the accumulated body.
	observer.lastPreviewAt = time.Now().Add(-livePreviewInterval)
	observer.OnJSONEvent(chatContentChunk(" world"))
	require.Len(t, logger.events, 2)
	require.Contains(t, logger.events[1].responseBody, `"Hello world"`)

	// Closing writes the final entry instead of another preview.
	observer.OnStreamClose()
	require.Len(t, logger.events, 2)
	require.Equal(t, 1, logger.writes)
}

func TestStreamLogObserverSkipsPreviewsWithoutNewContent(t *testing.T) {
	logger := &streamPreviewLogger{cfg: Config{Enabled: true, LogBodies: true}, subscribed: true}
	entry := &LogEntry{ID: "audit-1", Timestamp: time.Now(), Data: &LogData{}}
	observer := NewStreamLogObserver(logger, entry, "/v1/chat/completions")

	// Metadata-only events (usage, finish_reason) never publish previews.
	observer.OnJSONEvent(map[string]any{"usage": map[string]any{"total_tokens": float64(5)}})
	require.Empty(t, logger.events)

	observer.OnJSONEvent(chatContentChunk("Hi"))
	observer.lastPreviewAt = time.Now().Add(-livePreviewInterval)
	observer.OnJSONEvent(map[string]any{"usage": map[string]any{"total_tokens": float64(7)}})
	require.Len(t, logger.events, 1)
}

func TestStreamLogObserverSkipsPreviewsWithoutSubscribers(t *testing.T) {
	logger := &streamPreviewLogger{cfg: Config{Enabled: true, LogBodies: true}}
	entry := &LogEntry{ID: "audit-1", Timestamp: time.Now(), Data: &LogData{}}
	observer := NewStreamLogObserver(logger, entry, "/v1/chat/completions")

	observer.OnJSONEvent(chatContentChunk("Hi"))
	require.Empty(t, logger.events)

	// A subscriber connecting mid-stream catches up on the next tick because
	// every preview carries the full accumulated body.
	logger.subscribed = true
	observer.lastPreviewAt = time.Now().Add(-livePreviewInterval)
	observer.OnJSONEvent(chatContentChunk(" there"))
	require.Len(t, logger.events, 1)
	require.Contains(t, logger.events[0].responseBody, `"Hi there"`)
}

func TestStreamLogObserverPublishesResponsesAPIPreviews(t *testing.T) {
	logger := &streamPreviewLogger{cfg: Config{Enabled: true, LogBodies: true}, subscribed: true}
	entry := &LogEntry{ID: "audit-1", Timestamp: time.Now(), Data: &LogData{}}
	observer := NewStreamLogObserver(logger, entry, "/v1/responses")

	observer.OnJSONEvent(map[string]any{
		"type":  "response.output_text.delta",
		"delta": "Reasoning...",
	})
	require.Len(t, logger.events, 1)
	require.Contains(t, logger.events[0].responseBody, `"Reasoning..."`)
}
