package storage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingQueueStore records the batches, store flushes and closes a queue
// drives, in order.
type recordingQueueStore struct {
	mu       sync.Mutex
	batches  [][]int
	calls    []string
	closeErr error
}

func (s *recordingQueueStore) writeBatch(batch []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches = append(s.batches, append([]int(nil), batch...))
	s.calls = append(s.calls, "write")
}

func (s *recordingQueueStore) Flush(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "flush")
	return nil
}

func (s *recordingQueueStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "close")
	return s.closeErr
}

func (s *recordingQueueStore) snapshot() (batches [][]int, calls []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]int(nil), s.batches...), append([]string(nil), s.calls...)
}

func newTestQueue(store *recordingQueueStore, bufferSize int, interval time.Duration) *BatchQueue[int] {
	return NewBatchQueue(BatchQueueConfig[int]{
		Name:          "test",
		BufferSize:    bufferSize,
		FlushInterval: interval,
		WriteBatch:    store.writeBatch,
		Store:         store,
	})
}

func write(q *BatchQueue[int], entry int) bool {
	if !q.BeginWrite() {
		return false
	}
	defer q.EndWrite()
	return q.Enqueue(entry)
}

func TestBatchQueueFlushesAtThreshold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &recordingQueueStore{}
		q := newTestQueue(store, 1000, time.Hour)
		defer q.Close()

		for i := range BatchFlushThreshold + 1 {
			require.True(t, write(q, i))
		}
		synctest.Wait()

		batches, _ := store.snapshot()
		require.Len(t, batches, 1)
		assert.Len(t, batches[0], BatchFlushThreshold)
	})
}

func TestBatchQueueFlushesOnInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &recordingQueueStore{}
		q := newTestQueue(store, 1000, time.Second)
		defer q.Close()

		for i := range 5 {
			require.True(t, write(q, i))
		}
		synctest.Wait()
		batches, _ := store.snapshot()
		require.Empty(t, batches)

		time.Sleep(time.Second)
		synctest.Wait()

		batches, _ = store.snapshot()
		require.Len(t, batches, 1)
		assert.Equal(t, []int{0, 1, 2, 3, 4}, batches[0])
	})
}

func TestBatchQueueCloseDrainsThenFlushesAndClosesStore(t *testing.T) {
	store := &recordingQueueStore{closeErr: errors.New("close failed")}
	q := newTestQueue(store, 1000, time.Hour)
	for i := range 3 {
		require.True(t, write(q, i))
	}

	require.EqualError(t, q.Close(), "close failed")

	batches, calls := store.snapshot()
	assert.Equal(t, [][]int{{0, 1, 2}}, batches)
	assert.Equal(t, []string{"write", "flush", "close"}, calls)
}

func TestBatchQueueCloseIsIdempotent(t *testing.T) {
	store := &recordingQueueStore{}
	q := newTestQueue(store, 10, time.Hour)

	require.NoError(t, q.Close())
	require.NoError(t, q.Close())

	_, calls := store.snapshot()
	assert.Equal(t, []string{"flush", "close"}, calls)
}

func TestBatchQueueRejectsWritesAfterClose(t *testing.T) {
	store := &recordingQueueStore{}
	q := newTestQueue(store, 10, time.Hour)
	require.NoError(t, q.Close())

	assert.False(t, q.BeginWrite())
	batches, _ := store.snapshot()
	assert.Empty(t, batches)
}

func TestBatchQueueRejectsEntriesWhenBufferIsFull(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		store := &recordingQueueStore{}
		q := NewBatchQueue(BatchQueueConfig[int]{
			Name:          "test",
			BufferSize:    1,
			FlushInterval: time.Hour,
			WriteBatch: func(batch []int) {
				<-release
				store.writeBatch(batch)
			},
			Store: store,
		})

		// Fill one batch so the flush goroutine blocks in WriteBatch.
		for i := range BatchFlushThreshold {
			require.True(t, write(q, i))
			synctest.Wait()
		}

		assert.True(t, write(q, 100), "one slot left in the buffer")
		assert.False(t, write(q, 101), "buffer full")

		close(release)
		require.NoError(t, q.Close())
		batches, _ := store.snapshot()
		require.Len(t, batches, 2)
		assert.Equal(t, []int{100}, batches[1])
	})
}

// BenchmarkBatchQueueWrite measures the per-entry cost of a guarded enqueue.
func BenchmarkBatchQueueWrite(b *testing.B) {
	q := NewBatchQueue(BatchQueueConfig[*int]{
		Name:          "bench",
		BufferSize:    1 << 16,
		FlushInterval: time.Hour,
		WriteBatch:    func([]*int) {},
		Store:         &recordingQueueStore{},
	})
	defer q.Close()
	entry := new(int)

	b.ReportAllocs()
	for b.Loop() {
		if q.BeginWrite() {
			q.Enqueue(entry)
			q.EndWrite()
		}
	}
}
