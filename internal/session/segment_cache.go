package session

import (
	"container/list"
	"hash/maphash"
	"sync"

	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"
)

// Bounds for the canonical tools cache. Coding agents resend the same tool
// definitions (often tens of KB) on every turn, so a few hundred entries cover
// the distinct tool sets a gateway sees while the byte budget caps memory.
const (
	toolsCacheEntries = 256
	toolsCacheBytes   = 8 << 20
)

// segmentCache is a concurrency-safe, bounded LRU of canonicalSegment
// results keyed by the raw segment. It stores only a seeded hash and the
// length of the raw JSON, never the raw bytes; the per-process random seed
// keeps clients from precomputing colliding inputs.
type segmentCache struct {
	seed       maphash.Seed
	maxEntries int
	maxBytes   int

	mu      sync.Mutex
	bytes   int
	order   *list.List // of *segmentEntry, most recently used first
	entries map[segmentKey]*list.Element
}

type segmentKey struct {
	sum  uint64
	size int
}

type segmentEntry struct {
	key   segmentKey
	value json.RawMessage
}

func newSegmentCache(maxEntries, maxBytes int) *segmentCache {
	return &segmentCache{
		seed:       maphash.MakeSeed(),
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		order:      list.New(),
		entries:    make(map[segmentKey]*list.Element),
	}
}

func (c *segmentCache) key(raw string) segmentKey {
	return segmentKey{sum: maphash.String(c.seed, raw), size: len(raw)}
}

// canonical returns canonicalSegment(result), reusing the encoding of an
// identical raw segment seen recently. The returned bytes are shared and must
// not be modified.
func (c *segmentCache) canonical(result gjson.Result) json.RawMessage {
	if !result.Exists() || len(result.Raw) == 0 {
		return nil
	}
	key := c.key(result.Raw)
	c.mu.Lock()
	if elem, ok := c.entries[key]; ok {
		c.order.MoveToFront(elem)
		value := elem.Value.(*segmentEntry).value
		c.mu.Unlock()
		return value
	}
	c.mu.Unlock()

	value := canonicalSegment(result)
	if len(value) > c.maxBytes {
		return value
	}
	// canonicalSegment sizes its buffer from the raw segment, so whitespace-
	// padded input leaves a backing array far larger than the canonical bytes.
	// Store an exact-size copy so the byte budget reflects retained memory.
	if cap(value) > len(value) {
		value = append(make(json.RawMessage, 0, len(value)), value...)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.entries[key]; ok {
		c.order.MoveToFront(elem)
		return elem.Value.(*segmentEntry).value
	}
	c.entries[key] = c.order.PushFront(&segmentEntry{key: key, value: value})
	c.bytes += cap(value)
	for c.order.Len() > c.maxEntries || c.bytes > c.maxBytes {
		oldest := c.order.Back()
		entry := oldest.Value.(*segmentEntry)
		c.order.Remove(oldest)
		delete(c.entries, entry.key)
		c.bytes -= cap(entry.value)
	}
	return value
}
