package usage

import (
	"context"
	"io"
	"log/slog"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/storage"
)

// discardStore counts the entries it is asked to write and keeps none.
type discardStore struct{ written *atomic.Int64 }

func (s discardStore) WriteBatch(_ context.Context, entries []*UsageEntry) error {
	s.written.Add(int64(len(entries)))
	return nil
}
func (discardStore) Flush(context.Context) error { return nil }
func (discardStore) Close() error                { return nil }

// benchWriteChunk is how many writes BenchmarkLoggerWrite times before it lets
// the flush loop catch up; well below the buffer size.
const benchWriteChunk = 1 << 14

// BenchmarkLoggerWrite measures the per-request cost of queueing a usage
// entry. The timer pauses every benchWriteChunk writes until the flush loop
// has caught up, so the buffer never fills; drops/op must read 0.
func BenchmarkLoggerWrite(b *testing.B) {
	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))

	var written atomic.Int64
	logger := NewLogger(discardStore{written: &written}, Config{BufferSize: 1 << 16, FlushInterval: time.Hour})
	entry := &UsageEntry{ID: "bench", RequestID: "req"}

	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		logger.Write(entry)
		if (i+1)%benchWriteChunk == 0 {
			b.StopTimer()
			for written.Load() < int64(i+1-storage.BatchFlushThreshold) {
				runtime.Gosched()
			}
			b.StartTimer()
		}
	}
	b.StopTimer()
	if err := logger.Close(); err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(int64(b.N)-written.Load())/float64(b.N), "drops/op")
}
