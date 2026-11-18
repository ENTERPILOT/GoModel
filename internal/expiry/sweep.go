package expiry

import "time"

// MaxReconciliations bounds how much index repair a single Prune or
// EvictSoonest does. A key refreshed since it was tracked surfaces with a
// stale expiry and has to be re-tracked; a cache whose keys have all been
// refreshed would otherwise repair the entire heap while its lock is held.
// Past the budget the index is rebuilt once instead, which costs less than the
// pops it replaces and leaves it exact, so the sweep cannot need a second
// rebuild.
const MaxReconciliations = 64

// The sweeps below take the cache's map and a function reading an entry's
// true expiry. They take no lock: call them with the lock guarding entries
// held.

// Prune deletes the entries due at now, touching only the keys that are
// actually due rather than scanning entries. A key whose entry has moved past
// the recorded expiry is re-tracked; a key already deleted is discarded.
// Prune rebuilds the index when it has grown Stale.
func Prune[K comparable, V any](index *Index[K], entries map[K]V, expiresOf func(V) time.Time, now time.Time) {
	reconciled := 0
	for {
		key, tracked, ok := index.Expired(now)
		if !ok {
			break
		}
		entry, live := entries[key]
		switch {
		case !live:
		case expiresOf(entry).After(tracked):
			reconciled++
			if reconciled > MaxReconciliations {
				// The entry is still in the map, so the rebuild picks it up.
				Retrack(index, entries, expiresOf)
				continue
			}
			index.Track(key, expiresOf(entry))
		default:
			delete(entries, key)
		}
	}
	if index.Stale(len(entries)) {
		Retrack(index, entries, expiresOf)
	}
}

// EvictSoonest deletes the entry closest to expiry, to make room for a new
// one. It reports false when entries held nothing the index knew about.
func EvictSoonest[K comparable, V any](index *Index[K], entries map[K]V, expiresOf func(V) time.Time) bool {
	reconciled := 0
	for {
		key, tracked, ok := index.Soonest()
		if !ok {
			return false
		}
		entry, live := entries[key]
		switch {
		case !live:
			continue
		case expiresOf(entry).After(tracked):
			reconciled++
			if reconciled > MaxReconciliations {
				// The entry is still in the map, so the rebuild picks it up,
				// and the next pop is the true soonest.
				Retrack(index, entries, expiresOf)
				continue
			}
			index.Track(key, expiresOf(entry))
		default:
			delete(entries, key)
			return true
		}
	}
}

// Retrack rebuilds the index from entries, dropping the keys the cache
// deleted without the index ever seeing them.
func Retrack[K comparable, V any](index *Index[K], entries map[K]V, expiresOf func(V) time.Time) {
	index.Reset()
	for key, entry := range entries {
		index.Track(key, expiresOf(entry))
	}
}
