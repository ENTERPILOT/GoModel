package authkeys

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestSQLiteAuthKeyLabelsRoundTrip(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite database: %v", err)
	}
	defer db.Close()

	store, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatalf("failed to create sqlite store: %v", err)
	}

	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	labelled := AuthKey{
		ID:            "key-labelled",
		Name:          "labelled",
		UserPath:      "/team/alpha",
		Labels:        []string{"team-a", "batch"},
		RedactedValue: TokenPrefix + "...abcd",
		SecretHash:    "hash-labelled",
		Enabled:       true,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	unlabelled := AuthKey{
		ID:            "key-unlabelled",
		Name:          "unlabelled",
		RedactedValue: TokenPrefix + "...efgh",
		SecretHash:    "hash-unlabelled",
		Enabled:       true,
		CreatedAt:     now.Add(-time.Hour),
		UpdatedAt:     now.Add(-time.Hour),
	}
	for _, key := range []AuthKey{labelled, unlabelled} {
		if err := store.Create(ctx, key); err != nil {
			t.Fatalf("Create(%s) error = %v", key.ID, err)
		}
	}

	// Reopening against the same database must tolerate the already-applied
	// labels migration.
	if _, err := NewSQLiteStore(db); err != nil {
		t.Fatalf("NewSQLiteStore() reopen error = %v", err)
	}

	keys, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("List() len = %d, want 2", len(keys))
	}
	byID := map[string]AuthKey{}
	for _, key := range keys {
		byID[key.ID] = key
	}
	if got := byID["key-labelled"].Labels; !reflect.DeepEqual(got, []string{"team-a", "batch"}) {
		t.Fatalf("labelled key labels = %v, want [team-a batch]", got)
	}
	if got := byID["key-unlabelled"].Labels; got != nil {
		t.Fatalf("unlabelled key labels = %v, want nil", got)
	}

	later := now.Add(time.Hour)
	if err := store.UpdateLabels(ctx, "key-unlabelled", []string{"added"}, later); err != nil {
		t.Fatalf("UpdateLabels() error = %v", err)
	}
	if err := store.UpdateLabels(ctx, "key-labelled", nil, later); err != nil {
		t.Fatalf("UpdateLabels(clear) error = %v", err)
	}
	if err := store.UpdateLabels(ctx, "missing", []string{"x"}, later); err != ErrNotFound {
		t.Fatalf("UpdateLabels(missing) error = %v, want %v", err, ErrNotFound)
	}

	keys, err = store.List(ctx)
	if err != nil {
		t.Fatalf("List() after update error = %v", err)
	}
	byID = map[string]AuthKey{}
	for _, key := range keys {
		byID[key.ID] = key
	}
	if got := byID["key-unlabelled"].Labels; !reflect.DeepEqual(got, []string{"added"}) {
		t.Fatalf("updated key labels = %v, want [added]", got)
	}
	if got := byID["key-unlabelled"].UpdatedAt; !got.Equal(later) {
		t.Fatalf("updated key UpdatedAt = %v, want %v", got, later)
	}
	if got := byID["key-labelled"].Labels; got != nil {
		t.Fatalf("cleared key labels = %v, want nil", got)
	}
}
