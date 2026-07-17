package batch

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/storage"
)

func TestSQLiteStoreLifecycle(t *testing.T) {
	st, err := storage.NewSQLite(storage.SQLiteConfig{Path: filepath.Join(t.TempDir(), "batches.db")})
	if err != nil {
		t.Fatalf("new sqlite storage: %v", err)
	}
	defer st.Close()

	store, err := NewSQLiteStore(st.DB())
	if err != nil {
		t.Fatalf("new sqlite batch store: %v", err)
	}

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
		t.Fatalf("id = %q, want %q", got.Batch.ID, b.Batch.ID)
	}
	if got.Batch.RequestCounts.Total != 1 {
		t.Fatalf("request_counts.total = %d, want 1", got.Batch.RequestCounts.Total)
	}
	if len(got.Batch.Results) != 1 {
		t.Fatalf("results len = %d, want 1", len(got.Batch.Results))
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
		t.Fatalf("status = %q, want cancelled", got2.Batch.Status)
	}
}

func TestSQLiteStoreDelete(t *testing.T) {
	st, err := storage.NewSQLite(storage.SQLiteConfig{Path: filepath.Join(t.TempDir(), "batches.db")})
	if err != nil {
		t.Fatalf("new sqlite storage: %v", err)
	}
	defer st.Close()

	store, err := NewSQLiteStore(st.DB())
	if err != nil {
		t.Fatalf("new sqlite batch store: %v", err)
	}

	ctx := context.Background()
	if err := store.Delete(ctx, "missing"); err != ErrNotFound {
		t.Fatalf("delete missing = %v, want ErrNotFound", err)
	}

	b := &StoredBatch{Batch: &core.BatchResponse{ID: "batch-sql-del", Object: "batch", Status: "completed"}}
	if err := store.Create(ctx, b); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.Delete(ctx, "batch-sql-del"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Get(ctx, "batch-sql-del"); err != ErrNotFound {
		t.Fatalf("get after delete = %v, want ErrNotFound", err)
	}
}
