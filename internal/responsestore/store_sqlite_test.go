package responsestore

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/storage"
)

func newSQLiteTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	st, err := storage.NewSQLite(storage.SQLiteConfig{Path: filepath.Join(t.TempDir(), "responses.db")})
	if err != nil {
		t.Fatalf("new sqlite storage: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	store, err := NewSQLiteStore(st.DB())
	if err != nil {
		t.Fatalf("new sqlite response store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
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

func TestSQLiteStoreCreateGetRoundtrip(t *testing.T) {
	store := newSQLiteTestStore(t)
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
}

func TestSQLiteStoreCreateRejectsDuplicates(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()

	if err := store.Create(ctx, testStoredResponse("resp-1")); err != nil {
		t.Fatalf("create: %v", err)
	}
	err := store.Create(ctx, testStoredResponse("resp-1"))
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate create err = %v, want already exists", err)
	}
}

func TestSQLiteStoreCreateReplacesExpired(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()

	expired := testStoredResponse("resp-1")
	expired.StoredAt = time.Now().UTC().Add(-2 * time.Hour)
	expired.ExpiresAt = time.Now().UTC().Add(-time.Hour)
	// Expired-at-write snapshots are silently skipped, so seed the row directly.
	if _, err := store.db.Exec(
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
}

func TestSQLiteStoreUpdatePreservesRetentionColumns(t *testing.T) {
	store := newSQLiteTestStore(t)
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
}

func TestSQLiteStoreUpdateMissingReturnsNotFound(t *testing.T) {
	store := newSQLiteTestStore(t)
	if err := store.Update(context.Background(), testStoredResponse("missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing err = %v, want ErrNotFound", err)
	}
}

func TestSQLiteStoreDelete(t *testing.T) {
	store := newSQLiteTestStore(t)
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
}

func TestSQLiteStoreExpiryAndSweep(t *testing.T) {
	store := newSQLiteTestStore(t)
	ctx := context.Background()

	entry := testStoredResponse("resp-1")
	entry.ExpiresAt = time.Now().UTC().Add(time.Second)
	if err := store.Create(ctx, entry); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Simulate expiry passing by rewriting the retention column.
	if _, err := store.db.Exec(
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
	if err := store.db.QueryRow("SELECT COUNT(*) FROM response_snapshots").Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("rows after sweep = %d, want 0", count)
	}
}
