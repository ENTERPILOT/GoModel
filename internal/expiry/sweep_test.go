package expiry

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func identity(expires time.Time) time.Time { return expires }

// track builds a map and an index that agree, one key per expiry offset.
func track(offsets map[string]time.Duration) (map[string]time.Time, *Index[string]) {
	entries := make(map[string]time.Time, len(offsets))
	index := &Index[string]{}
	for key, offset := range offsets {
		entries[key] = base.Add(offset)
		index.Track(key, base.Add(offset))
	}
	return entries, index
}

func TestPruneDeletesOnlyDueEntries(t *testing.T) {
	entries, index := track(map[string]time.Duration{
		"past": -time.Minute, "now": 0, "later": time.Minute,
	})

	Prune(index, entries, identity, base)

	assert.Equal(t, map[string]time.Time{"later": base.Add(time.Minute)}, entries)
	assert.Equal(t, 1, index.Len())
}

func TestPruneDiscardsKeysTheCacheDeleted(t *testing.T) {
	entries, index := track(map[string]time.Duration{"gone": 0, "kept": time.Minute})
	delete(entries, "gone")

	Prune(index, entries, identity, base)

	assert.Equal(t, map[string]time.Time{"kept": base.Add(time.Minute)}, entries)
	assert.Equal(t, 1, index.Len())
}

func TestPruneReTracksRefreshedEntries(t *testing.T) {
	entries, index := track(map[string]time.Duration{"refreshed": 0})
	entries["refreshed"] = base.Add(time.Hour)

	Prune(index, entries, identity, base)

	require.Contains(t, entries, "refreshed", "a refreshed entry is not due")
	key, expires, ok := index.Soonest()
	require.True(t, ok)
	assert.Equal(t, "refreshed", key)
	assert.Equal(t, base.Add(time.Hour), expires, "the index now holds the true expiry")
}

// Past the reconciliation budget Prune rebuilds the index instead of
// re-tracking one key at a time. The result must be the same: every refreshed
// entry kept, every due one deleted, and the index exact.
func TestPruneRebuildsPastTheReconciliationBudget(t *testing.T) {
	entries := make(map[int]time.Time)
	var index Index[int]
	refreshed := 2 * MaxReconciliations
	for key := range refreshed {
		index.Track(key, base)
		entries[key] = base.Add(time.Hour)
	}
	index.Track(-1, base)
	entries[-1] = base

	Prune(&index, entries, identity, base)

	assert.Len(t, entries, refreshed, "only the entry that was really due goes")
	assert.NotContains(t, entries, -1)
	assert.Equal(t, refreshed, index.Len(), "the rebuild leaves one node per entry")
	_, _, ok := index.Expired(base)
	assert.False(t, ok, "nothing in the rebuilt index is due")
}

func TestPruneRebuildsAStaleIndex(t *testing.T) {
	entries := make(map[int]time.Time)
	var index Index[int]
	for key := range 100 {
		index.Track(key, base.Add(time.Hour))
	}
	entries[7] = base.Add(time.Hour)
	require.True(t, index.Stale(len(entries)))

	Prune(&index, entries, identity, base)

	require.Equal(t, 1, index.Len())
	key, expires, ok := index.Soonest()
	require.True(t, ok)
	assert.Equal(t, 7, key)
	assert.Equal(t, base.Add(time.Hour), expires)
}

func TestEvictSoonestDeletesTheEntryClosestToExpiry(t *testing.T) {
	entries, index := track(map[string]time.Duration{
		"first": time.Minute, "second": 2 * time.Minute, "third": 3 * time.Minute,
	})

	require.True(t, EvictSoonest(index, entries, identity))

	assert.NotContains(t, entries, "first")
	assert.Len(t, entries, 2)
}

func TestEvictSoonestSkipsDeletedAndRefreshedEntries(t *testing.T) {
	entries, index := track(map[string]time.Duration{
		"gone": time.Minute, "refreshed": 2 * time.Minute, "next": 3 * time.Minute,
	})
	delete(entries, "gone")
	entries["refreshed"] = base.Add(time.Hour)

	require.True(t, EvictSoonest(index, entries, identity))

	assert.Equal(t, map[string]time.Time{"refreshed": base.Add(time.Hour)}, entries)
}

func TestEvictSoonestRebuildsPastTheReconciliationBudget(t *testing.T) {
	entries := make(map[int]time.Time)
	var index Index[int]
	refreshed := 2 * MaxReconciliations
	for key := range refreshed {
		index.Track(key, base)
		entries[key] = base.Add(time.Hour)
	}
	index.Track(-1, base.Add(time.Minute))
	entries[-1] = base.Add(time.Minute)

	require.True(t, EvictSoonest(&index, entries, identity))

	assert.NotContains(t, entries, -1, "the true soonest entry goes, not a refreshed one")
	assert.Len(t, entries, refreshed)
}

func TestEvictSoonestReportsFalseWhenNothingIsLive(t *testing.T) {
	entries, index := track(map[string]time.Duration{"gone": 0})
	delete(entries, "gone")

	assert.False(t, EvictSoonest(index, entries, identity))
	assert.False(t, EvictSoonest(&Index[string]{}, map[string]time.Time{}, identity))
}

func TestRetrackRebuildsFromEntries(t *testing.T) {
	entries, index := track(map[string]time.Duration{"a": time.Minute, "b": 2 * time.Minute})
	index.Track("deleted", base)
	entries["a"] = base.Add(time.Hour)

	Retrack(index, entries, identity)

	var got []string
	for {
		key, _, ok := index.Soonest()
		if !ok {
			break
		}
		got = append(got, key)
	}
	assert.Equal(t, []string{"b", "a"}, got)
}
