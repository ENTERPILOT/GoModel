package tagging

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func newTestStore(t *testing.T, db sqlx.DB) *SQLStore {
	t.Helper()
	store, err := NewSQLStore(context.Background(), db)
	require.NoError(t, err)

	return store
}

// runStoreSuite exercises behaviour every Store implementation owes its
// callers, against each backend available in this environment.
func runStoreSuite(t *testing.T, body func(t *testing.T, store Store)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store := newTestStore(t, db)
		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, err := NewMongoDBStore(context.Background(), db)
		require.NoError(t, err)

		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
}

func TestNewSQLStoreIsIdempotent(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		store := newTestStore(t, db)
		err := store.SaveRules(ctx, []Rule{{Header: "X-Keep"}})
		require.NoError(t, err)

		// Constructing again is what every restart does; it must neither fail
		// nor discard the saved rules.
		second := newTestStore(t, db)
		got, err := second.GetRules(ctx)
		require.NoError(t, err)
		if assert.Len(t, got, 1, "want X-Keep preserved, got %+v", got) {
			assert.Equal(t, "X-Keep", got[0].Header)
		}
	})
}

func TestNewSQLStoreRejectsNilDB(t *testing.T) {
	_, err := NewSQLStore(context.Background(), nil)
	require.Error(t, err, "NewSQLStore(nil) should fail")
}
