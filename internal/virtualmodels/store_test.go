package virtualmodels

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStore_RoundTripRedirectAndPolicy(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		redirect := VirtualModel{
			Source:          "fast",
			Targets:         []Target{{Provider: "openai", Model: "gpt-4o"}},
			Description:     "primary",
			Slowdown:        new(0.4),
			SessionAffinity: new(false),
			Failover:        new(false),
			Enabled:         true,
		}
		policy := VirtualModel{
			Source:       "openai/gpt-4o",
			ProviderName: "openai",
			Model:        "gpt-4o",
			UserPaths:    []string{"/team"},
			Slowdown:     new(0.2),
			Enabled:      true,
		}
		disabledOverride := VirtualModel{
			Source:   "no-slowdown",
			Targets:  []Target{{Provider: "openai", Model: "gpt-4o"}},
			Slowdown: new(0.0),
			Enabled:  true,
		}
		err := store.Upsert(ctx, redirect)
		require.NoError(t, err)
		err = store.Upsert(ctx, policy)
		require.NoError(t, err)
		err = store.Upsert(ctx, disabledOverride)
		require.NoError(t, err)

		got, err := store.List(ctx)
		require.NoError(t, err)
		require.Len(t, got, 3)

		gotRedirect, err := store.Get(ctx, "fast")
		require.NoError(t, err)
		require.True(t, gotRedirect.IsRedirect())
		require.Len(t, gotRedirect.Targets, 1)
		require.Equal(t, "gpt-4o", gotRedirect.Targets[0].Model)
		require.Equal(t, "openai", gotRedirect.Targets[0].Provider)
		require.NotNil(t, gotRedirect.Slowdown)
		require.Equal(t, 0.4, *gotRedirect.Slowdown)

		require.NotNil(t, gotRedirect.SessionAffinity)
		require.False(t, *gotRedirect.SessionAffinity, "Get(fast).SessionAffinity: want explicit false")
		require.NotNil(t, gotRedirect.Failover)
		require.False(t, *gotRedirect.Failover, "Get(fast).Failover: want explicit false")

		gotPolicy, err := store.Get(ctx, "openai/gpt-4o")
		require.NoError(t, err)
		require.False(t, gotPolicy.IsRedirect())
		require.Len(t, gotPolicy.UserPaths, 1)
		require.Equal(t, "/team", gotPolicy.UserPaths[0])
		require.NotNil(t, gotPolicy.Slowdown)
		require.Equal(t, 0.2, *gotPolicy.Slowdown)
		require.Nil(t, gotPolicy.SessionAffinity)
		require.Nil(t, gotPolicy.Failover)

		gotDisabled, err := store.Get(ctx, "no-slowdown")
		require.NoError(t, err)
		require.NotNil(t, gotDisabled.Slowdown)
		require.Equal(t, float64(0), *gotDisabled.Slowdown)
	})
}

func TestStore_GetMissingAndDelete(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		_, err := store.Get(ctx, "nope")
		require.ErrorIs(t, err, ErrNotFound)
		err = store.Delete(ctx, "nope")
		require.ErrorIs(t, err, ErrNotFound)
		err = store.Upsert(ctx, VirtualModel{Source: "x", Targets: []Target{{Model: "m"}}, Enabled: true})
		require.NoError(t, err)
		err = store.Delete(ctx, "x")
		require.NoError(t, err)
		_, err = store.Get(ctx, "x")
		require.ErrorIs(t, err, ErrNotFound)
	})
}

func TestSQLStore_ListSurfacesUndecodableRow(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		store, err := NewSQLStore(ctx, db)
		require.NoError(t, err)
		err = store.Upsert(ctx, VirtualModel{Source: "ok", Targets: []Target{{Model: "openai/gpt-4o"}}, Enabled: true})
		require.NoError(t, err)
		// A row whose targets column is not JSON cannot be scanned; List must
		// report it rather than return a partial list.
		_, err = db.Exec(ctx, `INSERT INTO virtual_models (source, targets, created_at, updated_at) VALUES ('broken', 'not-json', 0, 0)`)
		require.NoError(t, err)
		_, err = store.List(ctx)
		require.ErrorContains(t, err, "decode targets")
	})
}

// A stored timestamp outside the range JSON can represent — a row written while
// the host clock was wrong, or hand-edited — must still list. The admin API
// serializes every row into one response, so a single unencodable column would
// otherwise fail the whole listing with an opaque 500 (issue #881).
func TestSQLStore_ListsRowWithOutOfRangeTimestamp(t *testing.T) {
	ctx := context.Background()
	db := sqlxtest.NewSQLite(t)
	store, err := NewSQLStore(ctx, db)
	require.NoError(t, err)

	t.Cleanup(func() { _ = store.Close() })

	vm := VirtualModel{
		Source:  "fast",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: true,
	}
	err = store.Upsert(ctx, vm)
	require.NoError(t, err)

	const beyondYear9999 = int64(99999999999999)
	_, err = db.Exec(ctx,
		"UPDATE virtual_models SET created_at = ?, updated_at = ? WHERE source = ?",
		beyondYear9999, beyondYear9999, vm.Source)
	require.NoError(t, err)

	rows, err := store.List(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.True(t, rows[0].CreatedAt.IsZero())
	assert.True(t, rows[0].UpdatedAt.IsZero(), "timestamps = %s / %s, want the zero time", rows[0].CreatedAt, rows[0].UpdatedAt)
	_, err = json.Marshal(rows)
	require.NoError(t, err)
}
