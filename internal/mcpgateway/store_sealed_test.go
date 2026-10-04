package mcpgateway

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
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
		report, err := sealed.reencrypt(ctx, raw.(headerSwapper))
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

		again, err := sealed.reencrypt(ctx, raw.(headerSwapper))
		require.NoError(t, err)
		assert.Equal(t, 0, again.Reencrypted)
	})
}

func TestSwapHeadersIsConditional(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, raw Store) {
		ctx := context.Background()
		require.NoError(t, raw.Upsert(ctx, sealedTestServer("github", map[string]string{"Authorization": "a", "X-Team": "t"})))
		current, err := raw.Get(ctx, "github")
		require.NoError(t, err)

		swap := raw.(headerSwapper)
		next := *current
		next.Headers = map[string]string{"Authorization": "b", "X-Team": "t2"}
		next.Description = "ignored"
		swapped, err := swap.swapHeaders(ctx, *current, next)
		require.NoError(t, err)
		assert.True(t, swapped)
		got, err := raw.Get(ctx, "github")
		require.NoError(t, err)
		assert.Equal(t, next.Headers, got.Headers)
		assert.Empty(t, got.Description)

		swapped, err = swap.swapHeaders(ctx, *current, next)
		require.NoError(t, err)
		assert.False(t, swapped, "a stale read does not match")
	})
}

type rotateDuringUpsert struct {
	Store
	rotate func()
}

func (s rotateDuringUpsert) Upsert(ctx context.Context, server ManagedServer) error {
	s.rotate()
	return s.Store.Upsert(ctx, server)
}

func (s rotateDuringUpsert) Update(ctx context.Context, server ManagedServer) error {
	s.rotate()
	return s.Store.Update(ctx, server)
}

func TestSealedStoreResealsSaveThatRacedARotation(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, raw Store) {
		ctx := context.Background()
		box, keys := encryptiontest.NewBox(t)
		store := &sealedStore{
			Store: rotateDuringUpsert{Store: raw, rotate: func() { encryptiontest.Rotate(t, keys) }},
			box:   box,
			swap:  raw.(headerSwapper),
		}
		require.NoError(t, store.Upsert(ctx, sealedTestServer("github", map[string]string{"Authorization": "Bearer late"})))

		stored, err := raw.Get(ctx, "github")
		require.NoError(t, err)
		assert.Regexp(t, `^enc:v1:2:`, stored.Headers["Authorization"])
	})
}

func TestReencryptHeadersWithDottedNames(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, raw Store) {
		ctx := context.Background()
		headers := map[string]string{"X.Api.Key": "k", "$Odd": "o", "Authorization": "Bearer a"}
		require.NoError(t, raw.Upsert(ctx, sealedTestServer("dotted", headers)))

		box, _ := encryptiontest.NewBox(t)
		report, err := (&sealedStore{Store: raw, box: box}).reencrypt(ctx, raw.(headerSwapper))
		require.NoError(t, err)
		assert.Equal(t, encryption.Report{Entity: "mcp_servers", Rows: 1, Reencrypted: 1}, report)

		stored, err := raw.Get(ctx, "dotted")
		require.NoError(t, err)
		for name, value := range stored.Headers {
			assert.True(t, box.IsCurrent(value), "header %s is sealed", name)
		}
		got, err := sealStore(raw, box).Get(ctx, "dotted")
		require.NoError(t, err)
		assert.Equal(t, headers, got.Headers)
	})
}

func TestSealedStoreReportsAnUnconfirmedDataKey(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, raw Store) {
		ctx := context.Background()
		box, keys := encryptiontest.NewBox(t)
		store := &sealedStore{
			Store: rotateDuringUpsert{Store: raw, rotate: func() { keys.FailLists(3) }},
			box:   box,
			swap:  raw.(headerSwapper),
		}
		err := store.Upsert(ctx, sealedTestServer("github", map[string]string{"Authorization": "Bearer unconfirmed"}))
		require.ErrorIs(t, err, encryption.ErrSealUnconfirmed)
		assert.NotContains(t, err.Error(), "Bearer unconfirmed")

		stored, err := raw.Get(ctx, "github")
		require.NoError(t, err)
		assert.True(t, encryption.IsSealed(stored.Headers["Authorization"]), "the row was saved sealed")
	})
}

func TestSealedStoreUpdateSealsHeaders(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, raw Store) {
		ctx := context.Background()
		box, _ := encryptiontest.NewBox(t)
		store := sealStore(raw, box)
		require.ErrorIs(t, store.Update(ctx, sealedTestServer("github", map[string]string{"Authorization": "Bearer new"})), ErrNotFound)

		require.NoError(t, store.Upsert(ctx, sealedTestServer("github", map[string]string{"Authorization": "Bearer old"})))
		require.NoError(t, store.Update(ctx, sealedTestServer("github", map[string]string{"Authorization": "Bearer new"})))

		stored, err := raw.Get(ctx, "github")
		require.NoError(t, err)
		assert.True(t, encryption.IsSealed(stored.Headers["Authorization"]), "Update does not write headers in plaintext")
		got, err := store.Get(ctx, "github")
		require.NoError(t, err)
		assert.Equal(t, "Bearer new", got.Headers["Authorization"])
	})
}

func assertHeadersSealed(t *testing.T, raw Store, name string) {
	t.Helper()
	stored, err := raw.Get(context.Background(), name)
	require.NoError(t, err)
	require.NotEmpty(t, stored.Headers)
	for header, value := range stored.Headers {
		assert.True(t, encryption.IsSealed(value), "header %s of %s is stored in plaintext", header, name)
	}
}

func TestNoWritePathStoresPlaintextHeaders(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, raw Store) {
		ctx := context.Background()
		box, keys := encryptiontest.NewBox(t)
		racing := &sealedStore{
			Store: rotateDuringUpsert{Store: raw, rotate: func() { encryptiontest.Rotate(t, keys) }},
			box:   box,
			swap:  raw.(headerSwapper),
		}
		store := sealStore(raw, box)
		headers := func(i int) map[string]string {
			return map[string]string{"Authorization": fmt.Sprintf("Bearer %d", i), "X.Dotted": "d"}
		}
		writes := map[string]func(name string) error{
			"upsert": func(name string) error { return store.Upsert(ctx, sealedTestServer(name, headers(1))) },
			"update": func(name string) error {
				require.NoError(t, raw.Upsert(ctx, sealedTestServer(name, map[string]string{"Authorization": "Bearer legacy"})))
				return store.Update(ctx, sealedTestServer(name, headers(2)))
			},
			"upsert-racing-rotation": func(name string) error { return racing.Upsert(ctx, sealedTestServer(name, headers(3))) },
			"update-racing-rotation": func(name string) error {
				require.NoError(t, raw.Upsert(ctx, sealedTestServer(name, nil)))
				return racing.Update(ctx, sealedTestServer(name, headers(4)))
			},
		}
		for name, write := range writes {
			require.NoError(t, write(name), name)
			stored, err := raw.Get(ctx, name)
			require.NoError(t, err)
			assert.True(t, box.IsCurrent(stored.Headers["Authorization"]), "%s ends up under the data key active after the save", name)
		}
		for name := range writes {
			assertHeadersSealed(t, raw, name)
		}
	})
}

func TestServiceEditOfVirtualNamedServerSealsHeaders(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, raw Store) {
		ctx := context.Background()
		box, _ := encryptiontest.NewBox(t)
		// The row predates the virtual server, so the edit goes through Update.
		require.NoError(t, raw.Upsert(ctx, sealedTestServer("coding", map[string]string{"Authorization": "Bearer legacy"})))
		service, err := NewService(ctx, Options{
			Store:          sealStore(raw, box),
			VirtualServers: map[string]VirtualServerSpec{"coding": {Name: "coding", Servers: []string{"alpha"}}},
		})
		require.NoError(t, err)
		t.Cleanup(service.Close)

		server := sealedTestServer("coding", map[string]string{"Authorization": "Bearer edited"})
		server.Transport = config.MCPTransportHTTP
		require.NoError(t, service.Upsert(ctx, server))
		assertHeadersSealed(t, raw, "coding")
	})
}
