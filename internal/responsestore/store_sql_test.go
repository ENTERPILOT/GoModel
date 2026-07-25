package responsestore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func runSQLStoreTest(t *testing.T, body func(t *testing.T, store *SQLStore)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
}

// runStoreSuite exercises behaviour every Store implementation owes its
// callers, against each backend available in this environment.
func runStoreSuite(t *testing.T, body func(t *testing.T, store Store)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLStore(context.Background(), db)
		if err != nil {
			t.Fatalf("NewSQLStore: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		store, err := NewMongoDBStore(db)
		if err != nil {
			t.Fatalf("NewMongoDBStore: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		body(t, store)
	})
}

func testStoredResponse(id string) *StoredResponse {
	return &StoredResponse{
		Response: &core.ResponsesResponse{
			ID:     id,
			Object: "response",
			Model:  "gpt-test",
		},
		InputItems: []json.RawMessage{
			json.RawMessage(`{"role":"user","content":"hello"}`),
		},
		Provider:  "openai",
		UserPath:  "/team-a",
		RequestID: "req-1",
	}
}

func TestSQLStoreCreateGetRoundtrip(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()

		if err := store.Create(ctx, testStoredResponse("resp-1")); err != nil {
			t.Fatalf("create: %v", err)
		}

		got, err := store.Get(ctx, "resp-1")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.Response == nil || got.Response.ID != "resp-1" || got.Response.Model != "gpt-test" {
			t.Fatalf("response = %+v, want id resp-1 model gpt-test", got.Response)
		}
		if len(got.InputItems) != 1 || !strings.Contains(string(got.InputItems[0]), "hello") {
			t.Fatalf("input items = %v, want original item", got.InputItems)
		}
		if got.Provider != "openai" || got.UserPath != "/team-a" || got.RequestID != "req-1" {
			t.Fatalf("metadata = %+v, want provider/user path/request id preserved", got)
		}
		if got.StoredAt.IsZero() {
			t.Fatal("StoredAt not stamped")
		}
		if got.ExpiresAt.IsZero() || !got.ExpiresAt.After(got.StoredAt) {
			t.Fatalf("ExpiresAt = %v, want after StoredAt %v", got.ExpiresAt, got.StoredAt)
		}
	})
}

func TestSQLStoreCreateRejectsDuplicates(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()

		if err := store.Create(ctx, testStoredResponse("resp-1")); err != nil {
			t.Fatalf("create: %v", err)
		}
		err := store.Create(ctx, testStoredResponse("resp-1"))
		if err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("duplicate create err = %v, want already exists", err)
		}
	})
}

func TestSQLStoreCreateReplacesExpired(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()

		expired := testStoredResponse("resp-1")
		expired.StoredAt = time.Now().UTC().Add(-2 * time.Hour)
		expired.ExpiresAt = time.Now().UTC().Add(-time.Hour)
		// Expired-at-write snapshots are silently skipped, so seed the row directly.
		if _, err := store.db.Exec(ctx,
			"INSERT INTO response_snapshots (id, data, stored_at, expires_at) VALUES (?, ?, ?, ?)",
			"resp-1", `{"response":{"id":"resp-1"}}`, expired.StoredAt.Unix(), expired.ExpiresAt.Unix(),
		); err != nil {
			t.Fatalf("seed expired row: %v", err)
		}

		replacement := testStoredResponse("resp-1")
		replacement.Response.Model = "gpt-replacement"
		if err := store.Create(ctx, replacement); err != nil {
			t.Fatalf("create over expired: %v", err)
		}
		got, err := store.Get(ctx, "resp-1")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.Response.Model != "gpt-replacement" {
			t.Fatalf("model = %q, want gpt-replacement", got.Response.Model)
		}
	})
}

func TestSQLStoreUpdatePreservesRetentionColumns(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()

		if err := store.Create(ctx, testStoredResponse("resp-1")); err != nil {
			t.Fatalf("create: %v", err)
		}
		created, err := store.Get(ctx, "resp-1")
		if err != nil {
			t.Fatalf("get created: %v", err)
		}

		updated := testStoredResponse("resp-1")
		updated.Response.Model = "gpt-updated"
		if err := store.Update(ctx, updated); err != nil {
			t.Fatalf("update: %v", err)
		}

		got, err := store.Get(ctx, "resp-1")
		if err != nil {
			t.Fatalf("get updated: %v", err)
		}
		if got.Response.Model != "gpt-updated" {
			t.Fatalf("model = %q, want gpt-updated", got.Response.Model)
		}
		if !got.StoredAt.Equal(created.StoredAt) || !got.ExpiresAt.Equal(created.ExpiresAt) {
			t.Fatalf("retention changed: stored %v→%v expires %v→%v",
				created.StoredAt, got.StoredAt, created.ExpiresAt, got.ExpiresAt)
		}
	})
}

func TestSQLStoreUpdateMissingReturnsNotFound(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		if err := store.Update(context.Background(), testStoredResponse("missing")); !errors.Is(err, ErrNotFound) {
			t.Fatalf("update missing err = %v, want ErrNotFound", err)
		}
	})
}

func TestStoreDelete(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store Store) {
		ctx := context.Background()

		if err := store.Create(ctx, testStoredResponse("resp-1")); err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := store.Delete(ctx, "resp-1"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, err := store.Get(ctx, "resp-1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get after delete err = %v, want ErrNotFound", err)
		}
		if err := store.Delete(ctx, "resp-1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second delete err = %v, want ErrNotFound", err)
		}
	})
}

func TestSQLStoreExpiryAndSweep(t *testing.T) {
	runSQLStoreTest(t, func(t *testing.T, store *SQLStore) {
		ctx := context.Background()

		entry := testStoredResponse("resp-1")
		entry.ExpiresAt = time.Now().UTC().Add(time.Second)
		if err := store.Create(ctx, entry); err != nil {
			t.Fatalf("create: %v", err)
		}

		// Simulate expiry passing by rewriting the retention column.
		if _, err := store.db.Exec(ctx,
			"UPDATE response_snapshots SET expires_at = ? WHERE id = ?",
			time.Now().Add(-time.Minute).Unix(), "resp-1",
		); err != nil {
			t.Fatalf("expire row: %v", err)
		}

		if _, err := store.Get(ctx, "resp-1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get expired err = %v, want ErrNotFound", err)
		}
		if err := store.DeleteExpired(ctx); err != nil {
			t.Fatalf("delete expired: %v", err)
		}
		var count int
		if err := store.db.QueryRow(ctx, "SELECT COUNT(*) FROM response_snapshots").Scan(&count); err != nil {
			t.Fatalf("count rows: %v", err)
		}
		if count != 0 {
			t.Fatalf("rows after sweep = %d, want 0", count)
		}
	})
}
