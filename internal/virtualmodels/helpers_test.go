package virtualmodels

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func runSQLStoreTest(t *testing.T, body func(t *testing.T, store *SQLStore)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}
		body(t, store)
	})
}

// newSQLVMStore returns a store on a fresh in-memory SQLite database, for
// tests that exercise logic above the store rather than the store itself.
func newSQLVMStore(t *testing.T) *SQLStore {
	t.Helper()
	store, err := NewSQLStore(context.Background(), sqlxtest.NewSQLite(t))
	if err != nil {
		t.Fatalf("NewSQLStore: %v", err)
	}
	return store
}

// fakeCatalog satisfies Catalog for the alias and access-override engines.
type fakeCatalog struct {
	providers []string
	supported map[string]core.Model
	// stale marks models whose provider inventory is stale: still supported,
	// but not available for target selection.
	stale map[string]bool
}

func (c fakeCatalog) Supports(model string) bool {
	_, ok := c.supported[model]
	return ok
}

func (c fakeCatalog) ModelAvailable(model string) bool {
	return c.Supports(model) && !c.stale[model]
}

func (c fakeCatalog) GetProviderType(model string) string {
	if _, ok := c.supported[model]; ok {
		return "openai"
	}
	return ""
}

func (c fakeCatalog) LookupModel(model string) (*core.Model, bool) {
	m, ok := c.supported[model]
	if !ok {
		return nil, false
	}
	clone := m
	return &clone, true
}

func (c fakeCatalog) ProviderNames() []string { return c.providers }

func testCatalog() fakeCatalog {
	return fakeCatalog{
		providers: []string{"openai"},
		supported: map[string]core.Model{
			"openai/gpt-4o": {ID: "openai/gpt-4o", Object: "model", OwnedBy: "openai"},
		},
	}
}
