package tagging

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T, db sqlx.DB) *SQLStore {
	t.Helper()
	store, err := NewSQLStore(context.Background(), db)
	require.NoError(t, err)

	return store
}

func TestSQLStoreRoundTrip(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		store := newTestStore(t, db)

		want := []Rule{
			{Header: "X-Team", Prefix: "team-", Delimiter: ","},
			{Header: "X-Env", DoNotPass: true, Delimiter: "|"},
		}
		err := store.SaveRules(ctx, want)
		require.NoError(t, err)

		got, err := store.GetRules(ctx)
		require.NoError(t, err)
		require.Len(t, got, len(want))

		for i := range want {
			assert.Equal(t, want[i], got[i], "rule %d = %+v, want %+v", i, got[i], want[i])
		}
	})
}

func TestSQLStoreGetRulesEmptyWhenUnset(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store := newTestStore(t, db)

		// A store with nothing saved must read as "no operator rules", not as
		// an error: it is the state of every fresh deployment.
		got, err := store.GetRules(context.Background())
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

func TestSQLStoreSaveReplacesPreviousRules(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		store := newTestStore(t, db)
		err := store.SaveRules(ctx, []Rule{{Header: "X-One"}, {Header: "X-Two"}})
		require.NoError(t, err)
		err = // SaveRules replaces the whole set rather than merging, so a shorter
			// second save must not leave the dropped rule behind.
			store.SaveRules(ctx, []Rule{{Header: "X-Three"}})
		require.NoError(t, err)

		got, err := store.GetRules(ctx)
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "X-Three", got[0].Header)
	})
}

func TestSQLStoreSaveEmptyClearsRules(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		store := newTestStore(t, db)
		err := store.SaveRules(ctx, []Rule{{Header: "X-One"}})
		require.NoError(t, err)
		err = store.SaveRules(ctx, nil)
		require.NoError(t, err)

		got, err := store.GetRules(ctx)
		require.NoError(t, err)
		assert.Empty(t, got)
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
		require.Len(t, got, 1)
		assert.Equal(t, "X-Keep", got[0].Header)
	})
}

func TestNewSQLStoreRejectsNilDB(t *testing.T) {
	_, err := NewSQLStore(context.Background(), nil)
	require.Error(t, err)
}
