package providers

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
	"github.com/stretchr/testify/require"
)

func runSQLCredentialStoreTest(t *testing.T, body func(t *testing.T, store *SQLCredentialStore)) {
	t.Helper()
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		store, err := NewSQLCredentialStore(context.Background(), db)
		require.NoError(t, err)

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
			Models:            []string{"gpt-4o", "gpt-4o-mini"},
			Enabled:           true,
		}
		err := store.Upsert(ctx, cred)
		require.NoError(t, err)

		got, err := store.Get(ctx, "my-openai")
		require.NoError(t, err)
		require.Equal(t, "openai", got.Type)
		require.Equal(t, cred.BaseURL, got.BaseURL)
		require.True(t, got.Enabled, "Get() = %+v, want round-tripped row", got)
		require.Len(t, got.APIKeys, 2)
		require.Equal(t, "sk-one", got.APIKeys[0])
		require.Equal(t, "sk-two", got.APIKeys[1])
		require.Len(t, got.Models, 2)
		require.Equal(t, "gpt-4o", got.Models[0])
		require.NotNil(t, got.SessionStickyKeys)
		require.False(t, *got.SessionStickyKeys)
		require.False(t, got.CreatedAt.IsZero())
		require.False(t, got.UpdatedAt.IsZero(), "Get() timestamps = (%v, %v), want both stamped", got.CreatedAt, got.UpdatedAt)

		// Upsert again with a changed field; CreatedAt must be preserved by the
		// caller passing it back (the store itself always stamps UpdatedAt).
		cred.BaseURL = "https://api.openai.com/v2"
		cred.CreatedAt = got.CreatedAt
		err = store.Upsert(ctx, cred)
		require.NoError(t, err)

		updated, err := store.Get(ctx, "my-openai")
		require.NoError(t, err)
		require.Equal(t, "https://api.openai.com/v2", updated.BaseURL)
		require.True(t, updated.CreatedAt.Equal(got.CreatedAt), "updated.CreatedAt = %v, want unchanged %v", updated.CreatedAt, got.CreatedAt)

		list, err := store.List(ctx)
		require.NoError(t, err)
		require.Len(t, list, 1)
		require.Equal(t, "my-openai", list[0].Name)
		err = store.Delete(ctx, "my-openai")
		require.NoError(t, err)
		_, err = store.Get(ctx, "my-openai")
		require.ErrorIs(t, err, ErrCredentialNotFound)
		err = store.Delete(ctx, "my-openai")
		require.ErrorIs(t, err, ErrCredentialNotFound)
	})
}

func TestSQLCredentialStoreGetMissing(t *testing.T) {
	runSQLCredentialStoreTest(t, func(t *testing.T, store *SQLCredentialStore) {
		ctx := context.Background()
		_, err := store.Get(ctx, "missing")
		require.ErrorIs(t, err, ErrCredentialNotFound)
	})
}

func TestSQLCredentialStoreListOrdersByName(t *testing.T) {
	runSQLCredentialStoreTest(t, func(t *testing.T, store *SQLCredentialStore) {
		ctx := context.Background()

		for _, name := range []string{"zeta", "alpha", "mid"} {
			err := store.Upsert(ctx, ManagedProviderCredential{Name: name, Type: "openai", Enabled: true})
			require.NoError(t, err)
		}

		list, err := store.List(ctx)
		require.NoError(t, err)
		require.Len(t, list, 3)

		want := []string{"alpha", "mid", "zeta"}
		for i, w := range want {
			require.Equal(t, w, list[i].Name)
		}
	})
}
