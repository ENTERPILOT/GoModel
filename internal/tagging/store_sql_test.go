package tagging

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func newTestStore(t *testing.T, db sqlx.DB) *SQLStore {
	t.Helper()
	store, err := NewSQLStore(context.Background(), db)
	if err != nil {
		t.Fatalf("NewSQLStore: %v", err)
	}
	return store
}

func TestSQLStoreRoundTrip(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		store := newTestStore(t, db)

		want := []Rule{
			{Header: "X-Team", Prefix: "team-", Delimiter: ","},
			{Header: "X-Env", DoNotPass: true, Delimiter: "|"},
		}
		if err := store.SaveRules(ctx, want); err != nil {
			t.Fatalf("SaveRules: %v", err)
		}

		got, err := store.GetRules(ctx)
		if err != nil {
			t.Fatalf("GetRules: %v", err)
		}
		if len(got) != len(want) {
			t.Fatalf("got %d rules, want %d", len(got), len(want))
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("rule %d = %+v, want %+v", i, got[i], want[i])
			}
		}
	})
}

func TestSQLStoreGetRulesEmptyWhenUnset(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store := newTestStore(t, db)

		// A store with nothing saved must read as "no operator rules", not as
		// an error: it is the state of every fresh deployment.
		got, err := store.GetRules(context.Background())
		if err != nil {
			t.Fatalf("GetRules: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d rules, want none", len(got))
		}
	})
}

func TestSQLStoreSaveReplacesPreviousRules(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		store := newTestStore(t, db)

		if err := store.SaveRules(ctx, []Rule{{Header: "X-One"}, {Header: "X-Two"}}); err != nil {
			t.Fatalf("first SaveRules: %v", err)
		}
		// SaveRules replaces the whole set rather than merging, so a shorter
		// second save must not leave the dropped rule behind.
		if err := store.SaveRules(ctx, []Rule{{Header: "X-Three"}}); err != nil {
			t.Fatalf("second SaveRules: %v", err)
		}

		got, err := store.GetRules(ctx)
		if err != nil {
			t.Fatalf("GetRules: %v", err)
		}
		if len(got) != 1 || got[0].Header != "X-Three" {
			t.Errorf("got %+v, want only X-Three", got)
		}
	})
}

func TestSQLStoreSaveEmptyClearsRules(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		store := newTestStore(t, db)

		if err := store.SaveRules(ctx, []Rule{{Header: "X-One"}}); err != nil {
			t.Fatalf("SaveRules: %v", err)
		}
		if err := store.SaveRules(ctx, nil); err != nil {
			t.Fatalf("SaveRules(nil): %v", err)
		}

		got, err := store.GetRules(ctx)
		if err != nil {
			t.Fatalf("GetRules: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %+v, want none", got)
		}
	})
}

func TestNewSQLStoreIsIdempotent(t *testing.T) {
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		ctx := context.Background()
		store := newTestStore(t, db)
		if err := store.SaveRules(ctx, []Rule{{Header: "X-Keep"}}); err != nil {
			t.Fatalf("SaveRules: %v", err)
		}

		// Constructing again is what every restart does; it must neither fail
		// nor discard the saved rules.
		second := newTestStore(t, db)
		got, err := second.GetRules(ctx)
		if err != nil {
			t.Fatalf("GetRules: %v", err)
		}
		if len(got) != 1 || got[0].Header != "X-Keep" {
			t.Errorf("got %+v, want X-Keep preserved", got)
		}
	})
}

func TestNewSQLStoreRejectsNilDB(t *testing.T) {
	if _, err := NewSQLStore(context.Background(), nil); err == nil {
		t.Fatal("NewSQLStore(nil) = nil error, want failure")
	}
}
