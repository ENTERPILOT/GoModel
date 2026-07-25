package pricingoverrides

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func runSQLStoreTest(t *testing.T, body func(t *testing.T, store *SQLStore, db sqlx.DB)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}
		body(t, store, db)
	})
}

func TestSQLStoreStoresPricingWithoutCurrency(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore, db sqlx.DB) {
		ctx := context.Background()

		if err := store.Upsert(ctx, Override{
			Selector: "openai/gpt-4o",
			Pricing:  Pricing{InputPerMtok: new(1.25)},
		}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}

		var rawPricing []byte
		err := db.QueryRow(ctx,
			`SELECT pricing FROM model_pricing_overrides WHERE selector = ?`, "openai/gpt-4o").
			Scan(&rawPricing)
		if err != nil {
			t.Fatalf("read pricing JSON: %v", err)
		}
		// An absent currency must stay absent in storage rather than being
		// persisted as an empty string.
		if strings.Contains(string(rawPricing), "currency") {
			t.Errorf("pricing JSON = %s, did not expect currency field", rawPricing)
		}

		overrides, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(overrides) != 1 {
			t.Fatalf("len(overrides) = %d, want 1", len(overrides))
		}
		if overrides[0].ProviderName != "openai" || overrides[0].Model != "gpt-4o" {
			t.Errorf("stored parts = (%q, %q), want (openai, gpt-4o)",
				overrides[0].ProviderName, overrides[0].Model)
		}
	})
}

func TestSQLStoreUpsertReplacesPricing(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore, _ sqlx.DB) {
		ctx := context.Background()

		if err := store.Upsert(ctx, Override{
			Selector: "openai/gpt-4o",
			Pricing:  Pricing{InputPerMtok: new(1.0)},
		}); err != nil {
			t.Fatalf("first Upsert: %v", err)
		}
		if err := store.Upsert(ctx, Override{
			Selector: "openai/gpt-4o",
			Pricing:  Pricing{InputPerMtok: new(2.0)},
		}); err != nil {
			t.Fatalf("second Upsert: %v", err)
		}

		overrides, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(overrides) != 1 {
			t.Fatalf("len(overrides) = %d, want 1 after re-upsert", len(overrides))
		}
		if overrides[0].Pricing.InputPerMtok == nil || *overrides[0].Pricing.InputPerMtok != 2.0 {
			t.Errorf("InputPerMtok = %v, want 2.0", overrides[0].Pricing.InputPerMtok)
		}
	})
}

func TestSQLStoreListIsOrderedBySelector(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore, _ sqlx.DB) {
		ctx := context.Background()

		for _, selector := range []string{"openai/gpt-4o", "anthropic/claude", "xai/grok"} {
			override := Override{Selector: selector, Pricing: Pricing{InputPerMtok: new(1.0)}}
			if err := store.Upsert(ctx, override); err != nil {
				t.Fatalf("Upsert %s: %v", selector, err)
			}
		}

		overrides, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		got := make([]string, 0, len(overrides))
		for _, override := range overrides {
			got = append(got, override.Selector)
		}
		want := []string{"anthropic/claude", "openai/gpt-4o", "xai/grok"}
		if len(got) != len(want) {
			t.Fatalf("selectors = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("selectors = %v, want %v", got, want)
			}
		}
	})
}

func TestSQLStoreDeleteMissingReturnsNotFound(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore, _ sqlx.DB) {
		err := store.Delete(context.Background(), "absent/model")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("Delete error = %v, want ErrNotFound", err)
		}
	})
}

func TestSQLStoreDeleteRemovesOverride(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore, _ sqlx.DB) {
		ctx := context.Background()

		if err := store.Upsert(ctx, Override{
			Selector: "openai/gpt-4o",
			Pricing:  Pricing{InputPerMtok: new(1.0)},
		}); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		// Selectors are trimmed on the way in and out, so a padded delete must
		// still find the row.
		if err := store.Delete(ctx, "  openai/gpt-4o  "); err != nil {
			t.Fatalf("Delete: %v", err)
		}

		overrides, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(overrides) != 0 {
			t.Errorf("len(overrides) = %d after delete, want 0", len(overrides))
		}
	})
}
