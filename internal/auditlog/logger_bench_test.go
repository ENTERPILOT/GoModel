package auditlog

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

type discardStore struct{}

func (discardStore) WriteBatch(context.Context, []*LogEntry) error { return nil }
func (discardStore) Flush(context.Context) error                   { return nil }
func (discardStore) Close() error                                  { return nil }

// BenchmarkLoggerWrite measures the per-request cost of queueing an audit
// entry. The buffer is large so the flush loop keeps up and nothing drops.
func BenchmarkLoggerWrite(b *testing.B) {
	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))

	logger := NewLogger(discardStore{}, Config{BufferSize: 1 << 16, FlushInterval: time.Hour})
	defer logger.Close()
	entry := &LogEntry{ID: "bench", RequestID: "req"}

	b.ReportAllocs()
	for b.Loop() {
		logger.Write(entry)
	}
}
