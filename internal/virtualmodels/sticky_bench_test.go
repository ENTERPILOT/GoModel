package virtualmodels

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// benchStickyAtCapacity fills the pin map to its production capacity with
// unexpired pins, which is when a new session costs the most: nothing can be
// pruned, so room has to be made.
func benchStickyAtCapacity(tb testing.TB) (*stickySessions, func(string) bool) {
	tb.Helper()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	sticky := &stickySessions{now: func() time.Time { return now }}
	viable := func(string) bool { return true }
	for i := range maxStickySessions {
		sticky.resolve("smart", "sess-"+strconv.Itoa(i), viable, "openai/gpt-4o")
	}
	require.Len(tb, sticky.entries, maxStickySessions, "the pin map must start full")
	return sticky, viable
}

// BenchmarkStickyNewPinAtCapacity is the first request of a new session while
// the pin map is full: a prune sweep, then an eviction sweep.
func BenchmarkStickyNewPinAtCapacity(b *testing.B) {
	sticky, viable := benchStickyAtCapacity(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sticky.resolve("smart", "new-"+strconv.Itoa(i), viable, "openai/gpt-4o")
	}
}

// BenchmarkStickyPinRefreshAtCapacity is every later request of a pinned
// session, the common case, for comparison.
func BenchmarkStickyPinRefreshAtCapacity(b *testing.B) {
	sticky, viable := benchStickyAtCapacity(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sticky.resolve("smart", "sess-7", viable, "openai/gpt-4o")
	}
}
