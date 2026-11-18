package usage

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/enterpilot/gomodel/internal/storage"
)

// Logger provides async buffered logging with batch writes.
// It collects usage entries in a channel and flushes them to storage
// either when the buffer is full or at regular intervals.
type Logger struct {
	store         UsageStore
	config        Config
	queue         *storage.BatchQueue[*UsageEntry]
	liveMu        sync.RWMutex
	livePublisher LiveEventPublisher
	// writerID is stamped on every entry the logger flushes; see
	// UsageEntry.Stamp.
	writerID string
}

// NewLogger creates a new async buffered Logger.
// The logger starts a background goroutine for flushing entries.
func NewLogger(store UsageStore, cfg Config) *Logger {
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
	l.queue = storage.NewBatchQueue(storage.BatchQueueConfig[*UsageEntry]{
		Name:          "usage",
		BufferSize:    cfg.BufferSize,
		FlushInterval: cfg.FlushInterval,
		WriteBatch:    l.flushBatch,
		Store:         store,
	})

	return l
}

// usageEntriesDropped counts usage entries the logger could not store. Spend
// they carry is missing from usage views, budgets and invoices.
var usageEntriesDropped = func() *prometheus.CounterVec {
	c := promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gomodel_usage_entries_dropped_total",
			Help: "Usage entries the logger could not store, by reason: buffer_full (USAGE_BUFFER_SIZE reached), write_failed (the batch write returned an error) or logger_closed (recorded after the logger shut down, by a request or session still running at shutdown)",
		},
		[]string{"reason"},
	)
	// Export every reason from the start, so an increase() alert has a
	// baseline before the first drop.
	for _, reason := range []string{dropBufferFull, dropWriteFailed, dropLoggerClosed} {
		c.WithLabelValues(reason)
	}
	return c
}()

// Values of the reason label of usageEntriesDropped.
const (
	dropBufferFull   = "buffer_full"
	dropWriteFailed  = "write_failed"
	dropLoggerClosed = "logger_closed"
)

// dropEntry counts and logs one entry Write could not queue. Operators alert
// on the message texts, so they stay as they are.
func dropEntry(reason, message string, entry *UsageEntry) {
	usageEntriesDropped.WithLabelValues(reason).Inc()
	slog.Warn(message, "request_id", cmp.Or(entry.RequestID, "unknown"), "model", entry.Model)
}

// Write queues a usage entry for async writing.
// This method is non-blocking. If the buffer is full or the logger is closed,
// the entry is dropped and a warning is logged.
func (l *Logger) Write(entry *UsageEntry) {
	if entry == nil {
		return
	}

	// Hold the queue open so Close waits for this entry before draining.
	if !l.queue.BeginWrite() {
		dropEntry(dropLoggerClosed, "usage logger closed, dropping entry", entry)
		return
	}
	defer l.queue.EndWrite()

	l.publishLiveEvent(LiveEventUsageCompleted, entry)
	if !l.queue.Enqueue(entry) {
		l.publishLiveEvent(LiveEventUsageFailed, entry)
		dropEntry(dropBufferFull, "usage log buffer full, dropping entry", entry)
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

func (l *Logger) publishLiveEvent(eventType string, entry *UsageEntry) {
	if l == nil || entry == nil {
		return
	}
	l.liveMu.RLock()
	publisher := l.livePublisher
	l.liveMu.RUnlock()
	if publisher == nil {
		return
	}
	publisher.PublishUsageEvent(eventType, entry)
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
func (l *Logger) flushBatch(batch []*UsageEntry) {
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
		dropped := len(batch)
		if partial, ok := errors.AsType[*PartialWriteError](err); ok {
			dropped = partial.FailedCount
		}
		usageEntriesDropped.WithLabelValues(dropWriteFailed).Add(float64(dropped))
		slog.Error("failed to write usage batch",
			"error", err,
			"count", len(batch),
		)
		for _, entry := range batch {
			l.publishLiveEvent(LiveEventUsageFailed, entry)
		}
		return
	}

	for _, entry := range batch {
		l.publishLiveEvent(LiveEventUsageFlushed, entry)
	}
}

// NoopLogger is a logger that does nothing (used when usage tracking is disabled)
type NoopLogger struct {
	config     Config
	configured bool
}

// NewNoopLogger creates a disabled logger that still carries policy config such as
// whether streaming requests should ask providers to include usage.
func NewNoopLogger(cfg Config) *NoopLogger {
	cfg.Enabled = false
	return &NoopLogger{config: cfg, configured: true}
}

// Write does nothing
func (l *NoopLogger) Write(_ *UsageEntry) {}

// Config returns the effective config with logging disabled.
func (l *NoopLogger) Config() Config {
	cfg := l.config
	if !l.configured {
		cfg = DefaultConfig()
	}
	cfg.Enabled = false
	return cfg
}

// Close does nothing
func (l *NoopLogger) Close() error {
	return nil
}

// LoggerInterface defines the interface for loggers (both real and noop)
type LoggerInterface interface {
	Write(entry *UsageEntry)
	Config() Config
	Close() error
}
