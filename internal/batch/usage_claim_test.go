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

const testUsageClaimTimeout = time.Minute

var testUsageClaimTime = time.Unix(1_700_000_000, 0).UTC()

func runClaimSuite(t *testing.T, body func(t *testing.T, store Store)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) { body(t, NewMemoryStore()) })
	runStoreSuite(t, body)
}

func createClaimBatch(t *testing.T, store Store, id string) {
	t.Helper()
	require.NoError(t, store.Create(context.Background(), &StoredBatch{Batch: &core.BatchResponse{ID: id, Status: "completed"}}))
}

func TestStoreClaimUsageGrantsOneClaim(t *testing.T) {
	runClaimSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		createClaimBatch(t, store, "batch-claim")

		const claimers = 8
		claims := make([]UsageClaim, claimers)
		errs := make([]error, claimers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range claimers {
			wg.Go(func() {
				<-start
				claims[i], errs[i] = store.ClaimUsage(ctx, "batch-claim", testUsageClaimTime, testUsageClaimTimeout)
			})
		}
		close(start)
		wg.Wait()

		granted := 0
		for i, claim := range claims {
			require.NoError(t, errs[i])
			if claim == UsageClaimed {
				granted++
				continue
			}
			assert.Equal(t, UsagePending, claim)
		}
		assert.Equal(t, 1, granted)

		loggedAt := testUsageClaimTime.Add(time.Second)
		require.NoError(t, store.CompleteUsage(ctx, "batch-claim", loggedAt))
		claim, err := store.ClaimUsage(ctx, "batch-claim", loggedAt.Add(2*testUsageClaimTimeout), testUsageClaimTimeout)
		require.NoError(t, err)
		assert.Equal(t, UsageLogged, claim, "logged usage is never claimed again")

		got, err := store.Get(ctx, "batch-claim")
		require.NoError(t, err)
		require.NotNil(t, got.UsageLoggedAt)
		assert.True(t, got.UsageLoggedAt.Equal(loggedAt), "UsageLoggedAt = %v, want %v", got.UsageLoggedAt, loggedAt)
		listed, err := store.List(ctx, 10, "", "")
		require.NoError(t, err)
		require.Len(t, listed, 1)
		assert.NotNil(t, listed[0].UsageLoggedAt)
	})
}

func TestStoreClaimUsageTakesOverStaleClaim(t *testing.T) {
	tests := []struct {
		name  string
		after time.Duration
		want  UsageClaim
	}{
		{name: "a claim younger than the timeout is kept", after: testUsageClaimTimeout - time.Nanosecond, want: UsagePending},
		{name: "a claim as old as the timeout is taken over", after: testUsageClaimTimeout, want: UsageClaimed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runClaimSuite(t, func(t *testing.T, store Store) {
				ctx := context.Background()
				createClaimBatch(t, store, "batch-stale")
				claim, err := store.ClaimUsage(ctx, "batch-stale", testUsageClaimTime, testUsageClaimTimeout)
				require.NoError(t, err)
				require.Equal(t, UsageClaimed, claim)

				retry := testUsageClaimTime.Add(tt.after)
				claim, err = store.ClaimUsage(ctx, "batch-stale", retry, testUsageClaimTimeout)
				require.NoError(t, err)
				assert.Equal(t, tt.want, claim)

				claim, err = store.ClaimUsage(ctx, "batch-stale", retry, testUsageClaimTimeout)
				require.NoError(t, err)
				assert.Equal(t, UsagePending, claim, "the claim now held is younger than the timeout")
			})
		})
	}
}

// A status poll reads a batch, waits for the provider, and saves its copy.
// When the usage was claimed or logged in between, the copy does not know.
func TestStoreUpdateKeepsUsageClaim(t *testing.T) {
	tests := []struct {
		name     string
		complete bool
		want     UsageClaim
	}{
		{name: "pending claim", want: UsagePending},
		{name: "logged usage", complete: true, want: UsageLogged},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runClaimSuite(t, func(t *testing.T, store Store) {
				ctx := context.Background()
				createClaimBatch(t, store, "batch-poll")
				stale, err := store.Get(ctx, "batch-poll")
				require.NoError(t, err)

				claim, err := store.ClaimUsage(ctx, "batch-poll", testUsageClaimTime, testUsageClaimTimeout)
				require.NoError(t, err)
				require.Equal(t, UsageClaimed, claim)
				if tt.complete {
					require.NoError(t, store.CompleteUsage(ctx, "batch-poll", testUsageClaimTime))
				}

				stale.Batch.Status = "cancelled"
				require.NoError(t, store.Update(ctx, stale))

				claim, err = store.ClaimUsage(ctx, "batch-poll", testUsageClaimTime.Add(time.Second), testUsageClaimTimeout)
				require.NoError(t, err)
				assert.Equal(t, tt.want, claim)
				got, err := store.Get(ctx, "batch-poll")
				require.NoError(t, err)
				assert.Equal(t, "cancelled", got.Batch.Status, "the update itself is saved")
				assert.Equal(t, tt.complete, got.UsageLoggedAt != nil)
			})
		})
	}
}

// Earlier releases kept the marker inside the serialized batch.
func TestStoreClaimUsageHonoursSerializedMarker(t *testing.T) {
	runClaimSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		loggedAt := testUsageClaimTime
		require.NoError(t, store.Create(ctx, &StoredBatch{
			Batch:         &core.BatchResponse{ID: "batch-legacy", Status: "completed"},
			UsageLoggedAt: &loggedAt,
		}))

		claim, err := store.ClaimUsage(ctx, "batch-legacy", testUsageClaimTime, testUsageClaimTimeout)
		require.NoError(t, err)
		assert.Equal(t, UsageLogged, claim)
	})
}

func TestStoreUsageClaimOfMissingBatch(t *testing.T) {
	runClaimSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		_, err := store.ClaimUsage(ctx, "batch-missing", testUsageClaimTime, testUsageClaimTimeout)
		require.ErrorIs(t, err, ErrNotFound)
		require.ErrorIs(t, store.CompleteUsage(ctx, "batch-missing", testUsageClaimTime), ErrNotFound)
	})
}
