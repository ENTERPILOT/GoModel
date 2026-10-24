// Package expiry orders keys by expiry time, so a bounded cache can find what
// to drop without scanning its whole map.
package expiry

import "time"

// Index is a min-heap of keys ordered by expiry. It holds no values and takes
// no lock: the cache owns both, and tracks a key when it first inserts it.
//
// Extending a key's TTL is deliberately not reported to the index. A cache
// that refreshes on every read would otherwise pay a heap fix on its hottest
// path, so the index is allowed to hold an expiry earlier than the truth.
// That is why Expired and Soonest hand back the expiry they recorded: the
// caller compares it with its own map, and re-Tracks a key that has moved.
// A key the cache has already deleted is simply discarded when it surfaces.
type Index[K comparable] struct {
	queue []node[K]
}

type node[K comparable] struct {
	key     K
	expires time.Time
}

// Track records that key expires at expires. Call it when a key enters the
// cache, and again when a popped key turns out to have been refreshed since.
func (i *Index[K]) Track(key K, expires time.Time) {
	i.queue = append(i.queue, node[K]{key: key, expires: expires})
	i.up(len(i.queue) - 1)
}

// Expired pops the next key due at now, with the expiry the index holds for
// it. It reports false when the soonest key is not due yet.
func (i *Index[K]) Expired(now time.Time) (K, time.Time, bool) {
	if len(i.queue) == 0 || i.queue[0].expires.After(now) {
		var zero K
		return zero, time.Time{}, false
	}
	next := i.pop()
	return next.key, next.expires, true
}

// Soonest pops the key closest to expiry, with the expiry the index holds for
// it. It reports false when the index is empty.
func (i *Index[K]) Soonest() (K, time.Time, bool) {
	if len(i.queue) == 0 {
		var zero K
		return zero, time.Time{}, false
	}
	next := i.pop()
	return next.key, next.expires, true
}

// Len is how many keys are tracked, including ones the cache has deleted and
// the index has not surfaced yet.
func (i *Index[K]) Len() int { return len(i.queue) }

// Stale reports whether the index holds far more keys than the live count,
// which is what happens when a cache deletes keys it never pops. The caller
// answers by resetting and re-tracking what it still holds.
func (i *Index[K]) Stale(live int) bool { return len(i.queue) > 2*live+16 }

// Reset empties the index.
func (i *Index[K]) Reset() { i.queue = i.queue[:0] }

func (i *Index[K]) pop() node[K] {
	top := i.queue[0]
	last := len(i.queue) - 1
	i.queue[0] = i.queue[last]
	var zero node[K]
	i.queue[last] = zero
	i.queue = i.queue[:last]
	if last > 0 {
		i.down(0)
	}
	return top
}

func (i *Index[K]) up(child int) {
	for child > 0 {
		parent := (child - 1) / 2
		if !i.queue[child].expires.Before(i.queue[parent].expires) {
			return
		}
		i.queue[child], i.queue[parent] = i.queue[parent], i.queue[child]
		child = parent
	}
}

func (i *Index[K]) down(parent int) {
	for {
		left := 2*parent + 1
		if left >= len(i.queue) {
			return
		}
		soonest := left
		if right := left + 1; right < len(i.queue) && i.queue[right].expires.Before(i.queue[left].expires) {
			soonest = right
		}
		if !i.queue[soonest].expires.Before(i.queue[parent].expires) {
			return
		}
		i.queue[parent], i.queue[soonest] = i.queue[soonest], i.queue[parent]
		parent = soonest
	}
}
