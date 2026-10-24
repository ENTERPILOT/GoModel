package realtime

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func benchRegistryAtCapacity(tb testing.TB) *CallRegistry {
	tb.Helper()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	registry := &CallRegistry{
		entries:  make(map[string]callEntry, maxCalls),
		ttl:      DefaultCallTTL,
		capacity: maxCalls,
		now:      func() time.Time { return now },
	}
	for i := range maxCalls {
		registry.Register("rtc_"+strconv.Itoa(i), CallRoute{Model: "gpt-4o-realtime", Provider: "openai"})
	}
	require.Len(tb, registry.entries, maxCalls, "the registry must start full")
	return registry
}

// BenchmarkCallRegistryRegisterAtCapacity registers a new call while the
// registry is full: a prune sweep, then an eviction sweep.
func BenchmarkCallRegistryRegisterAtCapacity(b *testing.B) {
	registry := benchRegistryAtCapacity(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		registry.Register("rtc_new_"+strconv.Itoa(i), CallRoute{Model: "gpt-4o-realtime", Provider: "openai"})
	}
}

// BenchmarkCallRegistryReRegisterAtCapacity re-registers an existing call,
// which makes no room but still sweeps for expired entries.
func BenchmarkCallRegistryReRegisterAtCapacity(b *testing.B) {
	registry := benchRegistryAtCapacity(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		registry.Register("rtc_7", CallRoute{Model: "gpt-4o-realtime", Provider: "openai"})
	}
}

// BenchmarkCallRegistryLookupAtCapacity is the sideband attach path, for
// comparison: a single map read.
func BenchmarkCallRegistryLookupAtCapacity(b *testing.B) {
	registry := benchRegistryAtCapacity(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		registry.Lookup("rtc_7")
	}
}
