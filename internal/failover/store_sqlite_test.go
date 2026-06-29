package failover

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSQLiteStoreMigratesLegacyFailoverRulesSchema(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE failover_rules (
			source TEXT PRIMARY KEY,
			targets TEXT NOT NULL DEFAULT '[]',
			description TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			managed_source TEXT NOT NULL DEFAULT 'dashboard',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);
		INSERT INTO failover_rules (
			source, targets, description, enabled, managed_source, created_at, updated_at
		) VALUES (
			'gpt-4o',
			'["azure/gpt-4o","gemini/gemini-2.5-pro"]',
			'legacy note',
			1,
			'dashboard',
			100,
			200
		);
	`)
	if err != nil {
		t.Fatalf("create legacy failover_rules table: %v", err)
	}

	store, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	rows, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("List() returned %d rows, want 1", len(rows))
	}
	if rows[0].Source != "gpt-4o" {
		t.Fatalf("row source = %q, want gpt-4o", rows[0].Source)
	}
	wantTargets := []string{"azure/gpt-4o", "gemini/gemini-2.5-pro"}
	if !reflect.DeepEqual(rows[0].Targets, wantTargets) {
		t.Fatalf("row targets = %v, want %v", rows[0].Targets, wantTargets)
	}

	columns := sqliteColumnsForTest(t, db)
	for _, removed := range []string{"source", "targets", "description"} {
		if columns[removed] {
			t.Fatalf("legacy column %q still exists after migration", removed)
		}
	}
	for _, added := range []string{"primary_model", "fallback_models"} {
		if !columns[added] {
			t.Fatalf("column %q missing after migration", added)
		}
	}
}

func sqliteColumnsForTest(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info('failover_rules')`)
	if err != nil {
		t.Fatalf("PRAGMA table_info() error = %v", err)
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table_info row: %v", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows error = %v", err)
	}
	return columns
}
