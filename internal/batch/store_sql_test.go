package batch

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func runSQLStoreTest(t *testing.T, body func(t *testing.T, store *SQLStore)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}
		body(t, store)
	})
}

// runStoreSuite exercises behaviour every Store implementation owes its
// callers, against each backend available in this environment.
func runStoreSuite(t *testing.T, body func(t *testing.T, store Store)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, err := NewMongoDBStore(db)
		if err != nil {
			t.Fatalf("NewMongoDBStore: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
}

func TestSQLStoreLifecycle(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
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

		if err := store.Create(ctx, b); err != nil {
			t.Fatalf("create: %v", err)
		}

		got, err := store.Get(ctx, b.Batch.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.Batch == nil {
			t.Fatal("got.Batch = nil")
		}
		if got.Batch.ID != b.Batch.ID {
			t.Errorf("id = %q, want %q", got.Batch.ID, b.Batch.ID)
		}
		if got.Batch.RequestCounts.Total != 1 {
			t.Errorf("request_counts.total = %d, want 1", got.Batch.RequestCounts.Total)
		}
		if len(got.Batch.Results) != 1 {
			t.Errorf("results len = %d, want 1", len(got.Batch.Results))
		}

		got.Batch.Status = "cancelled"
		if err := store.Update(ctx, got); err != nil {
			t.Fatalf("update: %v", err)
		}

		got2, err := store.Get(ctx, b.Batch.ID)
		if err != nil {
			t.Fatalf("get after update: %v", err)
		}
		if got2.Batch == nil {
			t.Fatal("got2.Batch = nil")
		}
		if got2.Batch.Status != "cancelled" {
			t.Errorf("status = %q, want cancelled", got2.Batch.Status)
		}
	})
}

func TestStoreDelete(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		if err := store.Delete(ctx, "missing"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("delete missing = %v, want ErrNotFound", err)
		}

		b := &StoredBatch{Batch: &core.BatchResponse{ID: "batch-sql-del", Object: "batch", Status: "completed"}}
		if err := store.Create(ctx, b); err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := store.Delete(ctx, "batch-sql-del"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, err := store.Get(ctx, "batch-sql-del"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get after delete = %v, want ErrNotFound", err)
		}
	})
}

func TestSQLStoreUpdateMissingReturnsNotFound(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		b := &StoredBatch{Batch: &core.BatchResponse{ID: "absent", Object: "batch", Status: "completed"}}
		if err := store.Update(context.Background(), b); !errors.Is(err, ErrNotFound) {
			t.Fatalf("update missing = %v, want ErrNotFound", err)
		}
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
			if err := store.Create(ctx, b); err != nil {
				t.Fatalf("create %s: %v", id, err)
			}
		}

		page, err := store.List(ctx, 2, "", "")
		if err != nil {
			t.Fatalf("first page: %v", err)
		}
		if len(page) != 2 || page[0].Batch.ID != "batch-c" || page[1].Batch.ID != "batch-b" {
			t.Fatalf("first page = %v, want [batch-c batch-b]", batchIDs(page))
		}

		next, err := store.List(ctx, 2, "batch-b", "")
		if err != nil {
			t.Fatalf("second page: %v", err)
		}
		if len(next) != 1 || next[0].Batch.ID != "batch-a" {
			t.Fatalf("second page = %v, want [batch-a]", batchIDs(next))
		}
	})
}

func TestSQLStoreListAfterUnknownCursorReturnsNotFound(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		if _, err := store.List(context.Background(), 10, "absent", ""); !errors.Is(err, ErrNotFound) {
			t.Fatalf("List after unknown cursor = %v, want ErrNotFound", err)
		}
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
		if err := store.Create(ctx, &StoredBatch{
			Batch:    &core.BatchResponse{ID: row.id, Object: "batch", Status: "completed", CreatedAt: row.createdAt},
			UserPath: row.userPath,
		}); err != nil {
			t.Fatalf("create %s: %v", row.id, err)
		}
	}
}

func TestStoreListFiltersByUserPathSubtree(t *testing.T) {
	suite := func(t *testing.T, store Store) {
		seedScopedBatches(t, store)
		ctx := context.Background()

		all, err := store.List(ctx, 10, "", "")
		if err != nil {
			t.Fatalf("list all: %v", err)
		}
		if got := batchIDs(all); len(got) != 5 {
			t.Fatalf("unfiltered list = %v, want 5 rows", got)
		}

		scoped, err := store.List(ctx, 10, "", "/team/alpha")
		if err != nil {
			t.Fatalf("list scoped: %v", err)
		}
		want := []string{"batch-alpha-new", "batch-alpha-child"}
		if got := batchIDs(scoped); !slices.Equal(got, want) {
			t.Fatalf("scoped list = %v, want %v", got, want)
		}

		page, err := store.List(ctx, 10, "batch-alpha-new", "/team/alpha")
		if err != nil {
			t.Fatalf("list scoped after cursor: %v", err)
		}
		if got := batchIDs(page); !slices.Equal(got, []string{"batch-alpha-child"}) {
			t.Fatalf("scoped page after cursor = %v, want [batch-alpha-child]", got)
		}

		if _, err := store.List(ctx, 10, "batch-beta", "/team/alpha"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("foreign cursor error = %v, want ErrNotFound", err)
		}

		root, err := store.List(ctx, 10, "", "/")
		if err != nil {
			t.Fatalf("list root: %v", err)
		}
		if got := batchIDs(root); len(got) != 4 {
			t.Fatalf("root list = %v, want every tracked path but the legacy row", got)
		}
	}
	t.Run("memory", func(t *testing.T) { suite(t, NewMemoryStore()) })
	runStoreSuite(t, suite)
}

func TestSQLStoreBackfillsUserPathColumn(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		if err := db.Schema(ctx, sqlSchema...); err != nil {
			t.Fatalf("legacy schema: %v", err)
		}
		payload, err := serializeBatch(&StoredBatch{
			Batch:    &core.BatchResponse{ID: "batch-old", Object: "batch", Status: "completed", CreatedAt: 1},
			UserPath: "/team/alpha",
		})
		if err != nil {
			t.Fatalf("serialize: %v", err)
		}
		if _, err := db.Exec(ctx, "INSERT INTO batches (id, created_at, updated_at, status, data) VALUES (?, ?, ?, ?, ?)", "batch-old", 1, 1, "completed", string(payload)); err != nil {
			t.Fatalf("insert legacy row: %v", err)
		}

		store, err := NewSQLStore(ctx, db)
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}
		scoped, err := store.List(ctx, 10, "", "/team/alpha")
		if err != nil {
			t.Fatalf("list scoped: %v", err)
		}
		if got := batchIDs(scoped); !slices.Equal(got, []string{"batch-old"}) {
			t.Fatalf("scoped list after backfill = %v, want [batch-old]", got)
		}
	})
}
