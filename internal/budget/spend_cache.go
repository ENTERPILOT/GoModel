package budget

import (
	"sync"
	"time"
)

// defaultSpendCacheTTL bounds how long an enforcement check reuses a window's
// spend. The cache is bypassed while the usage logger writes a batch and
// cleared once it finishes, so within one instance new spend is seen as soon
// as it reaches the database; the TTL only bounds staleness from other
// instances' writes.
const defaultSpendCacheTTL = 2 * time.Second

// spendKey identifies a budget window. The end is always "now", so it is not
// part of the key; resets, settings changes and period rollovers move the
// start and therefore miss the cache on their own.
type spendKey struct {
	scope   Scope
	subject string
	start   int64
}

type cachedSpend struct {
	spend     Spend
	expiresAt time.Time
}

// spendCache memoizes SumSpend results for enforcement checks.
type spendCache struct {
	ttl time.Duration
	now func() time.Time

	mu        sync.Mutex
	entries   map[spendKey]cachedSpend
	nextSweep time.Time
	// generation advances whenever a flush starts or ends, so a query that
	// started before either cannot store its now-stale result after it.
	generation uint64
	// flushing counts usage batch writes in progress; while any runs, rows
	// may be visible before the write returns, so nothing is served or stored.
	flushing int
}

func newSpendCache(ttl time.Duration) *spendCache {
	return &spendCache{ttl: ttl, now: time.Now, entries: map[spendKey]cachedSpend{}}
}

func keyFor(window SpendWindow) spendKey {
	return spendKey{scope: window.Scope, subject: window.Subject, start: window.Start.UnixNano()}
}

// get returns the cached spend for every window, the indexes of the windows
// that missed, and the generation to pass to put.
func (c *spendCache) get(windows []SpendWindow) ([]Spend, []int, uint64) {
	spends := make([]Spend, len(windows))
	misses := make([]int, 0, len(windows))
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, window := range windows {
		if c.flushing > 0 {
			misses = append(misses, i)
			continue
		}
		entry, ok := c.entries[keyFor(window)]
		if !ok || !now.Before(entry.expiresAt) {
			misses = append(misses, i)
			continue
		}
		spends[i] = entry.spend
	}
	return spends, misses, c.generation
}

// currentGeneration returns the generation to pass to put for a fresh query.
func (c *spendCache) currentGeneration() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generation
}

// put stores spends summed during generation; it drops them when a flush
// started or ended since, or is still running.
func (c *spendCache) put(windows []SpendWindow, spends []Spend, generation uint64) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation != c.generation || c.flushing > 0 {
		return
	}
	// Drop expired windows at most once per TTL, so per-child budgets with
	// many subjects do not grow the map without bound.
	if !now.Before(c.nextSweep) {
		for key, entry := range c.entries {
			if !now.Before(entry.expiresAt) {
				delete(c.entries, key)
			}
		}
		c.nextSweep = now.Add(c.ttl)
	}
	for i, window := range windows {
		c.entries[keyFor(window)] = cachedSpend{spend: spends[i], expiresAt: now.Add(c.ttl)}
	}
}

// beginFlush stops serving and storing spends until the matching endFlush.
func (c *spendCache) beginFlush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flushing++
	c.generation++
}

// endFlush drops every cached spend, since the written batch changed them.
func (c *spendCache) endFlush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.flushing > 0 {
		c.flushing--
	}
	clear(c.entries)
	c.generation++
}
