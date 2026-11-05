package clustersync

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func TestSQLStore(t *testing.T) {
	_, err := NewSQLStore(context.Background(), nil)
	require.Error(t, err)

	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		require.NoError(t, err)
		testStoreContract(t, store)
	})
}

func TestMongoDBStore(t *testing.T) {
	_, err := NewMongoDBStore(nil)
	require.Error(t, err)

	mongotest.Run(t, func(t *testing.T, database *mongo.Database) {
		store, err := NewMongoDBStore(database)
		require.NoError(t, err)
		testStoreContract(t, store)
	})
}

// testStoreContract is the behaviour every backend must share.
func testStoreContract(t *testing.T, store Store) {
	t.Helper()
	ctx := context.Background()

	revisions, err := store.Revisions(ctx)
	require.NoError(t, err)
	assert.Empty(t, revisions)

	require.NoError(t, store.Bump(ctx, "budgets"))
	require.NoError(t, store.Bump(ctx, "budgets"))
	require.NoError(t, store.Bump(ctx, "auth_keys"))

	revisions, err = store.Revisions(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{"budgets": 2, "auth_keys": 1}, revisions)

	// Instances bump concurrently; none of the increments may be lost.
	const writers = 8
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			assert.NoError(t, store.Bump(ctx, "rate_limits"))
		})
	}
	wg.Wait()

	revisions, err = store.Revisions(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(writers), revisions["rate_limits"])
}
