package cache

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryStoreExpiresEntries(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	store := NewMemoryStore(1 << 10)
	store.now = func() time.Time { return now }

	require.NoError(t, store.Set(ctx, "short", []byte("a"), time.Minute))
	require.NoError(t, store.Set(ctx, "kept", []byte("b"), 0))

	got, err := store.Get(ctx, "short")
	require.NoError(t, err)
	assert.Equal(t, []byte("a"), got)

	now = now.Add(time.Minute)
	got, err = store.Get(ctx, "short")
	require.NoError(t, err)
	assert.Nil(t, got, "an entry must not be served at or past its TTL")
	assert.Equal(t, len("kept")+1, store.bytes, "an expired entry must give its bytes back")

	got, err = store.Get(ctx, "kept")
	require.NoError(t, err)
	assert.Equal(t, []byte("b"), got)
}

func TestMemoryStoreEvictsOldestOverBudget(t *testing.T) {
	ctx := context.Background()
	// Each entry is a 1-byte key plus a 4-byte value; three do not fit.
	store := NewMemoryStore(12)

	for _, key := range []string{"a", "b", "c"} {
		require.NoError(t, store.Set(ctx, key, []byte("1234"), time.Hour))
	}

	got, err := store.Get(ctx, "a")
	require.NoError(t, err)
	assert.Nil(t, got, "the oldest entry must be evicted first")
	for _, key := range []string{"b", "c"} {
		got, err = store.Get(ctx, key)
		require.NoError(t, err)
		assert.Equal(t, []byte("1234"), got, "entry %q", key)
	}
	assert.Equal(t, 10, store.bytes)
}

func TestMemoryStoreReplacesAndRefusesOversizedValues(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(8)

	require.NoError(t, store.Set(ctx, "k", []byte("old"), time.Hour))
	require.NoError(t, store.Set(ctx, "k", []byte("newer"), time.Hour))
	got, err := store.Get(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, []byte("newer"), got)
	assert.Equal(t, 6, store.bytes, "a replaced value must not be counted twice")

	require.NoError(t, store.Set(ctx, "k", []byte("far too large"), time.Hour))
	got, err = store.Get(ctx, "k")
	require.NoError(t, err)
	assert.Nil(t, got, "a value over the budget must not leave the stale one behind")
	assert.Zero(t, store.bytes)
}

func TestMemoryStoreCopiesValues(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(1 << 10)
	value := []byte("abc")

	require.NoError(t, store.Set(ctx, "k", value, 0))
	value[0] = 'x'
	got, err := store.Get(ctx, "k")
	require.NoError(t, err)
	got[1] = 'y'

	again, err := store.Get(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, []byte("abc"), again)
}
