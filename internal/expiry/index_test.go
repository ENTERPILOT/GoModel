package expiry

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var base = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

func TestIndexPopsInExpiryOrder(t *testing.T) {
	var index Index[string]
	index.Track("third", base.Add(3*time.Minute))
	index.Track("first", base.Add(time.Minute))
	index.Track("second", base.Add(2*time.Minute))

	var got []string
	for {
		key, _, ok := index.Soonest()
		if !ok {
			break
		}
		got = append(got, key)
	}
	assert.Equal(t, []string{"first", "second", "third"}, got)
}

func TestIndexKeepsEveryKeyWithEqualExpiries(t *testing.T) {
	var index Index[int]
	for i := range 64 {
		index.Track(i, base)
	}

	seen := make(map[int]int, 64)
	for {
		key, _, ok := index.Soonest()
		if !ok {
			break
		}
		seen[key]++
	}
	require.Len(t, seen, 64, "every key must surface exactly once")
	for key, count := range seen {
		assert.Equal(t, 1, count, "key %d surfaced %d times", key, count)
	}
}

func TestIndexExpiredOnlyYieldsDueKeys(t *testing.T) {
	var index Index[string]
	index.Track("due", base)
	index.Track("later", base.Add(time.Hour))

	key, expires, ok := index.Expired(base)
	require.True(t, ok)
	assert.Equal(t, "due", key)
	assert.Equal(t, base, expires)

	_, _, ok = index.Expired(base)
	assert.False(t, ok, "a key expiring later is not due")
	assert.Equal(t, 1, index.Len())
}

func TestIndexEmptyReportsNothing(t *testing.T) {
	var index Index[string]

	_, _, ok := index.Expired(base)
	assert.False(t, ok)
	_, _, ok = index.Soonest()
	assert.False(t, ok)
	assert.Zero(t, index.Len())
}

func TestIndexReTrackedKeyMovesBack(t *testing.T) {
	var index Index[string]
	index.Track("refreshed", base)
	index.Track("other", base.Add(time.Minute))

	key, _, ok := index.Expired(base)
	require.True(t, ok)
	require.Equal(t, "refreshed", key)
	// The cache found it had been refreshed, so it goes back with the later
	// expiry and must no longer be the soonest.
	index.Track(key, base.Add(2*time.Minute))

	key, _, ok = index.Soonest()
	require.True(t, ok)
	assert.Equal(t, "other", key)
}

func TestIndexStaleTracksDeletedKeys(t *testing.T) {
	var index Index[string]
	for i := range 128 {
		index.Track(strconv.Itoa(i), base)
	}
	assert.True(t, index.Stale(8), "128 tracked against 8 live is stale")
	assert.False(t, index.Stale(128), "nothing was deleted")

	index.Reset()
	assert.Zero(t, index.Len())
	_, _, ok := index.Soonest()
	assert.False(t, ok)
}
