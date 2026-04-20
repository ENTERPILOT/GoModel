package modeloverrides

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestNewSQLiteStore_AddsMissingUserPathsColumn(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE model_overrides (
			selector TEXT PRIMARY KEY,
			provider_name TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)
	`)
	if err != nil {
		t.Fatalf("create model_overrides table: %v", err)
	}

	store, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	if store == nil {
		t.Fatal("NewSQLiteStore() = nil, want store")
	}

	rows, err := db.Query(`PRAGMA table_info('model_overrides')`)
	if err != nil {
		t.Fatalf("PRAGMA table_info() error = %v", err)
	}
	defer rows.Close()

	hasUserPathsColumn := false
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
		if name == "user_paths" {
			hasUserPathsColumn = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err() = %v", err)
	}
	if !hasUserPathsColumn {
		t.Fatal("user_paths column missing after initialization")
	}
}

func TestNewSQLiteStore_LegacyRowsDefaultUserPaths(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE model_overrides (
			selector TEXT PRIMARY KEY,
			provider_name TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)
	`)
	if err != nil {
		t.Fatalf("create model_overrides table: %v", err)
	}
	_, err = db.Exec(`
		INSERT INTO model_overrides (selector, provider_name, model, created_at, updated_at)
		VALUES ('openai/gpt-4o', 'openai', 'gpt-4o', 1, 2)
	`)
	if err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	store, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}

	overrides, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(overrides) != 1 {
		t.Fatalf("len(overrides) = %d, want 1", len(overrides))
	}
	if len(overrides[0].UserPaths) != 0 {
		t.Fatalf("overrides[0].UserPaths = %#v, want empty", overrides[0].UserPaths)
	}
}
