package pricingoverrides

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runSQLStoreTest(t *testing.T, body func(t *testing.T, store *SQLStore, db sqlx.DB)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		require.NoError(t, err)

		body(t, store, db)
	})
}

// runStoreSuite exercises behaviour every Store implementation owes its
// callers, against each backend available in this environment.
func runStoreSuite(t *testing.T, body func(t *testing.T, store Store)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		require.NoError(t, err)

		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, err := NewMongoDBStore(db)
		require.NoError(t, err)

		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
}

func TestSQLStoreStoresPricingWithoutCurrency(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore, db sqlx.DB) {
		ctx := context.Background()
		err := store.Upsert(ctx, Override{
			Selector: "openai/gpt-4o",
			Pricing:  Pricing{InputPerMtok: new(1.25)},
		})
		require.NoError(t, err)

		var rawPricing []byte
		err = db.QueryRow(ctx,
			`SELECT pricing FROM model_pricing_overrides WHERE selector = ?`, "openai/gpt-4o").
			Scan(&rawPricing)
		require.NoError(t, err)

		// An absent currency must stay absent in storage rather than being
		// persisted as an empty string.
		assert.NotContains(t, string(rawPricing), "currency", "pricing JSON = %s, did not expect currency field", rawPricing)

		overrides, err := store.List(ctx)
		require.NoError(t, err)
		require.Len(t, overrides, 1)
		assert.Equal(t, "openai", overrides[0].ProviderName)
		assert.Equal(t, "gpt-4o", overrides[0].Model)
	})
}

func TestStoreUpsertReplacesPricing(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		err := store.Upsert(ctx, Override{
			Selector: "openai/gpt-4o",
			Pricing:  Pricing{InputPerMtok: new(1.0)},
		})
		require.NoError(t, err)
		err = store.Upsert(ctx, Override{
			Selector: "openai/gpt-4o",
			Pricing:  Pricing{InputPerMtok: new(2.0)},
		})
		require.NoError(t, err)

		overrides, err := store.List(ctx)
		require.NoError(t, err)
		require.Len(t, overrides, 1)
		require.NotNil(t, overrides[0].Pricing.InputPerMtok)
		assert.Equal(t, 2.0, *overrides[0].Pricing.InputPerMtok)
	})
}

func TestSQLStoreListIsOrderedBySelector(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore, _ sqlx.DB) {
		ctx := context.Background()

		for _, selector := range []string{"openai/gpt-4o", "anthropic/claude", "xai/grok"} {
			override := Override{Selector: selector, Pricing: Pricing{InputPerMtok: new(1.0)}}
			err := store.Upsert(ctx, override)
			require.NoError(t, err, "Upsert %s: %v", selector, err)
		}

		overrides, err := store.List(ctx)
		require.NoError(t, err)

		got := make([]string, 0, len(overrides))
		for _, override := range overrides {
			got = append(got, override.Selector)
		}
		want := []string{"anthropic/claude", "openai/gpt-4o", "xai/grok"}
		require.Len(t, got, len(want), "selectors = %v, want %v", got, want)

		for i := range want {
			require.Equal(t, want[i], got[i], "selectors = %v, want %v", got, want)
		}
	})
}

func TestStoreDeleteMissingReturnsNotFound(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		err := store.Delete(context.Background(), "absent/model")
		require.ErrorIs(t, err, ErrNotFound)
	})
}

func TestStoreDeleteRemovesOverride(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()
		err := store.Upsert(ctx, Override{
			Selector: "openai/gpt-4o",
			Pricing:  Pricing{InputPerMtok: new(1.0)},
		})
		require.NoError(t, err)
		err = // Selectors are trimmed on the way in and out, so a padded delete must
			// still find the row.
			store.Delete(ctx, "  openai/gpt-4o  ")
		require.NoError(t, err)

		overrides, err := store.List(ctx)
		require.NoError(t, err)
		assert.Empty(t, overrides)
	})
}
