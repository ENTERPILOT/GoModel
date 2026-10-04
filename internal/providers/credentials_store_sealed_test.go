package providers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/encryption"
	"github.com/enterpilot/gomodel/internal/encryption/encryptiontest"
)

func sealedTestCredential() ManagedProviderCredential {
	return ManagedProviderCredential{
		Name:                     "my-vertex",
		Type:                     "gemini",
		APIKeys:                  []string{"sk-one", "sk-two"},
		BaseURL:                  "https://api.example.com/v1",
		ServiceAccountJSON:       `{"type":"service_account"}`,
		ServiceAccountJSONBase64: "eyJ0eXBlIjoic2VydmljZV9hY2NvdW50In0=",
		ProxyURL:                 "socks5://user:pass@proxy.internal:1080",
		Enabled:                  true,
	}
}

func TestSealedCredentialStoreEncryptsSecretFields(t *testing.T) {
	runCredentialStoreSuite(t, func(t *testing.T, raw CredentialStore) {
		ctx := context.Background()
		box, _ := encryptiontest.NewBox(t)
		store := sealCredentialStore(raw, box)

		cred := sealedTestCredential()
		require.NoError(t, store.Upsert(ctx, cred))
		assert.Equal(t, []string{"sk-one", "sk-two"}, cred.APIKeys, "the caller's slice is not overwritten with ciphertext")

		stored, err := raw.Get(ctx, "my-vertex")
		require.NoError(t, err)
		for _, value := range append([]string{stored.ServiceAccountJSON, stored.ServiceAccountJSONBase64, stored.ProxyURL}, stored.APIKeys...) {
			assert.True(t, encryption.IsSealed(value), "stored value %q is not sealed", value)
		}
		assert.Equal(t, cred.BaseURL, stored.BaseURL, "non-secret fields stay readable")

		got, err := store.Get(ctx, "my-vertex")
		require.NoError(t, err)
		assert.Equal(t, cred.APIKeys, got.APIKeys)
		assert.Equal(t, cred.ServiceAccountJSON, got.ServiceAccountJSON)
		assert.Equal(t, cred.ServiceAccountJSONBase64, got.ServiceAccountJSONBase64)
		assert.Equal(t, cred.ProxyURL, got.ProxyURL)

		list, err := store.List(ctx)
		require.NoError(t, err)
		require.Len(t, list, 1)
		assert.Equal(t, *got, list[0])
	})
}

func TestSealedCredentialStoreRejectsMovedCiphertext(t *testing.T) {
	runCredentialStoreSuite(t, func(t *testing.T, raw CredentialStore) {
		ctx := context.Background()
		box, _ := encryptiontest.NewBox(t)
		store := sealCredentialStore(raw, box)

		require.NoError(t, store.Upsert(ctx, sealedTestCredential()))
		stolen, err := raw.Get(ctx, "my-vertex")
		require.NoError(t, err)

		// Copy row A's sealed key into row B, as someone with database access
		// might to make B use A's credential.
		require.NoError(t, raw.Upsert(ctx, ManagedProviderCredential{Name: "other", Type: "openai", APIKeys: stolen.APIKeys[:1], Enabled: true}))
		_, err = store.Get(ctx, "other")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `provider_credential "other" field api_keys`)
		assert.NotContains(t, err.Error(), "sk-one")

		// Within a row, a key moved into another field fails as well.
		stolen.ProxyURL = stolen.APIKeys[0]
		require.NoError(t, raw.Upsert(ctx, *stolen))
		_, err = store.Get(ctx, "my-vertex")
		require.Error(t, err)
	})
}

func TestSealedCredentialStoreReadsPlaintextRows(t *testing.T) {
	runCredentialStoreSuite(t, func(t *testing.T, raw CredentialStore) {
		ctx := context.Background()
		require.NoError(t, raw.Upsert(ctx, sealedTestCredential()))

		box, _ := encryptiontest.NewBox(t)
		store := sealCredentialStore(raw, box)
		got, err := store.Get(ctx, "my-vertex")
		require.NoError(t, err)
		assert.Equal(t, []string{"sk-one", "sk-two"}, got.APIKeys, "rows written before encryption stay readable")

		require.NoError(t, store.Upsert(ctx, *got))
		stored, err := raw.Get(ctx, "my-vertex")
		require.NoError(t, err)
		assert.True(t, encryption.IsSealed(stored.APIKeys[0]), "the next write seals them")
	})
}

func TestSealedCredentialStoreWithoutKey(t *testing.T) {
	runCredentialStoreSuite(t, func(t *testing.T, raw CredentialStore) {
		ctx := context.Background()
		store := sealCredentialStore(raw, encryption.Disabled())
		require.NoError(t, store.Upsert(ctx, sealedTestCredential()))

		stored, err := raw.Get(ctx, "my-vertex")
		require.NoError(t, err)
		assert.Equal(t, []string{"sk-one", "sk-two"}, stored.APIKeys, "without a key, behaviour is unchanged")
	})
}

func TestReencryptCredentials(t *testing.T) {
	runCredentialStoreSuite(t, func(t *testing.T, raw CredentialStore) {
		ctx := context.Background()
		box, keys := encryptiontest.NewBox(t)

		require.NoError(t, raw.Upsert(ctx, ManagedProviderCredential{Name: "plain", Type: "openai", APIKeys: []string{"sk-plain"}, Enabled: true}))
		require.NoError(t, sealCredentialStore(raw, box).Upsert(ctx, ManagedProviderCredential{Name: "old-key", Type: "openai", APIKeys: []string{"sk-old"}, Enabled: true}))
		require.NoError(t, raw.Upsert(ctx, ManagedProviderCredential{Name: "keyless", Type: "ollama", Enabled: true}))

		rotated := encryptiontest.Rotate(t, keys)
		sealed := &sealedCredentialStore{CredentialStore: raw, box: rotated}

		report, err := sealed.reencrypt(ctx, raw.(credentialSwapper))
		require.NoError(t, err)
		assert.Equal(t, encryption.Report{Entity: "provider_credentials", Rows: 3, Reencrypted: 2}, report)

		for name, want := range map[string]string{"plain": "sk-plain", "old-key": "sk-old"} {
			stored, err := raw.Get(ctx, name)
			require.NoError(t, err)
			assert.True(t, rotated.IsCurrent(stored.APIKeys[0]), "%s is sealed with the active key", name)
			got, err := sealed.Get(ctx, name)
			require.NoError(t, err)
			assert.Equal(t, []string{want}, got.APIKeys)
		}

		again, err := sealed.reencrypt(ctx, raw.(credentialSwapper))
		require.NoError(t, err)
		assert.Equal(t, 0, again.Reencrypted, "a second pass rewrites nothing")
	})
}

// editAfterGet runs edit once, right after the first Get returns: an admin
// change landing between a re-encryption pass's read and its write.
type editAfterGet struct {
	CredentialStore
	edit func()
	done *bool
}

func (s editAfterGet) Get(ctx context.Context, name string) (*ManagedProviderCredential, error) {
	cred, err := s.CredentialStore.Get(ctx, name)
	if !*s.done {
		*s.done = true
		s.edit()
	}
	return cred, err
}

func TestReencryptCredentialsNeverOverwritesConcurrentEdits(t *testing.T) {
	tests := []struct {
		name       string
		edit       func(t *testing.T, raw CredentialStore)
		want       []string
		wantReport encryption.Report
	}{
		{
			name: "edited",
			edit: func(t *testing.T, raw CredentialStore) {
				require.NoError(t, raw.Upsert(context.Background(), ManagedProviderCredential{Name: "row", Type: "openai", APIKeys: []string{"sk-new"}, Enabled: false}))
			},
			want:       []string{"sk-new"},
			wantReport: encryption.Report{Entity: "provider_credentials", Rows: 1, Reencrypted: 1},
		},
		{
			name: "deleted",
			edit: func(t *testing.T, raw CredentialStore) {
				require.NoError(t, raw.Delete(context.Background(), "row"))
			},
			wantReport: encryption.Report{Entity: "provider_credentials", Rows: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runCredentialStoreSuite(t, func(t *testing.T, raw CredentialStore) {
				ctx := context.Background()
				box, _ := encryptiontest.NewBox(t)
				require.NoError(t, raw.Upsert(ctx, ManagedProviderCredential{Name: "row", Type: "openai", APIKeys: []string{"sk-old"}, Enabled: true}))

				done := false
				store := editAfterGet{CredentialStore: raw, done: &done, edit: func() { tt.edit(t, raw) }}
				sealed := &sealedCredentialStore{CredentialStore: store, box: box}
				report, err := sealed.reencrypt(ctx, raw.(credentialSwapper))
				require.NoError(t, err)
				assert.Equal(t, tt.wantReport, report)

				got, err := sealCredentialStore(raw, box).Get(ctx, "row")
				if tt.want == nil {
					require.ErrorIs(t, err, ErrCredentialNotFound, "a deleted row is not recreated")
					return
				}
				require.NoError(t, err)
				assert.Equal(t, tt.want, got.APIKeys, "the edit is not overwritten with the earlier read")
				assert.False(t, got.Enabled)
				stored, err := raw.Get(ctx, "row")
				require.NoError(t, err)
				assert.True(t, box.IsCurrent(stored.APIKeys[0]), "the edited value is encrypted on the retry")
			})
		})
	}
}

func TestSwapSecretsLeavesOtherColumnsAlone(t *testing.T) {
	runCredentialStoreSuite(t, func(t *testing.T, raw CredentialStore) {
		ctx := context.Background()
		require.NoError(t, raw.Upsert(ctx, ManagedProviderCredential{Name: "row", Type: "openai", APIKeys: []string{"a"}, BaseURL: "https://one", Enabled: true}))
		current, err := raw.Get(ctx, "row")
		require.NoError(t, err)

		swap := raw.(credentialSwapper)
		next := *current
		next.APIKeys, next.BaseURL = []string{"b"}, "https://ignored"
		swapped, err := swap.swapSecrets(ctx, *current, next)
		require.NoError(t, err)
		assert.True(t, swapped)

		got, err := raw.Get(ctx, "row")
		require.NoError(t, err)
		assert.Equal(t, []string{"b"}, got.APIKeys)
		assert.Equal(t, "https://one", got.BaseURL)
		assert.True(t, got.UpdatedAt.Equal(current.UpdatedAt), "re-encryption does not touch updated_at")

		swapped, err = swap.swapSecrets(ctx, *current, next)
		require.NoError(t, err)
		assert.False(t, swapped, "a stale read does not match")
	})
}
