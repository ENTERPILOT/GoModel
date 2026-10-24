package metadataoverrides

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// runStoreSuite runs body against every storage backend available here.
func runStoreSuite(t *testing.T, body func(t *testing.T, store Store)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		require.NoError(t, err)
		body(t, store)
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, err := NewMongoDBStore(db)
		require.NoError(t, err)
		body(t, store)
	})
}

func TestStoreRoundTripUpsertAndDelete(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		first := Override{
			Selector:     "pollinations/flux",
			ProviderName: "pollinations",
			Model:        "flux",
			Metadata: Metadata{
				Categories:    []core.ModelCategory{core.CategoryImage},
				Capabilities:  map[string]bool{"vision": true, "audio_input": false},
				ContextWindow: new(32000),
			},
		}
		require.NoError(t, store.Upsert(ctx, first))

		listed, err := store.List(ctx)
		require.NoError(t, err)
		require.Len(t, listed, 1)
		assert.Equal(t, first.Selector, listed[0].Selector)
		assert.Equal(t, "pollinations", listed[0].ProviderName)
		assert.Equal(t, "flux", listed[0].Model)
		assert.Equal(t, first.Metadata, listed[0].Metadata)
		assert.False(t, listed[0].CreatedAt.IsZero())

		replaced := first
		replaced.Metadata = Metadata{MaxOutputTokens: new(4096)}
		require.NoError(t, store.Upsert(ctx, replaced))
		listed, err = store.List(ctx)
		require.NoError(t, err)
		require.Len(t, listed, 1)
		assert.Equal(t, Metadata{MaxOutputTokens: new(4096)}, listed[0].Metadata)

		require.NoError(t, store.Delete(ctx, "pollinations/flux"))
		require.ErrorIs(t, store.Delete(ctx, "pollinations/flux"), ErrNotFound)
		listed, err = store.List(ctx)
		require.NoError(t, err)
		assert.Empty(t, listed)
	})
}
