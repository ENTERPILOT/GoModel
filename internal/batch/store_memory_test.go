package batch

import (
	"context"
	"testing"

	"gomodel/internal/core"
)

func TestMemoryStoreLifecycle(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	b := &StoredBatch{
		Batch: &core.BatchResponse{
			ID:        "batch-1",
			Object:    "batch",
			Status:    "completed",
			CreatedAt: 100,
			Results: []core.BatchResultItem{
				{Index: 0, StatusCode: 200},
			},
		},
	}

	if err := store.Create(ctx, b); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := store.Get(ctx, "batch-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Batch == nil {
		t.Fatal("got.Batch = nil")
	}
	if got.Batch.ID != b.Batch.ID {
		t.Fatalf("id = %q, want %q", got.Batch.ID, b.Batch.ID)
	}
	if len(got.Batch.Results) != 1 {
		t.Fatalf("results len = %d, want 1", len(got.Batch.Results))
	}

	got.Batch.Status = "cancelled"
	if err := store.Update(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}

	got2, err := store.Get(ctx, "batch-1")
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

func TestMemoryStoreListAfter(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	inputs := []*StoredBatch{
		{Batch: &core.BatchResponse{ID: "batch-c", CreatedAt: 3, Status: "completed"}},
		{Batch: &core.BatchResponse{ID: "batch-b", CreatedAt: 2, Status: "completed"}},
		{Batch: &core.BatchResponse{ID: "batch-a", CreatedAt: 1, Status: "completed"}},
	}
	for _, b := range inputs {
		b.Batch.Object = "batch"
		if err := store.Create(ctx, b); err != nil {
			t.Fatalf("create %s: %v", b.Batch.ID, err)
		}
	}

	list, err := store.List(ctx, 2, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2", len(list))
	}
	if list[0].Batch.ID != "batch-c" || list[1].Batch.ID != "batch-b" {
		t.Fatalf("unexpected order: %s, %s", list[0].Batch.ID, list[1].Batch.ID)
	}

	next, err := store.List(ctx, 2, "batch-b")
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if len(next) != 1 || next[0].Batch.ID != "batch-a" {
		t.Fatalf("unexpected after result: %+v", next)
	}
}
