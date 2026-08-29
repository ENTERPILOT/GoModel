package providers

import (
	"context"
	"errors"
	"testing"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

func runSQLCredentialStoreTest(t *testing.T, body func(t *testing.T, store *SQLCredentialStore)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLCredentialStore(context.Background(), db)
		if err != nil {
			t.Fatalf("NewSQLCredentialStore: %v", err)
		}
		body(t, store)
	})
}

func TestSQLCredentialStoreRoundTrip(t *testing.T) {
	runSQLCredentialStoreTest(t, func(t *testing.T, store *SQLCredentialStore) {
		ctx := context.Background()
		sessionStickyKeys := false

		cred := ManagedProviderCredential{
			Name:              "my-openai",
			Type:              "openai",
			APIKeys:           []string{"sk-one", "sk-two"},
			SessionStickyKeys: &sessionStickyKeys,
			BaseURL:           "https://api.openai.com/v1",
			APIVersion:        "2024-01-01",
			ProxyURL:          "socks5://user:secret@proxy.internal:1080",
			Models:            []string{"gpt-4o", "gpt-4o-mini"},
			Enabled:           true,
		}
		if err := store.Upsert(ctx, cred); err != nil {
			t.Fatalf("Upsert() error = %v", err)
		}

		got, err := store.Get(ctx, "my-openai")
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if got.Type != "openai" || got.BaseURL != cred.BaseURL || !got.Enabled {
			t.Fatalf("Get() = %+v, want round-tripped row", got)
		}
		if got.ProxyURL != cred.ProxyURL {
			t.Fatalf("Get().ProxyURL = %q, want %q", got.ProxyURL, cred.ProxyURL)
		}
		if len(got.APIKeys) != 2 || got.APIKeys[0] != "sk-one" || got.APIKeys[1] != "sk-two" {
			t.Fatalf("Get().APIKeys = %v, want [sk-one sk-two]", got.APIKeys)
		}
		if len(got.Models) != 2 || got.Models[0] != "gpt-4o" {
			t.Fatalf("Get().Models = %v, want [gpt-4o gpt-4o-mini]", got.Models)
		}
		if got.SessionStickyKeys == nil || *got.SessionStickyKeys {
			t.Fatalf("Get().SessionStickyKeys = %v, want false", got.SessionStickyKeys)
		}
		if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
			t.Fatalf("Get() timestamps = (%v, %v), want both stamped", got.CreatedAt, got.UpdatedAt)
		}

		// Upsert again with a changed field; CreatedAt must be preserved by the
		// caller passing it back (the store itself always stamps UpdatedAt).
		cred.BaseURL = "https://api.openai.com/v2"
		cred.CreatedAt = got.CreatedAt
		if err := store.Upsert(ctx, cred); err != nil {
			t.Fatalf("second Upsert() error = %v", err)
		}
		updated, err := store.Get(ctx, "my-openai")
		if err != nil {
			t.Fatalf("Get() after update error = %v", err)
		}
		if updated.BaseURL != "https://api.openai.com/v2" {
			t.Fatalf("updated.BaseURL = %q, want the new value", updated.BaseURL)
		}
		if !updated.CreatedAt.Equal(got.CreatedAt) {
			t.Fatalf("updated.CreatedAt = %v, want unchanged %v", updated.CreatedAt, got.CreatedAt)
		}

		list, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(list) != 1 || list[0].Name != "my-openai" {
			t.Fatalf("List() = %+v, want one row named my-openai", list)
		}

		if err := store.Delete(ctx, "my-openai"); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if _, err := store.Get(ctx, "my-openai"); !errors.Is(err, ErrCredentialNotFound) {
			t.Fatalf("Get() after delete error = %v, want ErrCredentialNotFound", err)
		}
		if err := store.Delete(ctx, "my-openai"); !errors.Is(err, ErrCredentialNotFound) {
			t.Fatalf("Delete() of already-deleted row error = %v, want ErrCredentialNotFound", err)
		}
	})
}

func TestSQLCredentialStoreGetMissing(t *testing.T) {
	runSQLCredentialStoreTest(t, func(t *testing.T, store *SQLCredentialStore) {
		ctx := context.Background()

		if _, err := store.Get(ctx, "missing"); !errors.Is(err, ErrCredentialNotFound) {
			t.Fatalf("Get() error = %v, want ErrCredentialNotFound", err)
		}
	})
}

func TestSQLCredentialStoreListOrdersByName(t *testing.T) {
	runSQLCredentialStoreTest(t, func(t *testing.T, store *SQLCredentialStore) {
		ctx := context.Background()

		for _, name := range []string{"zeta", "alpha", "mid"} {
			if err := store.Upsert(ctx, ManagedProviderCredential{Name: name, Type: "openai", Enabled: true}); err != nil {
				t.Fatalf("Upsert(%q) error = %v", name, err)
			}
		}

		list, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(list) != 3 {
			t.Fatalf("len(List()) = %d, want 3", len(list))
		}
		want := []string{"alpha", "mid", "zeta"}
		for i, w := range want {
			if list[i].Name != w {
				t.Fatalf("List()[%d].Name = %q, want %q", i, list[i].Name, w)
			}
		}
	})
}
