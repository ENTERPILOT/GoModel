package runtimesettings

import (
	"context"
	"sync"
	"testing"

	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
)

func newSQLiteStore(t *testing.T) *SQLStore {
	t.Helper()
	backend := newTestStorage(t)
	store, err := storage.ResolveSQLBackend[*SQLStore](context.Background(), backend,
		func(db sqlx.DB) (*SQLStore, error) { return NewSQLStore(context.Background(), db) },
		nil,
	)
	if err != nil {
		t.Fatalf("create SQL store: %v", err)
	}
	return store
}

func TestSQLStoreSetDefaultKeepsFirstValue(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteStore(t)

	if stored, err := store.SetDefault(ctx, "install_id", "first"); err != nil || stored != "first" {
		t.Fatalf("SetDefault on a new key = %q, %v; want the given value", stored, err)
	}
	if stored, err := store.SetDefault(ctx, "install_id", "second"); err != nil || stored != "first" {
		t.Fatalf("SetDefault on an existing key = %q, %v; want the first value", stored, err)
	}
	if err := store.Set(ctx, "install_id", "third"); err != nil {
		t.Fatal(err)
	}
	if stored, err := store.SetDefault(ctx, "install_id", "fourth"); err != nil || stored != "third" {
		t.Fatalf("SetDefault after Set = %q, %v; want the set value", stored, err)
	}
}

func TestSQLStoreSetDefaultConvergesConcurrentWriters(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteStore(t)

	const writers = 8
	results := make([]string, writers)
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			stored, err := store.SetDefault(ctx, "install_id", string(rune('a'+w)))
			if err != nil {
				t.Errorf("writer %d: %v", w, err)
			}
			results[w] = stored
		})
	}
	wg.Wait()

	winner, found, err := store.Get(ctx, "install_id")
	if err != nil || !found {
		t.Fatalf("Get after concurrent SetDefault: found=%v err=%v", found, err)
	}
	for w, got := range results {
		if got != winner {
			t.Errorf("writer %d got %q, database holds %q", w, got, winner)
		}
	}
}
