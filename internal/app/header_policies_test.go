package app

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/enterpilot/gomodel/internal/guardrails"
	"github.com/enterpilot/gomodel/internal/headerpolicy"

	_ "modernc.org/sqlite"
)

func TestMigrateLegacyHeaderPoliciesMovesAndDeletesRows(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	legacy, err := guardrails.NewSQLiteStore(t.Context(), db)
	if err != nil {
		t.Fatalf("guardrails.NewSQLiteStore() error = %v", err)
	}
	target, err := headerpolicy.NewSQLiteStore(t.Context(), db)
	if err != nil {
		t.Fatalf("headerpolicy.NewSQLiteStore() error = %v", err)
	}
	if err := legacy.Upsert(t.Context(), guardrails.Definition{
		Name: "pin-beta", Type: "header_modification", Description: "legacy",
		Config: []byte(`{"endpoints":["/v1/*"],"actions":[{"action":"set","header":"Anthropic-Beta","value":"context-1m"}]}`),
	}); err != nil {
		t.Fatalf("legacy.Upsert() error = %v", err)
	}
	count, err := migrateLegacyHeaderPolicies(t.Context(), legacy, target)
	if err != nil {
		t.Fatalf("migrateLegacyHeaderPolicies() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("migrated count = %d, want 1", count)
	}
	got, err := target.Get(t.Context(), "pin-beta")
	if err != nil {
		t.Fatalf("target.Get() error = %v", err)
	}
	if got.Description != "legacy" || got.Paths[0] != "/v1/*" {
		t.Fatalf("migrated definition = %#v", got)
	}
	if _, err := legacy.Get(t.Context(), "pin-beta"); !errors.Is(err, guardrails.ErrNotFound) {
		t.Fatalf("legacy.Get() error = %v, want ErrNotFound", err)
	}
}

func TestMigrateLegacyHeaderPoliciesPreservesDedicatedRow(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	legacy, _ := guardrails.NewSQLiteStore(t.Context(), db)
	target, _ := headerpolicy.NewSQLiteStore(t.Context(), db)
	if err := target.Upsert(t.Context(), headerpolicy.Definition{
		Name: "headers", Description: "dedicated",
		Actions: []headerpolicy.Action{{Action: headerpolicy.ActionRemove, Header: "X-Debug"}},
	}); err != nil {
		t.Fatalf("target.Upsert() error = %v", err)
	}
	if err := legacy.Upsert(t.Context(), guardrails.Definition{
		Name: "headers", Type: "header_modification", Description: "stale",
		Config: []byte(`{"actions":[{"action":"remove","header":"X-Legacy"}]}`),
	}); err != nil {
		t.Fatalf("legacy.Upsert() error = %v", err)
	}
	count, err := migrateLegacyHeaderPolicies(t.Context(), legacy, target)
	if err != nil || count != 0 {
		t.Fatalf("migration = %d, %v", count, err)
	}
	got, err := target.Get(t.Context(), "headers")
	if err != nil || got.Description != "dedicated" {
		t.Fatalf("dedicated row = %#v, %v", got, err)
	}
}
