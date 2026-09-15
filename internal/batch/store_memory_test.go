package batch

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
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
	err := store.Create(ctx, b)
	require.NoError(t, err)

	got, err := store.Get(ctx, "batch-1")
	require.NoError(t, err)
	require.NotNil(t, got.Batch)
	require.Equal(t, b.Batch.ID, got.Batch.ID)
	require.Len(t, got.Batch.Results, 1)

	got.Batch.Status = "cancelled"
	err = store.Update(ctx, got)
	require.NoError(t, err)

	got2, err := store.Get(ctx, "batch-1")
	require.NoError(t, err)
	require.NotNil(t, got2.Batch)
	require.Equal(t, "cancelled", got2.Batch.Status)
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
		err := store.Create(ctx, b)
		require.NoError(t, err, "create %s: %v", b.Batch.ID, err)
	}

	list, err := store.List(ctx, 2, "", "")
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, "batch-c", list[0].Batch.ID)
	require.Equal(t, "batch-b", list[1].Batch.ID)

	next, err := store.List(ctx, 2, "batch-b", "")
	require.NoError(t, err)
	require.Len(t, next, 1)
	require.Equal(t, "batch-a", next[0].Batch.ID)
}

func TestMemoryStoreDelete(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	err := store.Delete(ctx, "missing")
	require.Equal(t, ErrNotFound, err)

	b := &StoredBatch{Batch: &core.BatchResponse{ID: "batch-1", Object: "batch", Status: "completed"}}
	err = store.Create(ctx, b)
	require.NoError(t, err)
	err = store.Delete(ctx, "batch-1")
	require.NoError(t, err)
	_, err = store.Get(ctx, "batch-1")
	require.Equal(t, ErrNotFound, err)
}
