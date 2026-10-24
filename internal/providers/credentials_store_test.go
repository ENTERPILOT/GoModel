package providers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCredentialStore_RoundTrip(t *testing.T) {
	runCredentialStoreSuite(t, func(t *testing.T, store CredentialStore) {
		ctx := context.Background()
		sessionStickyKeys := false

		cred := ManagedProviderCredential{
			Name:                     "my-vertex",
			Type:                     "gemini",
			APIKeys:                  []string{"sk-one", "sk-two"},
			SessionStickyKeys:        &sessionStickyKeys,
			BaseURL:                  "https://api.example.com/v1",
			APIVersion:               "2024-01-01",
			Backend:                  "vertex",
			AuthType:                 "service_account",
			APIMode:                  "responses",
			VertexProject:            "proj",
			VertexLocation:           "us-central1",
			ServiceAccountFile:       "/etc/sa.json",
			ServiceAccountJSON:       `{"type":"service_account"}`,
			ServiceAccountJSONBase64: "eyJ0eXBlIjoic2VydmljZV9hY2NvdW50In0=",
			GCPScope:                 "https://www.googleapis.com/auth/cloud-platform",
			ProxyURL:                 "socks5://user:pass@proxy.internal:1080",
			Models:                   []string{"gemini-2.5-pro", "gemini-2.5-flash"},
			Enabled:                  true,
		}
		require.NoError(t, store.Upsert(ctx, cred))

		got, err := store.Get(ctx, "my-vertex")
		require.NoError(t, err)
		// Secrets come back raw: the store is the only place they live and the
		// admin API redacts them on the way out, not on the way in.
		require.Equal(t, []string{"sk-one", "sk-two"}, got.APIKeys)
		require.Equal(t, cred.ServiceAccountJSON, got.ServiceAccountJSON)
		require.Equal(t, cred.ServiceAccountJSONBase64, got.ServiceAccountJSONBase64)
		require.NotNil(t, got.SessionStickyKeys)
		require.False(t, *got.SessionStickyKeys)
		require.False(t, got.CreatedAt.IsZero())
		require.False(t, got.UpdatedAt.IsZero(), "Get() timestamps = (%v, %v), want both stamped", got.CreatedAt, got.UpdatedAt)

		want := cred
		want.CreatedAt, want.UpdatedAt = got.CreatedAt, got.UpdatedAt
		require.Equal(t, want, *got)

		// Upsert again with a changed field; CreatedAt must be preserved by the
		// caller passing it back (the store itself always stamps UpdatedAt).
		cred.BaseURL = "https://api.example.com/v2"
		cred.CreatedAt = got.CreatedAt
		require.NoError(t, store.Upsert(ctx, cred))

		updated, err := store.Get(ctx, "my-vertex")
		require.NoError(t, err)
		require.Equal(t, "https://api.example.com/v2", updated.BaseURL)
		require.True(t, updated.CreatedAt.Equal(got.CreatedAt), "updated.CreatedAt = %v, want unchanged %v", updated.CreatedAt, got.CreatedAt)
		require.False(t, updated.UpdatedAt.Before(got.UpdatedAt), "updated.UpdatedAt = %v, want >= %v", updated.UpdatedAt, got.UpdatedAt)

		list, err := store.List(ctx)
		require.NoError(t, err)
		require.Len(t, list, 1)
		require.Equal(t, *updated, list[0])

		require.NoError(t, store.Delete(ctx, "my-vertex"))
		_, err = store.Get(ctx, "my-vertex")
		require.ErrorIs(t, err, ErrCredentialNotFound)
		require.ErrorIs(t, store.Delete(ctx, "my-vertex"), ErrCredentialNotFound)

		list, err = store.List(ctx)
		require.NoError(t, err)
		require.NotNil(t, list)
		require.Empty(t, list)
	})
}

func TestCredentialStore_Defaults(t *testing.T) {
	runCredentialStoreSuite(t, func(t *testing.T, store CredentialStore) {
		ctx := context.Background()

		// A keyless provider with nothing but a name and type.
		require.NoError(t, store.Upsert(ctx, ManagedProviderCredential{Name: "ollama", Type: "ollama"}))

		got, err := store.Get(ctx, "ollama")
		require.NoError(t, err)
		require.Nil(t, got.APIKeys)
		require.Nil(t, got.Models)
		require.NotNil(t, got.SessionStickyKeys, "nil SessionStickyKeys means enabled and is stored as such")
		require.True(t, *got.SessionStickyKeys)
		require.False(t, got.Enabled)
		require.Empty(t, got.BaseURL)

		// Empty slices read back as nil, like never set.
		require.NoError(t, store.Upsert(ctx, ManagedProviderCredential{Name: "ollama", Type: "ollama", APIKeys: []string{}, Models: []string{}}))
		got, err = store.Get(ctx, "ollama")
		require.NoError(t, err)
		require.Nil(t, got.APIKeys)
		require.Nil(t, got.Models)
	})
}

func TestCredentialStore_UpsertClearsLists(t *testing.T) {
	runCredentialStoreSuite(t, func(t *testing.T, store CredentialStore) {
		ctx := context.Background()
		require.NoError(t, store.Upsert(ctx, ManagedProviderCredential{
			Name:    "my-openai",
			Type:    "openai",
			APIKeys: []string{"sk-one"},
			Models:  []string{"gpt-4o"},
			Enabled: true,
		}))

		// Upsert replaces the row rather than merging: dropping the lists must
		// not leave the previous values behind.
		require.NoError(t, store.Upsert(ctx, ManagedProviderCredential{Name: "my-openai", Type: "openai", Enabled: false}))

		got, err := store.Get(ctx, "my-openai")
		require.NoError(t, err)
		require.Nil(t, got.APIKeys)
		require.Nil(t, got.Models)
		require.False(t, got.Enabled)
	})
}

func TestCredentialStore_NameIsTrimmed(t *testing.T) {
	runCredentialStoreSuite(t, func(t *testing.T, store CredentialStore) {
		ctx := context.Background()
		require.NoError(t, store.Upsert(ctx, ManagedProviderCredential{Name: "  padded ", Type: "openai", Enabled: true}))

		got, err := store.Get(ctx, "padded")
		require.NoError(t, err)
		require.Equal(t, "padded", got.Name)
		got, err = store.Get(ctx, " padded  ")
		require.NoError(t, err)
		require.Equal(t, "padded", got.Name)

		require.NoError(t, store.Delete(ctx, " padded "))
		_, err = store.Get(ctx, "padded")
		require.ErrorIs(t, err, ErrCredentialNotFound)
	})
}

func TestCredentialStore_GetMissing(t *testing.T) {
	runCredentialStoreSuite(t, func(t *testing.T, store CredentialStore) {
		_, err := store.Get(context.Background(), "missing")
		require.ErrorIs(t, err, ErrCredentialNotFound)
	})
}

func TestCredentialStore_ListOrdersByName(t *testing.T) {
	runCredentialStoreSuite(t, func(t *testing.T, store CredentialStore) {
		ctx := context.Background()

		for _, name := range []string{"zeta", "alpha", "mid"} {
			require.NoError(t, store.Upsert(ctx, ManagedProviderCredential{Name: name, Type: "openai", Enabled: true}))
		}

		list, err := store.List(ctx)
		require.NoError(t, err)
		require.Len(t, list, 3)

		names := make([]string, 0, len(list))
		for _, cred := range list {
			names = append(names, cred.Name)
		}
		require.Equal(t, []string{"alpha", "mid", "zeta"}, names)
	})
}
