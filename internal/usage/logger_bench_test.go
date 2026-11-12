package usage

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

type discardStore struct{}

func (discardStore) WriteBatch(context.Context, []*UsageEntry) error { return nil }
func (discardStore) Flush(context.Context) error                     { return nil }
func (discardStore) Close() error                                    { return nil }

// BenchmarkLoggerWrite measures the per-request cost of queueing a usage
// entry. The buffer is large so the flush loop keeps up and nothing drops.
func BenchmarkLoggerWrite(b *testing.B) {
	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))

	logger := NewLogger(discardStore{}, Config{BufferSize: 1 << 16, FlushInterval: time.Hour})
	defer logger.Close()
	entry := &UsageEntry{ID: "bench", RequestID: "req"}

	b.ReportAllocs()
	for b.Loop() {
		logger.Write(entry)
	}
}
