package mcpgateway

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/encryption"
	"github.com/enterpilot/gomodel/internal/encryption/encryptiontest"
)

func testOAuthRecord() oauthRecord {
	return oauthRecord{
		ServerURL:    "https://mcp.example.com/mcp",
		Resource:     "https://mcp.example.com/mcp",
		Issuer:       "https://auth.example.com",
		TokenURL:     "https://auth.example.com/token",
		RedirectURI:  "https://gomodel.example.com/admin/mcp-oauth/callback",
		ClientID:     "client-1",
		ClientSecret: "client-secret",
		AccessToken:  "access-secret",
		RefreshToken: "refresh-secret",
		Expiry:       time.Now().Add(time.Hour).UTC().Truncate(time.Second),
		SignedInAt:   time.Now().UTC().Truncate(time.Second),
	}
}

func TestOAuthStoreSealsRecords(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, raw Store) {
		ctx := context.Background()
		box, _ := encryptiontest.NewBox(t)
		store := newOAuthStore(raw, box)
		require.NotNil(t, store)

		record := testOAuthRecord()
		require.NoError(t, store.put(ctx, "github", record))

		rows := raw.(oauthRowStore)
		stored, err := rows.getOAuth(ctx, "github")
		require.NoError(t, err)
		assert.True(t, encryption.IsSealed(stored))
		for _, secret := range []string{"access-secret", "refresh-secret", "client-secret"} {
			assert.NotContains(t, stored, secret)
		}

		got, err := store.get(ctx, "github")
		require.NoError(t, err)
		assert.Equal(t, record.AccessToken, got.AccessToken)
		assert.Equal(t, record.RefreshToken, got.RefreshToken)
		assert.True(t, record.Expiry.Equal(got.Expiry))

		// A sealed record moved to another server does not open.
		require.NoError(t, rows.putOAuth(ctx, "gitlab", stored))
		_, err = store.get(ctx, "gitlab")
		require.Error(t, err)

		listed, err := store.list(ctx)
		require.Error(t, err, "the moved record is reported")
		assert.Contains(t, listed, "github", "one bad record does not hide the others")
		assert.NotContains(t, listed, "gitlab")

		require.NoError(t, store.delete(ctx, "gitlab"))
		require.NoError(t, store.delete(ctx, "github"))
		_, err = store.get(ctx, "github")
		require.ErrorIs(t, err, ErrNotFound)
	})
}

func TestOAuthStoreReencryptsPlaintext(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, raw Store) {
		ctx := context.Background()
		plain := newOAuthStore(raw, nil)
		require.NoError(t, plain.put(ctx, "github", testOAuthRecord()))

		box, _ := encryptiontest.NewBox(t)
		store := newOAuthStore(raw, box)
		report, err := store.reencrypt(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, report.Reencrypted)

		stored, err := raw.(oauthRowStore).getOAuth(ctx, "github")
		require.NoError(t, err)
		assert.True(t, encryption.IsSealed(stored))
		got, err := store.get(ctx, "github")
		require.NoError(t, err)
		assert.Equal(t, "access-secret", got.AccessToken)

		report, err = store.reencrypt(ctx)
		require.NoError(t, err)
		assert.Equal(t, 0, report.Reencrypted, "a second pass rewrites nothing")
	})
}
