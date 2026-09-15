package tagging

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStore_RoundTrip(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		want := []Rule{
			{Header: "X-Team", Prefix: "team-", Delimiter: ","},
			{Header: "X-Env", DoNotPass: true, Delimiter: "|"},
			{Header: "X-Bare"},
		}
		err := store.SaveRules(ctx, want)
		require.NoError(t, err)

		got, err := store.GetRules(ctx)
		require.NoError(t, err)
		require.Len(t, got, len(want))
		for i := range want {
			assert.Equal(t, want[i], got[i], "rule %d", i)
		}
	})
}

func TestStore_ManagedFlagIsNeverPersisted(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		// Managed marks declarative config/env rules; a caller that saves one
		// by mistake must not be able to make it read-only in the dashboard.
		err := store.SaveRules(ctx, []Rule{{Header: "X-Team", Managed: true}})
		require.NoError(t, err)

		got, err := store.GetRules(ctx)
		require.NoError(t, err)
		if assert.Len(t, got, 1, "want X-Team only") {
			assert.False(t, got[0].Managed, "got %+v, want X-Team with Managed=false", got)
		}
	})
}

func TestStore_GetRulesEmptyWhenUnset(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		// A store with nothing saved must read as "no operator rules", not as
		// an error: it is the state of every fresh deployment.
		got, err := store.GetRules(context.Background())
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}

func TestStore_SaveReplacesPreviousRules(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		err := store.SaveRules(ctx, []Rule{{Header: "X-One"}, {Header: "X-Two"}})
		require.NoError(t, err, "first SaveRules")
		// SaveRules replaces the whole set rather than merging, so a shorter
		// second save must not leave the dropped rule behind.
		err = store.SaveRules(ctx, []Rule{{Header: "X-Three"}})
		require.NoError(t, err, "second SaveRules")

		got, err := store.GetRules(ctx)
		require.NoError(t, err)
		if assert.Len(t, got, 1, "want only X-Three, got %+v", got) {
			assert.Equal(t, "X-Three", got[0].Header)
		}
	})
}

func TestStore_SaveEmptyClearsRules(t *testing.T) {
	for name, empty := range map[string][]Rule{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			runStoreSuite(t, func(t *testing.T, store Store) {
				ctx := context.Background()

				err := store.SaveRules(ctx, []Rule{{Header: "X-One"}})
				require.NoError(t, err)
				err = store.SaveRules(ctx, empty)
				require.NoError(t, err, "SaveRules(%s)", name)

				got, err := store.GetRules(ctx)
				require.NoError(t, err)
				assert.Empty(t, got)
			})
		})
	}
}
