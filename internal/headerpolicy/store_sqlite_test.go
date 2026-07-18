package headerpolicy

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSQLiteStoreCRUDRoundTrip(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	store, err := NewSQLiteStore(t.Context(), db)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	definition := Definition{
		Name: "pin-beta", Description: "test", Methods: []string{"post"}, Paths: []string{"/v1/*"},
		When:    []Condition{{Header: "X-Empty", Equals: new("")}},
		Actions: []Action{{Action: ActionSet, Header: "Anthropic-Beta", Value: new("context-1m")}},
	}
	if err := store.Upsert(t.Context(), definition); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	got, err := store.Get(t.Context(), " pin-beta ")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Methods[0] != "POST" || got.Paths[0] != "/v1/*" || got.When[0].Equals == nil || *got.When[0].Equals != "" {
		t.Fatalf("Get() = %#v", got)
	}
	listed, err := store.List(context.Background())
	if err != nil || len(listed) != 1 {
		t.Fatalf("List() = %#v, %v", listed, err)
	}
	if err := store.Delete(t.Context(), "pin-beta"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Get(t.Context(), "pin-beta"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() after delete error = %v, want ErrNotFound", err)
	}
}

func TestSQLiteStoreUsesDedicatedTable(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()
	if _, err := NewSQLiteStore(t.Context(), db); err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='header_policy_definitions'`).Scan(&name); err != nil {
		t.Fatalf("dedicated table lookup error = %v", err)
	}
	if name != "header_policy_definitions" {
		t.Fatalf("table = %q", name)
	}
}
