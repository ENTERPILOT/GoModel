package guardrails

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "modernc.org/sqlite"
)

func TestNewSQLiteStore_AddsMissingUserPathColumn(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE guardrail_definitions (
			name TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			config JSON NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)
	`)
	if err != nil {
		t.Fatalf("create guardrail_definitions table: %v", err)
	}

	store, err := NewSQLiteStore(context.Background(), db)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	if store == nil {
		t.Fatal("NewSQLiteStore() = nil, want store")
	}

	rows, err := db.Query(`PRAGMA table_info('guardrail_definitions')`)
	if err != nil {
		t.Fatalf("PRAGMA table_info() error = %v", err)
	}
	defer rows.Close()

	hasUserPathColumn := false
	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue sql.NullString
		var primaryKey int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("rows.Scan() error = %v", err)
		}
		if name == "user_path" {
			hasUserPathColumn = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err() = %v", err)
	}
	if !hasUserPathColumn {
		t.Fatal("user_path column missing after initialization")
	}
}

func TestSQLiteStore_UpsertAndListRoundTripsUserPath(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	store, err := NewSQLiteStore(context.Background(), db)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}

	err = store.Upsert(context.Background(), Definition{
		Name:        "policy-system",
		Type:        "system_prompt",
		Description: "Default policy",
		UserPath:    "/team/alpha",
		Config:      rawConfig(t, map[string]any{"mode": "inject", "content": "be careful"}),
	})
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	definitions, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(definitions) != 1 {
		t.Fatalf("len(definitions) = %d, want 1", len(definitions))
	}
	if definitions[0].UserPath != "/team/alpha" {
		t.Fatalf("definitions[0].UserPath = %q, want /team/alpha", definitions[0].UserPath)
	}
}

func TestIsSQLiteDuplicateColumnError_RequiresColumnContext(t *testing.T) {
	t.Parallel()

	if !isSQLiteDuplicateColumnError(errors.New("duplicate column name: user_path")) {
		t.Fatal("isSQLiteDuplicateColumnError() = false, want true for duplicate column")
	}
	if !isSQLiteDuplicateColumnError(errors.New("column user_path already exists")) {
		t.Fatal("isSQLiteDuplicateColumnError() = false, want true for existing column")
	}
	if isSQLiteDuplicateColumnError(errors.New("table guardrail_definitions already exists")) {
		t.Fatal("isSQLiteDuplicateColumnError() = true, want false for non-column already exists")
	}
}
