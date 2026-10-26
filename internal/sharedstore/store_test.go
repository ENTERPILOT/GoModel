package sharedstore

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/enterpilot/gomodel/ext"
	"github.com/enterpilot/gomodel/internal/storage"
	"github.com/enterpilot/gomodel/internal/storage/mongotest"
	"github.com/enterpilot/gomodel/internal/storage/sqlx"
	"github.com/enterpilot/gomodel/internal/storage/sqlx/sqlxtest"
)

// clock is a settable time source, so expiry is tested without sleeping.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

// runStoreSuite runs body against every reachable backend.
func runStoreSuite(t *testing.T, body func(t *testing.T, store *Store, clk *clock)) {
	t.Helper()
	run := func(t *testing.T, b backend) {
		clk := &clock{now: time.Unix(1_800_000_000, 0)}
		store := newStore(b, clk.Now)
		t.Cleanup(func() { _ = store.Close() })
		body(t, store, clk)
	}
	sqlxtest.Run(t, func(t *testing.T, db sqlx.DB) {
		b, err := newSQLBackend(context.Background(), db)
		require.NoError(t, err)
		run(t, b)
	})
	mongotest.Run(t, func(t *testing.T, db *mongo.Database) {
		b, err := newMongoBackend(context.Background(), db)
		require.NoError(t, err)
		run(t, b)
	})
}

func TestStorePutGetDelete(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store *Store, _ *clock) {
		ctx := context.Background()
		_, found, err := store.Get(ctx, "test.ns", "missing")
		require.NoError(t, err)
		assert.False(t, found)

		err = store.Put(ctx, "test.ns", "k", []byte("first"), time.Time{})
		require.NoError(t, err)
		err = store.Put(ctx, "test.ns", "k", []byte("second"), time.Time{})
		require.NoError(t, err)
		value, found, err := store.Get(ctx, "test.ns", "k")
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, []byte("second"), value)

		err = store.Delete(ctx, "test.ns", "k")
		require.NoError(t, err)
		_, found, err = store.Get(ctx, "test.ns", "k")
		require.NoError(t, err)
		assert.False(t, found)
		// Deleting a missing key is not an error.
		err = store.Delete(ctx, "test.ns", "k")
		assert.NoError(t, err)
	})
}

func TestStoreEmptyValueRoundTrips(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store *Store, _ *clock) {
		ctx := context.Background()
		err := store.Put(ctx, "test.ns", "marker", nil, time.Time{})
		require.NoError(t, err)
		value, found, err := store.Get(ctx, "test.ns", "marker")
		require.NoError(t, err)
		require.True(t, found)
		assert.NotNil(t, value)
		assert.Empty(t, value)

		entries, err := store.List(ctx, "test.ns")
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.NotNil(t, entries[0].Value)
	})
}

func TestStoreHidesAndSweepsExpiredEntries(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store *Store, clk *clock) {
		ctx := context.Background()
		start := clk.now
		expiry := start.Add(10 * time.Second)
		require.NoError(t, store.Put(ctx, "test.ns", "short", []byte("s"), expiry))
		require.NoError(t, store.Put(ctx, "test.ns", "forever", []byte("f"), time.Time{}))

		entries, err := store.List(ctx, "test.ns")
		require.NoError(t, err)
		assert.ElementsMatch(t, []ext.SharedEntry{
			{Key: "short", Value: []byte("s"), ExpiresAt: expiry.UTC()},
			{Key: "forever", Value: []byte("f")},
		}, entries)

		// Expiry is exclusive: an entry is gone at its expiry second.
		clk.now = expiry
		_, found, err := store.Get(ctx, "test.ns", "short")
		require.NoError(t, err)
		assert.False(t, found)
		entries, err = store.List(ctx, "test.ns")
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, "forever", entries[0].Key)

		// The sweep deletes the row: rewinding the clock no longer finds it.
		store.sweep()
		clk.now = start
		_, found, err = store.Get(ctx, "test.ns", "short")
		require.NoError(t, err)
		assert.False(t, found)
		_, found, err = store.Get(ctx, "test.ns", "forever")
		require.NoError(t, err)
		assert.True(t, found)
	})
}

func TestStorePutWithPastExpiryRemovesEntry(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store *Store, clk *clock) {
		ctx := context.Background()
		require.NoError(t, store.Put(ctx, "test.ns", "k", []byte("v"), time.Time{}))
		require.NoError(t, store.Put(ctx, "test.ns", "k", []byte("v"), clk.now.Add(-time.Second)))
		_, found, err := store.Get(ctx, "test.ns", "k")
		require.NoError(t, err)
		assert.False(t, found)
	})
}

func TestStoreIsolatesNamespaces(t *testing.T) {
	runStoreSuite(t, func(t *testing.T, store *Store, _ *clock) {
		ctx := context.Background()
		// "sso" is a prefix of "sso.revoked": neither may see the other.
		require.NoError(t, store.Put(ctx, "sso", "id", []byte("a"), time.Time{}))
		require.NoError(t, store.Put(ctx, "sso.revoked", "id", []byte("b"), time.Time{}))
		require.NoError(t, store.Put(ctx, "sso.revoked", "other", []byte("c"), time.Time{}))

		value, found, err := store.Get(ctx, "sso", "id")
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, []byte("a"), value)

		entries, err := store.List(ctx, "sso")
		require.NoError(t, err)
		assert.Equal(t, []ext.SharedEntry{{Key: "id", Value: []byte("a")}}, entries)

		require.NoError(t, store.Delete(ctx, "sso.revoked", "id"))
		_, found, err = store.Get(ctx, "sso", "id")
		require.NoError(t, err)
		assert.True(t, found, "deleting in one namespace removed the other's entry")

		entries, err = store.List(ctx, "sso.revoked")
		require.NoError(t, err)
		assert.Equal(t, []ext.SharedEntry{{Key: "other", Value: []byte("c")}}, entries)

		entries, err = store.List(ctx, "empty")
		require.NoError(t, err)
		assert.Empty(t, entries)
	})
}

func TestStoreRejectsInvalidArgumentsBeforeStorage(t *testing.T) {
	// A nil backend panics if reached, so each case proves validation runs
	// first.
	store := newStore(nil, time.Now)
	ctx := context.Background()
	tests := []struct {
		name      string
		namespace string
		key       string
		value     []byte
	}{
		{name: "empty namespace", namespace: "", key: "k"},
		{name: "uppercase namespace", namespace: "SSO", key: "k"},
		{name: "colon in namespace", namespace: "a:b", key: "k"},
		{name: "long namespace", namespace: strings.Repeat("a", ext.MaxSharedNamespaceBytes+1), key: "k"},
		{name: "empty key", namespace: "ns", key: ""},
		{name: "long key", namespace: "ns", key: strings.Repeat("k", ext.MaxSharedKeyBytes+1)},
		{name: "invalid utf8 key", namespace: "ns", key: "\xff"},
		{name: "nul in key", namespace: "ns", key: "a\x00b"},
		{name: "large value", namespace: "ns", key: "k", value: make([]byte, ext.MaxSharedValueBytes+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.Put(ctx, tt.namespace, tt.key, tt.value, time.Time{})
			require.Error(t, err)
			if tt.value != nil {
				return
			}
			_, _, err = store.Get(ctx, tt.namespace, tt.key)
			require.Error(t, err)
			err = store.Delete(ctx, tt.namespace, tt.key)
			assert.Error(t, err)
		})
	}
	_, err := store.List(ctx, "Bad")
	assert.Error(t, err)
}

func TestClosedStoreFailsEveryCall(t *testing.T) {
	store := newStore(nil, time.Now)
	require.NoError(t, store.Close())
	require.NoError(t, store.Close(), "Close must be idempotent")

	ctx := context.Background()
	err := store.Put(ctx, "ns", "k", nil, time.Time{})
	require.ErrorIs(t, err, ext.ErrSharedStoreClosed)
	_, _, err = store.Get(ctx, "ns", "k")
	require.ErrorIs(t, err, ext.ErrSharedStoreClosed)
	err = store.Delete(ctx, "ns", "k")
	require.ErrorIs(t, err, ext.ErrSharedStoreClosed)
	_, err = store.List(ctx, "ns")
	require.ErrorIs(t, err, ext.ErrSharedStoreClosed)
}

// New must share entries between two stores opened on one database, which is
// what two replicas on the same backend do.
func TestNewSharesEntriesThroughTheStorageBackend(t *testing.T) {
	ctx := context.Background()
	backend, err := storage.NewSQLite(storage.SQLiteConfig{Path: filepath.Join(t.TempDir(), "shared.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = backend.Close() })

	first, err := New(ctx, backend)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })
	second, err := New(ctx, backend)
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })

	require.NoError(t, first.Put(ctx, "sso.revoked", "session", []byte{}, time.Now().Add(time.Hour)))
	entries, err := second.List(ctx, "sso.revoked")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "session", entries[0].Key)

	_, err = New(ctx, nil)
	assert.Error(t, err)
}
