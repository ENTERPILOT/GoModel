package virtualmodels

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/enterpilot/gomodel/internal/core"
)

func newSQLiteVMStore(t *testing.T) *SQLiteStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
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
