package workflows

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestNewSQLiteStore_SkipsExistingScopeUserPathMigration(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE workflow_versions (
			id TEXT PRIMARY KEY,
			scope_provider TEXT,
			scope_model TEXT,
			scope_user_path TEXT,
			scope_key TEXT NOT NULL,
			version INTEGER NOT NULL,
			active INTEGER NOT NULL DEFAULT 1,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			workflow_payload JSON NOT NULL,
			workflow_hash TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)
	`)
	if err != nil {
		t.Fatalf("create workflow_versions table: %v", err)
	}

	store, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	if store == nil {
		t.Fatal("NewSQLiteStore() = nil, want store")
	}
}

func TestNewSQLiteStore_AddsMissingScopeUserPathColumn(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE workflow_versions (
			id TEXT PRIMARY KEY,
			scope_provider TEXT,
			scope_model TEXT,
			scope_key TEXT NOT NULL,
			version INTEGER NOT NULL,
			active INTEGER NOT NULL DEFAULT 1,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			workflow_payload JSON NOT NULL,
			workflow_hash TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)
	`)
	if err != nil {
		t.Fatalf("create workflow_versions table: %v", err)
	}

	store, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	if store == nil {
		t.Fatal("NewSQLiteStore() = nil, want store")
	}

	rows, err := db.Query(`PRAGMA table_info('workflow_versions')`)
	if err != nil {
		t.Fatalf("PRAGMA table_info() error = %v", err)
	}
	defer rows.Close()

	hasScopeUserPathColumn := false
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
		if name == "scope_user_path" {
			hasScopeUserPathColumn = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err() = %v", err)
	}
	if !hasScopeUserPathColumn {
		t.Fatal("scope_user_path column missing after initialization")
	}
}
