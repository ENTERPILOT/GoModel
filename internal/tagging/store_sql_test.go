package tagging

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
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

// runStoreSuite exercises behaviour every Store implementation owes its
// callers, against each backend available in this environment.
func runStoreSuite(t *testing.T, body func(t *testing.T, store Store)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store := newTestStore(t, db)
		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, err := NewMongoDBStore(context.Background(), db)
		if err != nil {
			t.Fatalf("NewMongoDBStore: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
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
