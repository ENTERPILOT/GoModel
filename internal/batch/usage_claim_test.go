package batch

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func runClaimSuite(t *testing.T, body func(t *testing.T, store Store)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) { body(t, NewMemoryStore()) })
	runStoreSuite(t, body)
}

func TestStoreClaimUsageSucceedsOnce(t *testing.T) {
	runClaimSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		require.NoError(t, store.Create(ctx, &StoredBatch{Batch: &core.BatchResponse{ID: "batch-claim", Status: "completed"}}))

		const claimers = 8
		claimed := make([]bool, claimers)
		errs := make([]error, claimers)
		var wg sync.WaitGroup
		for i := range claimers {
			wg.Go(func() {
				loggedAt := time.Unix(int64(1_700_000_000+i), 0).UTC()
				claimed[i], errs[i] = store.ClaimUsage(ctx, &StoredBatch{
					Batch:         &core.BatchResponse{ID: "batch-claim", Status: "completed", Usage: core.BatchUsageSummary{InputTokens: 10}},
					UsageLoggedAt: &loggedAt,
				})
			})
		}
		wg.Wait()

		for _, err := range errs {
			require.NoError(t, err)
		}
		wins := 0
		for _, won := range claimed {
			if won {
				wins++
			}
		}
		assert.Equal(t, 1, wins)
		got, err := store.Get(ctx, "batch-claim")
		require.NoError(t, err)
		assert.NotNil(t, got.UsageLoggedAt)
		assert.Equal(t, 10, got.Batch.Usage.InputTokens, "the claim persists the batch it carries")
	})
}

func TestStoreClaimUsageRejectsInvalidClaims(t *testing.T) {
	loggedAt := time.Now().UTC()
	runClaimSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		require.NoError(t, store.Create(ctx, &StoredBatch{Batch: &core.BatchResponse{ID: "batch-present"}}))

		_, err := store.ClaimUsage(ctx, &StoredBatch{Batch: &core.BatchResponse{ID: "batch-missing"}, UsageLoggedAt: &loggedAt})
		require.ErrorIs(t, err, ErrNotFound)
		_, err = store.ClaimUsage(ctx, &StoredBatch{Batch: &core.BatchResponse{ID: "batch-present"}})
		require.Error(t, err, "a claim must carry the usage marker")
	})
}
