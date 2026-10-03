package mcpgateway

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/encryption"
	"github.com/enterpilot/gomodel/internal/encryption/encryptiontest"
)

func sealedTestServer(name string, headers map[string]string) ManagedServer {
	return ManagedServer{
		Name:      name,
		URL:       "https://mcp.example.com/mcp",
		Transport: "http",
		Headers:   headers,
		Enabled:   true,
	}
}

func TestSealedStoreEncryptsHeaderValues(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, raw Store) {
		ctx := context.Background()
		box, _ := encryptiontest.NewBox(t)
		store := sealStore(raw, box)

		headers := map[string]string{"Authorization": "Bearer secret", "X-Team": "alpha"}
		require.NoError(t, store.Upsert(ctx, sealedTestServer("github", headers)))
		assert.Equal(t, "Bearer secret", headers["Authorization"], "the caller's map is not overwritten with ciphertext")

		stored, err := raw.Get(ctx, "github")
		require.NoError(t, err)
		require.Len(t, stored.Headers, 2)
		for name, value := range stored.Headers {
			assert.True(t, encryption.IsSealed(value), "header %s is not sealed", name)
		}

		got, err := store.Get(ctx, "github")
		require.NoError(t, err)
		assert.Equal(t, headers, got.Headers)

		list, err := store.List(ctx)
		require.NoError(t, err)
		require.Len(t, list, 1)
		assert.Equal(t, headers, list[0].Headers)

		// A header value moved to another header name or server fails.
		require.NoError(t, raw.Upsert(ctx, sealedTestServer("gitlab", map[string]string{"Authorization": stored.Headers["Authorization"]})))
		_, err = store.Get(ctx, "gitlab")
		require.Error(t, err)
		require.NoError(t, raw.Upsert(ctx, sealedTestServer("github", map[string]string{"X-Team": stored.Headers["Authorization"]})))
		_, err = store.Get(ctx, "github")
		require.Error(t, err)
	})
}

func TestSealedStoreReadsPlaintextHeaders(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, raw Store) {
		ctx := context.Background()
		require.NoError(t, raw.Upsert(ctx, sealedTestServer("github", map[string]string{"Authorization": "Bearer legacy"})))

		box, _ := encryptiontest.NewBox(t)
		got, err := sealStore(raw, box).Get(ctx, "github")
		require.NoError(t, err)
		assert.Equal(t, "Bearer legacy", got.Headers["Authorization"])
	})
}

func TestReencryptHeaders(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, raw Store) {
		ctx := context.Background()
		box, keys := encryptiontest.NewBox(t)
		require.NoError(t, raw.Upsert(ctx, sealedTestServer("plain", map[string]string{"Authorization": "Bearer plain"})))
		require.NoError(t, sealStore(raw, box).Upsert(ctx, sealedTestServer("old-key", map[string]string{"Authorization": "Bearer old"})))
		require.NoError(t, raw.Upsert(ctx, sealedTestServer("no-headers", nil)))

		sealed := &sealedStore{Store: raw, box: encryptiontest.Rotate(t, keys)}
		report, err := sealed.reencrypt(ctx)
		require.NoError(t, err)
		assert.Equal(t, encryption.Report{Entity: "mcp_servers", Rows: 3, Reencrypted: 2}, report)

		for name, want := range map[string]string{"plain": "Bearer plain", "old-key": "Bearer old"} {
			stored, err := raw.Get(ctx, name)
			require.NoError(t, err)
			assert.True(t, sealed.box.IsCurrent(stored.Headers["Authorization"]))
			got, err := sealed.Get(ctx, name)
			require.NoError(t, err)
			assert.Equal(t, want, got.Headers["Authorization"])
		}

		again, err := sealed.reencrypt(ctx)
		require.NoError(t, err)
		assert.Equal(t, 0, again.Reencrypted)
	})
}
