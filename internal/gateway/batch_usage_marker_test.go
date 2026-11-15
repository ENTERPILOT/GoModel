package gateway

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	batchstore "github.com/enterpilot/gomodel/internal/batch"
)

// hangingCompleteStore never answers CompleteUsage, as a store whose batch
// row is locked or whose connection pool is exhausted.
type hangingCompleteStore struct {
	*batchstore.MemoryStore
	live    bool
	bounded bool
}

func (s *hangingCompleteStore) CompleteUsage(ctx context.Context, _ string, _ time.Time) error {
	s.live = ctx.Err() == nil
	_, s.bounded = ctx.Deadline()
	<-ctx.Done()
	return ctx.Err()
}

// unwritableStore reads batches but takes no write, as a store that fails
// while the results are fetched.
type unwritableStore struct {
	failingClaimStore
}

func (s unwritableStore) Update(context.Context, *batchstore.StoredBatch) error {
	return errors.New("store unavailable")
}

// The entries are with the logger by the time the marker is saved, so a store
// that does not answer delays the results by a bound and no longer.
func TestBatchResultsReturnWhenMarkingUsageHangs(t *testing.T) {
	tests := []struct {
		name       string
		callerGone bool
	}{
		{name: "caller waits"},
		{name: "caller went away while the entries were written", callerGone: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const items = 2
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			logger := &countingUsageLogger{}
			if tt.callerGone {
				logger.onWrite = cancel
			}
			store := &hangingCompleteStore{MemoryStore: batchstore.NewMemoryStore()}
			orchestrator := newUsageOrchestrator(&batchItemsProvider{items: items}, store, logger)
			orchestrator.usageCompleteTimeout = 20 * time.Millisecond
			id := createUsageBatch(t, orchestrator)

			result, err := orchestrator.Results(ctx, id, "req-results")

			require.NoError(t, err)
			assert.Len(t, result.Response.Data, items)
			assert.Equal(t, items, logger.count())
			assert.True(t, store.live, "the marker is saved whether or not the caller waits")
			assert.True(t, store.bounded, "the marker is saved within a bound")
		})
	}
}

// A batch whose marker was not saved is written again, so that storage has
// every entry. The process that wrote it before marks the repeat, and the
// token and cost metrics in front of storage count the entries once.
func TestBatchResultsMarkRepeatedUsage(t *testing.T) {
	const items = 2
	tests := []struct {
		name  string
		store func(*batchstore.MemoryStore) batchstore.Store
	}{
		{name: "the marker was not saved", store: func(m *batchstore.MemoryStore) batchstore.Store { return failingCompleteStore{m} }},
		{name: "the store took no write", store: func(m *batchstore.MemoryStore) batchstore.Store { return unwritableStore{failingClaimStore{m}} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			provider := &batchItemsProvider{items: items}
			store := tt.store(batchstore.NewMemoryStore())
			fetch := func(logger *countingUsageLogger) *BatchOrchestrator {
				orchestrator := newUsageOrchestrator(provider, store, logger)
				orchestrator.usageClaimTimeout = 20 * time.Millisecond
				return orchestrator
			}
			logger := &countingUsageLogger{}
			orchestrator := fetch(logger)
			id := createUsageBatch(t, orchestrator)

			for range 2 {
				_, err := orchestrator.Results(ctx, id, "req-results")
				require.NoError(t, err)
			}
			assert.Equal(t, []bool{false, false, true, true}, logger.repeated())

			other := &countingUsageLogger{}
			_, err := fetch(other).Results(ctx, id, "req-results")
			require.NoError(t, err)
			assert.Equal(t, []bool{false, false}, other.repeated(), "another process has not counted the entries")
		})
	}
}

func TestBatchUsageHandoffsAreBounded(t *testing.T) {
	var handoffs batchUsageHandoffs
	for i := range batchUsageHandoffLimit + 1 {
		handoffs.set(fmt.Sprintf("batch-%d", i), true)
	}
	assert.Len(t, handoffs.batches, batchUsageHandoffLimit)
	assert.True(t, handoffs.has(fmt.Sprintf("batch-%d", batchUsageHandoffLimit)), "the newest batch is kept")

	handoffs.set(fmt.Sprintf("batch-%d", batchUsageHandoffLimit), false)
	assert.False(t, handoffs.has(fmt.Sprintf("batch-%d", batchUsageHandoffLimit)))
}
