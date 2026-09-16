package batch

import (
	"context"
	"slices"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func runSQLStoreTest(t *testing.T, body func(t *testing.T, store *SQLStore)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		require.NoError(t, err)

		body(t, store)
	})
}

// runStoreSuite exercises behaviour every Store implementation owes its
// callers, against each backend available in this environment.
func runStoreSuite(t *testing.T, body func(t *testing.T, store Store)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		require.NoError(t, err)

		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, err := NewMongoDBStore(db)
		require.NoError(t, err)

		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
}

func TestStoreLifecycle(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		b := &StoredBatch{
			Batch: &core.BatchResponse{
				ID:        "batch-sql-1",
				Object:    "batch",
				Status:    "completed",
				CreatedAt: 123,
				RequestCounts: core.BatchRequestCounts{
					Total:     1,
					Completed: 1,
				},
				Results: []core.BatchResultItem{
					{Index: 0, StatusCode: 200, URL: "/v1/chat/completions"},
				},
			},
		}
		err := store.Create(ctx, b)
		require.NoError(t, err)

		got, err := store.Get(ctx, b.Batch.ID)
		require.NoError(t, err)
		require.NotNil(t, got.Batch)
		assert.Equal(t, b.Batch.ID, got.Batch.ID)
		assert.Equal(t, 1, got.Batch.RequestCounts.Total)
		assert.Len(t, got.Batch.Results, 1)

		got.Batch.Status = "cancelled"
		err = store.Update(ctx, got)
		require.NoError(t, err)

		got2, err := store.Get(ctx, b.Batch.ID)
		require.NoError(t, err)
		require.NotNil(t, got2.Batch)
		assert.Equal(t, "cancelled", got2.Batch.Status)
	})
}

func TestStoreDelete(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		err := store.Delete(ctx, "missing")
		require.ErrorIs(t, err, ErrNotFound)

		b := &StoredBatch{Batch: &core.BatchResponse{ID: "batch-sql-del", Object: "batch", Status: "completed"}}
		err = store.Create(ctx, b)
		require.NoError(t, err)
		err = store.Delete(ctx, "batch-sql-del")
		require.NoError(t, err)
		_, err = store.Get(ctx, "batch-sql-del")
		require.ErrorIs(t, err, ErrNotFound)
	})
}

func TestStoreUpdateMissingReturnsNotFound(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		b := &StoredBatch{Batch: &core.BatchResponse{ID: "absent", Object: "batch", Status: "completed"}}
		err := store.Update(context.Background(), b)
		require.ErrorIs(t, err, ErrNotFound)
	})
}

func TestSQLStoreListPaginatesNewestFirst(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()
		// created_at descending, id descending as the tiebreak.
		for i, id := range []string{"batch-a", "batch-b", "batch-c"} {
			b := &StoredBatch{Batch: &core.BatchResponse{
				ID: id, Object: "batch", Status: "completed", CreatedAt: int64(i),
			}}
			err := store.Create(ctx, b)
			require.NoError(t, err, "create %s: %v", id, err)
		}

		page, err := store.List(ctx, 2, "", "")
		require.NoError(t, err)
		require.Len(t, page, 2)
		require.Equal(t, "batch-c", page[0].Batch.ID)
		require.Equal(t, "batch-b", page[1].Batch.ID)

		next, err := store.List(ctx, 2, "batch-b", "")
		require.NoError(t, err)
		require.Len(t, next, 1)
		require.Equal(t, "batch-a", next[0].Batch.ID)
	})
}

func TestSQLStoreListAfterUnknownCursorReturnsNotFound(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		_, err := store.List(context.Background(), 10, "absent", "")
		require.ErrorIs(t, err, ErrNotFound)
	})
}

func batchIDs(batches []*StoredBatch) []string {
	ids := make([]string, 0, len(batches))
	for _, b := range batches {
		ids = append(ids, b.Batch.ID)
	}
	return ids
}

func seedScopedBatches(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()
	rows := []struct {
		id, userPath string
		createdAt    int64
	}{
		{"batch-beta", "/team/beta", 5},
		{"batch-alpha-new", "/team/alpha", 4},
		{"batch-alpha-child", "/team/alpha/service", 3},
		{"batch-alpha-sibling", "/team/alpha-2", 2},
		{"batch-legacy", "", 1},
	}
	for _, row := range rows {
		err := store.Create(ctx, &StoredBatch{
			Batch:    &core.BatchResponse{ID: row.id, Object: "batch", Status: "completed", CreatedAt: row.createdAt},
			UserPath: row.userPath,
		})
		require.NoError(t, err, "create %s: %v", row.id, err)
	}
}

func TestStoreListFiltersByUserPathSubtree(t *testing.T) {
	suite := func(t *testing.T, store Store) {
		seedScopedBatches(t, store)
		ctx := context.Background()

		all, err := store.List(ctx, 10, "", "")
		require.NoError(t, err)
		got := batchIDs(all)
		require.Len(t, got, 5)

		scoped, err := store.List(ctx, 10, "", "/team/alpha")
		require.NoError(t, err)

		want := []string{"batch-alpha-new", "batch-alpha-child"}
		got = batchIDs(scoped)
		require.True(t, slices.Equal(got, want), "scoped list = %v, want %v", got, want)

		page, err := store.List(ctx, 10, "batch-alpha-new", "/team/alpha")
		require.NoError(t, err)
		got = batchIDs(page)
		require.True(t, slices.Equal(got, []string{"batch-alpha-child"}), "scoped page after cursor = %v, want [batch-alpha-child]", got)
		_, err = store.List(ctx, 10, "batch-beta", "/team/alpha")
		require.ErrorIs(t, err, ErrNotFound)

		root, err := store.List(ctx, 10, "", "/")
		require.NoError(t, err)
		got = batchIDs(root)
		require.Len(t, got, 4)
	}
	t.Run("memory", func(t *testing.T) { suite(t, NewMemoryStore()) })
	runStoreSuite(t, suite)
}

func TestSQLStoreBackfillsUserPathColumn(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		err := db.Schema(ctx, sqlSchema...)
		require.NoError(t, err)

		payload, err := serializeBatch(&StoredBatch{
			Batch:    &core.BatchResponse{ID: "batch-old", Object: "batch", Status: "completed", CreatedAt: 1},
			UserPath: "/team/alpha",
		})
		require.NoError(t, err)
		_, err = db.Exec(ctx, "INSERT INTO batches (id, created_at, updated_at, status, data) VALUES (?, ?, ?, ?, ?)", "batch-old", 1, 1, "completed", string(payload))
		require.NoError(t, err)

		store, err := NewSQLStore(ctx, db)
		require.NoError(t, err)

		scoped, err := store.List(ctx, 10, "", "/team/alpha")
		require.NoError(t, err)
		got := batchIDs(scoped)
		require.True(t, slices.Equal(got, []string{"batch-old"}), "scoped list after backfill = %v, want [batch-old]", got)
	})
}
