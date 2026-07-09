package pricingoverrides

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSQLiteStoreStoresPricingWithoutCurrency(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	store, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}

	if err := store.Upsert(context.Background(), Override{
		Selector: "openai/gpt-4o",
		Pricing:  Pricing{InputPerMtok: new(1.25)},
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	var rawPricing string
	if err := db.QueryRow(`SELECT pricing FROM model_pricing_overrides WHERE selector = 'openai/gpt-4o'`).Scan(&rawPricing); err != nil {
		t.Fatalf("read pricing JSON: %v", err)
	}
	if strings.Contains(rawPricing, "currency") {
		t.Fatalf("pricing JSON = %s, did not expect currency field", rawPricing)
	}

	overrides, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(overrides) != 1 {
		t.Fatalf("len(overrides) = %d, want 1", len(overrides))
	}
	if overrides[0].ProviderName != "openai" || overrides[0].Model != "gpt-4o" {
		t.Fatalf("stored parts = (%q, %q), want (openai, gpt-4o)", overrides[0].ProviderName, overrides[0].Model)
	}
}
