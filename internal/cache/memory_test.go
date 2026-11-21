package cache

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// entrySize is what the store charges for one entry.
func entrySize(key, value string) int {
	return memoryEntryOverhead + len(key) + len(value)
}

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
	assert.Equal(t, entrySize("kept", "b"), store.bytes, "an expired entry must give its bytes back")

	got, err = store.Get(ctx, "kept")
	require.NoError(t, err)
	assert.Equal(t, []byte("b"), got)
}

func TestMemoryStoreEvictsEntriesClosestToExpiry(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0)
	store := NewMemoryStore(3 * entrySize("k", "1234"))
	store.now = func() time.Time { return now }

	// The long-lived entry is the oldest, and the short-lived one has already
	// expired without being read: the budget must be made from that one, then
	// from the entry with the least time left, never from the live ones first.
	require.NoError(t, store.Set(ctx, "a", []byte("1234"), 24*time.Hour))
	require.NoError(t, store.Set(ctx, "b", []byte("1234"), time.Minute))
	require.NoError(t, store.Set(ctx, "c", []byte("1234"), 0))
	now = now.Add(2 * time.Minute)
	require.NoError(t, store.Set(ctx, "d", []byte("1234"), time.Hour))

	for key, want := range map[string][]byte{"a": []byte("1234"), "b": nil, "c": []byte("1234"), "d": []byte("1234")} {
		got, err := store.Get(ctx, key)
		require.NoError(t, err)
		assert.Equal(t, want, got, "entry %q", key)
	}

	require.NoError(t, store.Set(ctx, "e", []byte("1234"), 2*time.Hour))
	for key, want := range map[string][]byte{"a": []byte("1234"), "c": []byte("1234"), "d": nil, "e": []byte("1234")} {
		got, err := store.Get(ctx, key)
		require.NoError(t, err)
		assert.Equal(t, want, got, "entry %q: an entry without a TTL is evicted last", key)
	}
	assert.Equal(t, 3*entrySize("k", "1234"), store.bytes)
	assert.Len(t, store.queue, 3)
}

func TestMemoryStoreReplacesAndRefusesOversizedValues(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(entrySize("k", "newer"))

	require.NoError(t, store.Set(ctx, "k", []byte("old"), time.Hour))
	require.NoError(t, store.Set(ctx, "k", []byte("newer"), time.Hour))
	got, err := store.Get(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, []byte("newer"), got)
	assert.Equal(t, entrySize("k", "newer"), store.bytes, "a replaced value must not be counted twice")

	require.NoError(t, store.Set(ctx, "k", []byte("far too large"), time.Hour))
	got, err = store.Get(ctx, "k")
	require.NoError(t, err)
	assert.Nil(t, got, "a value over the budget must not leave the stale one behind")
	assert.Zero(t, store.bytes)
	assert.Empty(t, store.queue)
}

func TestMemoryStoreCopiesValues(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore(1 << 10)
	value := []byte("abc")

	require.NoError(t, store.Set(ctx, "k", value, 0))
	value[0] = 'x'
	got, err := store.Get(ctx, "k")
	require.NoError(t, err)
	require.Len(t, got, len(value))
	got[1] = 'y'

	again, err := store.Get(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, []byte("abc"), again)
}
