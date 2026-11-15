package batch

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

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

// insertLegacyBatch stores a row as an earlier release wrote it: the
// serialized batch alone, with no usage claim beside it.
func insertLegacyBatch(t *testing.T, store Store, id, payload string) {
	t.Helper()
	ctx := context.Background()
	switch s := store.(type) {
	case *SQLStore:
		_, err := s.db.Exec(ctx, "INSERT INTO batches (id, created_at, updated_at, status, data) VALUES (?, ?, ?, ?, ?)", id, 1, 1, "completed", payload)
		require.NoError(t, err)
	case *MongoDBStore:
		_, err := s.collection.InsertOne(ctx, bson.M{"_id": id, "created_at": int64(1), "updated_at": int64(1), "status": "completed", "data": []byte(payload)})
		require.NoError(t, err)
	default:
		require.Failf(t, "unsupported store", "%T keeps no rows of an earlier release", store)
	}
}

// Earlier releases kept the marker inside the serialized batch: first in the
// batch's metadata, then beside the batch.
func TestStoreClaimUsageHonoursSerializedMarker(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{name: "marker beside the batch", payload: `{"batch":{"id":"batch-legacy","status":"completed"},"usage_logged_at":"2023-11-14T22:13:20Z"}`},
		{name: "marker in the batch metadata", payload: `{"batch":{"id":"batch-legacy","status":"completed","metadata":{"usage_logged_at":"1700000000"}}}`},
		{name: "marker in the metadata of a bare batch", payload: `{"id":"batch-legacy","status":"completed","metadata":{"usage_logged_at":"1700000000"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runStoreSuite(t, func(t *testing.T, store Store) {
				ctx := context.Background()
				insertLegacyBatch(t, store, "batch-legacy", tt.payload)

				claim, err := store.ClaimUsage(ctx, "batch-legacy", testUsageClaimTime.Add(time.Hour), testUsageClaimTimeout)
				require.NoError(t, err)
				assert.Equal(t, UsageLogged, claim)

				got, err := store.Get(ctx, "batch-legacy")
				require.NoError(t, err)
				require.NotNil(t, got.UsageLoggedAt)
				assert.True(t, got.UsageLoggedAt.Equal(testUsageClaimTime), "UsageLoggedAt = %v, want %v", got.UsageLoggedAt, testUsageClaimTime)
			})
		})
	}
}

func TestStoreUsageClaimOfMissingBatch(t *testing.T) {
	runClaimSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		_, err := store.ClaimUsage(ctx, "batch-missing", testUsageClaimTime, testUsageClaimTimeout)
		require.ErrorIs(t, err, ErrNotFound)
		require.ErrorIs(t, store.CompleteUsage(ctx, "batch-missing", testUsageClaimTime), ErrNotFound)
	})
}
