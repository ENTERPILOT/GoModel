package storage

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// BatchFlushThreshold is the number of queued entries that triggers an
// immediate batch write instead of waiting for the flush interval.
const BatchFlushThreshold = 100

// finalFlushTimeout bounds the store flush after the last batch on Close.
const finalFlushTimeout = 10 * time.Second

// FlushCloser is the part of an audit or usage store the queue drives at
// shutdown.
type FlushCloser interface {
	Flush(ctx context.Context) error
	Close() error
}

// BatchQueueConfig configures a BatchQueue.
type BatchQueueConfig[T any] struct {
	// Name labels the queue in log messages, e.g. "audit log" or "usage".
	Name          string
	BufferSize    int
	FlushInterval time.Duration
	// WriteBatch writes one batch and handles its outcome. It runs on the
	// flush goroutine only, so batches never overlap.
	WriteBatch func(batch []T)
	// Store is flushed after the last batch and closed by Close.
	Store FlushCloser
}

// BatchQueue buffers entries in memory and writes them in batches from one
// background goroutine: when BatchFlushThreshold entries are waiting, every
// FlushInterval, and once more on Close. Writers never block; an entry that
// does not fit in the buffer is rejected and the caller decides what a drop
// means.
//
// A writer brackets its enqueue with BeginWrite and EndWrite so Close can wait
// for in-flight writes before it drains the buffer:
//
//	if !q.BeginWrite() {
//		// closed
//		return
//	}
//	defer q.EndWrite()
//	if !q.Enqueue(entry) {
//		// buffer full
//	}
type BatchQueue[T any] struct {
	cfg    BatchQueueConfig[T]
	buffer chan T
	done   chan struct{}
	wg     sync.WaitGroup
	writes sync.WaitGroup // tracks in-flight writes
	closed atomic.Bool
}

// NewBatchQueue starts the flush goroutine and returns the queue.
func NewBatchQueue[T any](cfg BatchQueueConfig[T]) *BatchQueue[T] {
	q := &BatchQueue[T]{
		cfg:    cfg,
		buffer: make(chan T, cfg.BufferSize),
		done:   make(chan struct{}),
	}
	q.wg.Add(1)
	go q.flushLoop()
	return q
}

// BeginWrite registers an in-flight write. It returns false once Close has
// started; the caller must then not call Enqueue or EndWrite.
func (q *BatchQueue[T]) BeginWrite() bool {
	if q.closed.Load() {
		return false
	}
	q.writes.Add(1)
	// Close may have started between the check and Add.
	if q.closed.Load() {
		q.writes.Done()
		return false
	}
	return true
}

// EndWrite ends a write that BeginWrite registered.
func (q *BatchQueue[T]) EndWrite() {
	q.writes.Done()
}

// Enqueue adds entry to the buffer without blocking. It returns false when the
// buffer is full and the entry was not queued.
func (q *BatchQueue[T]) Enqueue(entry T) bool {
	select {
	case q.buffer <- entry:
		return true
	default:
		return false
	}
}

// Close waits for in-flight writes, writes everything still buffered, flushes
// and closes the store, and returns the store's Close error. Calls after the
// first return nil.
func (q *BatchQueue[T]) Close() error {
	if q.closed.Swap(true) {
		return nil
	}
	q.writes.Wait()
	close(q.done)
	q.wg.Wait()
	return q.cfg.Store.Close()
}

// flushLoop collects entries into batches and hands them to WriteBatch.
func (q *BatchQueue[T]) flushLoop() {
	defer q.wg.Done()

	ticker := time.NewTicker(q.cfg.FlushInterval)
	defer ticker.Stop()

	batch := make([]T, 0, BatchFlushThreshold)
	flush := func() {
		if len(batch) > 0 {
			q.cfg.WriteBatch(batch)
			batch = make([]T, 0, BatchFlushThreshold)
		}
	}

	for {
		select {
		case entry := <-q.buffer:
			batch = append(batch, entry)
			if len(batch) >= BatchFlushThreshold {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-q.done:
			// Close set closed and waited for in-flight writes, so nothing is
			// sent after this drain. The buffer is never closed: that would
			// race with a Write that passed BeginWrite's first check.
		drain:
			for {
				select {
				case entry := <-q.buffer:
					batch = append(batch, entry)
				default:
					break drain
				}
			}
			flush()
			q.flushStore()
			return
		}
	}
}

func (q *BatchQueue[T]) flushStore() {
	ctx, cancel := context.WithTimeout(context.Background(), finalFlushTimeout)
	defer cancel()
	if err := q.cfg.Store.Flush(ctx); err != nil {
		slog.Error("failed to flush "+q.cfg.Name+" store", "error", err)
	}
}
