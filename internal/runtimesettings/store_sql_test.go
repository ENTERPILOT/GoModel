package runtimesettings

import (
	"context"
	"sync"
	"testing"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newSQLiteStore(t *testing.T) *SQLStore {
	t.Helper()
	backend := newTestStorage(t)
	store, err := storage.ResolveSQLBackend[*SQLStore](context.Background(), backend,
		func(db sqlx.DB) (*SQLStore, error) { return NewSQLStore(context.Background(), db) },
		nil,
	)
	require.NoError(t, err)

	return store
}

func TestSQLStoreSetDefaultKeepsFirstValue(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteStore(t)
	stored, err := store.SetDefault(ctx, "install_id", "first")
	require.NoError(t, err)
	require.Equal(t, "first", stored)
	stored, err = store.SetDefault(ctx, "install_id", "second")
	require.NoError(t, err)
	require.Equal(t, "first", stored)
	err = store.Set(ctx, "install_id", "third")
	require.NoError(t, err)
	stored, err = store.SetDefault(ctx, "install_id", "fourth")
	require.NoError(t, err)
	require.Equal(t, "third", stored)
}

func TestSQLStoreSetDefaultConvergesConcurrentWriters(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteStore(t)

	const writers = 8
	results := make([]string, writers)
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			stored, err := store.SetDefault(ctx, "install_id", string(rune('a'+w)))
			assert.NoError(t, err, "writer %d: %v", w, err)

			results[w] = stored
		})
	}
	wg.Wait()

	winner, found, err := store.Get(ctx, "install_id")
	require.NoError(t, err)
	require.True(t, found)

	for w, got := range results {
		assert.Equal(t, winner, got, "worker %d", w)
	}
}
