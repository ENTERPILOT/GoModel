package auditlog

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/enterpilot/gomodel/internal/storage"
)

// Logger provides async buffered logging with batch writes.
// It collects log entries in a channel and flushes them to storage
// either when the buffer is full or at regular intervals.
type Logger struct {
	store         LogStore
	config        Config
	queue         *storage.BatchQueue[*LogEntry]
	liveMu        sync.RWMutex
	livePublisher LiveEventPublisher
	// writerID is stamped on every entry the logger flushes; see
	// LogEntry.Stamp.
	writerID string
}

// NewLogger creates a new async buffered Logger.
// The logger starts a background goroutine for flushing entries.
func NewLogger(store LogStore, cfg Config) *Logger {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 1000
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 5 * time.Second
	}

	l := &Logger{
		store:    store,
		config:   cfg,
		writerID: storage.WriterID(),
	}
	l.queue = storage.NewBatchQueue(storage.BatchQueueConfig[*LogEntry]{
		Name:          "audit log",
		BufferSize:    cfg.BufferSize,
		FlushInterval: cfg.FlushInterval,
		WriteBatch:    l.flushBatch,
		Store:         store,
	})

	return l
}

// Write queues a log entry for async writing.
// This method is non-blocking. If the buffer is full or the logger is closed,
// the entry is dropped and a warning is logged.
func (l *Logger) Write(entry *LogEntry) {
	if entry == nil {
		return
	}

	// Hold the queue open so Close waits for this entry before draining.
	if !l.queue.BeginWrite() {
		return
	}
	defer l.queue.EndWrite()

	l.PublishLiveEvent(LiveEventAuditCompleted, entry)
	if !l.queue.Enqueue(entry) {
		l.PublishLiveEvent(LiveEventAuditRemoved, entry)
		// Buffer full - drop entry and log warning
		requestID := entry.RequestID
		if requestID == "" {
			requestID = "unknown"
		}
		slog.Warn("audit log buffer full, dropping entry",
			"request_id", requestID,
			"requested_model", entry.RequestedModel,
		)
	}
}

// SetLivePublisher attaches the optional realtime dashboard publisher.
func (l *Logger) SetLivePublisher(p LiveEventPublisher) {
	if l == nil {
		return
	}
	l.liveMu.Lock()
	defer l.liveMu.Unlock()
	l.livePublisher = p
}

// PublishLiveEvent publishes a compact lifecycle preview when live logs are enabled.
func (l *Logger) PublishLiveEvent(eventType string, entry *LogEntry) {
	if l == nil || entry == nil {
		return
	}
	l.liveMu.RLock()
	publisher := l.livePublisher
	l.liveMu.RUnlock()
	if publisher == nil {
		return
	}
	publisher.PublishAuditEvent(eventType, entry)
}

// HasLiveSubscribers reports whether the attached live publisher currently has
// connected dashboard subscribers. Used to skip building live body previews
// that nobody would receive; a publisher that cannot tell counts as subscribed.
func (l *Logger) HasLiveSubscribers() bool {
	if l == nil {
		return false
	}
	l.liveMu.RLock()
	publisher := l.livePublisher
	l.liveMu.RUnlock()
	if publisher == nil {
		return false
	}
	if reporter, ok := publisher.(LiveSubscriberReporter); ok {
		return reporter.HasLiveSubscribers()
	}
	return true
}

// Config returns the logger configuration
func (l *Logger) Config() Config {
	return l.config
}

// Close stops the logger and flushes remaining entries.
// This should be called during graceful shutdown.
// Close is idempotent - calling it multiple times is safe.
func (l *Logger) Close() error {
	return l.queue.Close()
}

// flushBatch writes a batch of entries to the store.
func (l *Logger) flushBatch(batch []*LogEntry) {
	if len(batch) == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The batch is one write, so all of it carries one stamp. Other replicas
	// find it by that stamp to show it in their live log view.
	stamp := &storage.WriteStamp{WriterID: l.writerID, WrittenAt: time.Now().UnixMicro()}
	for _, entry := range batch {
		entry.Stamp = stamp
	}

	if err := l.store.WriteBatch(ctx, batch); err != nil {
		slog.Error("failed to write audit log batch",
			"error", err,
			"count", len(batch),
		)
		for _, entry := range batch {
			l.PublishLiveEvent(LiveEventAuditFailed, entry)
		}
		return
	}

	for _, entry := range batch {
		l.PublishLiveEvent(LiveEventAuditFlushed, entry)
	}
}

// NoopLogger is a logger that does nothing (used when logging is disabled)
type NoopLogger struct{}

// Write does nothing
func (l *NoopLogger) Write(_ *LogEntry) {}

// Config returns an empty config
func (l *NoopLogger) Config() Config {
	return Config{Enabled: false}
}

// Close does nothing
func (l *NoopLogger) Close() error {
	return nil
}

// LoggerInterface defines the interface for loggers (both real and noop)
type LoggerInterface interface {
	Write(entry *LogEntry)
	Config() Config
	Close() error
}
