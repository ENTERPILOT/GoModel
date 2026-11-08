package session

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestSegmentCacheMatchesCanonicalSegment(t *testing.T) {
	cache := newSegmentCache(4, 1<<20)
	for _, raw := range []string{
		`[{"type":"function","function":{"name":"b","parameters":{"z":1,"a":2}}}]`,
		`[ {"a" : 1 , "a" : 2} ]`,
		`[]`,
		`"\ud800 lone surrogate"`,
	} {
		result := gjson.Parse(raw)
		want := canonicalSegment(result)
		first := cache.canonical(result)
		second := cache.canonical(result)
		assert.Equal(t, string(want), string(first), raw)
		assert.Equal(t, string(want), string(second), raw)
	}
	assert.Nil(t, cache.canonical(gjson.Result{}))
}

func TestSegmentCacheIsBounded(t *testing.T) {
	tests := []struct {
		name       string
		maxEntries int
		maxBytes   int
		wantLen    int
	}{
		{name: "entry limit", maxEntries: 3, maxBytes: 1 << 20, wantLen: 3},
		{name: "byte limit", maxEntries: 100, maxBytes: 3 * len(`[{"n":10}]`), wantLen: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := newSegmentCache(tt.maxEntries, tt.maxBytes)
			for i := range 10 {
				cache.canonical(gjson.Parse(fmt.Sprintf(`[{"n":%d}]`, 10+i)))
			}
			require.Equal(t, tt.wantLen, cache.order.Len())
			assert.Len(t, cache.entries, tt.wantLen)
			assert.LessOrEqual(t, cache.bytes, tt.maxBytes)

			// The most recent entries survive; the oldest were evicted.
			assert.Contains(t, cache.entries, cache.key(`[{"n":19}]`))
			assert.NotContains(t, cache.entries, cache.key(`[{"n":10}]`))
		})
	}
}

func TestSegmentCacheHitPromotesEntry(t *testing.T) {
	cache := newSegmentCache(2, 1<<20)
	cache.canonical(gjson.Parse(`["a"]`))
	cache.canonical(gjson.Parse(`["b"]`))
	cache.canonical(gjson.Parse(`["a"]`))
	cache.canonical(gjson.Parse(`["c"]`))

	assert.Contains(t, cache.entries, cache.key(`["a"]`))
	assert.Contains(t, cache.entries, cache.key(`["c"]`))
	assert.NotContains(t, cache.entries, cache.key(`["b"]`))
}

func TestSegmentCacheSkipsOversizedSegments(t *testing.T) {
	cache := newSegmentCache(8, 4)
	got := cache.canonical(gjson.Parse(`[{"b":1,"a":2}]`))
	assert.JSONEq(t, `[{"a":2,"b":1}]`, string(got))
	assert.Zero(t, cache.order.Len())
	assert.Zero(t, cache.bytes)
}

func TestSegmentCacheDoesNotRetainPaddedBuffers(t *testing.T) {
	cache := newSegmentCache(8, 1<<20)
	raw := "[" + strings.Repeat(" ", 1<<20) + "]"
	got := cache.canonical(gjson.Parse(raw))
	require.Equal(t, "[]", string(got))
	assert.LessOrEqual(t, cap(got), 8)

	elem, ok := cache.entries[cache.key(raw)]
	require.True(t, ok)
	cached := elem.Value.(*segmentEntry).value
	assert.LessOrEqual(t, cap(cached), 8)
	assert.Equal(t, cap(cached), cache.bytes)
}

func TestSegmentCacheConcurrentUse(t *testing.T) {
	cache := newSegmentCache(8, 1<<20)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 200 {
				raw := fmt.Sprintf(`[{"tool":%d,"group":%d}]`, i%16, g%2)
				want := canonicalSegment(gjson.Parse(raw))
				assert.Equal(t, string(want), string(cache.canonical(gjson.Parse(raw))))
			}
		})
	}
	wg.Wait()
	assert.LessOrEqual(t, cache.order.Len(), 8)
}
