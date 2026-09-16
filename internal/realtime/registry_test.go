package realtime

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestRegistry pins the clock and shrinks the capacity: eviction is a
// full-map scan, so filling the production 10,000 slots makes the capacity
// tests quadratic.
func newTestRegistry(now *time.Time) *CallRegistry {
	r := NewCallRegistry()
	r.now = func() time.Time { return *now }
	r.capacity = 100
	return r
}

func TestCallRegistryRegisterAndLookup(t *testing.T) {
	now := time.Unix(1000, 0)
	r := newTestRegistry(&now)

	r.Register("rtc_1", CallRoute{Model: "gpt-realtime", Provider: "openai"})

	route, ok := r.Lookup("rtc_1")
	require.True(t, ok)
	assert.Equal(t, "gpt-realtime", route.Model)
	assert.Equal(t, "openai", route.Provider, "route = %+v, want registered model and provider", route)
	_, ok = r.Lookup("rtc_unknown")
	assert.False(t, ok)
}

func TestCallRegistryExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	r := newTestRegistry(&now)

	r.Register("rtc_1", CallRoute{Model: "m", Provider: "p"})
	now = now.Add(DefaultCallTTL + time.Second)
	_, ok := r.Lookup("rtc_1")
	assert.False(t, ok)
}

func TestCallRegistryIgnoresEmptyAndNil(t *testing.T) {
	now := time.Unix(1000, 0)
	r := newTestRegistry(&now)
	r.Register("  ", CallRoute{Model: "m"})
	_, ok := r.Lookup("")
	assert.False(t, ok)

	var nilRegistry *CallRegistry
	nilRegistry.Register("rtc_1", CallRoute{})
	_, // must not panic
		ok = nilRegistry.Lookup("rtc_1")
	assert.False(t, ok)
}

func TestCallRegistryEvictsAtCapacity(t *testing.T) {
	now := time.Unix(1000, 0)
	r := newTestRegistry(&now)

	for i := range r.capacity {
		r.Register(fmt.Sprintf("rtc_%d", i), CallRoute{Model: "m"})
		now = now.Add(time.Millisecond) // strictly ordered expiries
	}
	r.Register("rtc_new", CallRoute{Model: "m"})

	require.LessOrEqual(t, len(r.entries), r.capacity, "registry grew to %d entries, want capped at %d", len(r.entries), r.capacity)
	_, ok := r.Lookup("rtc_new")
	require.True(t, ok, "newest call must survive eviction")
	_, ok = r.Lookup("rtc_0")
	require.False(t, ok, "soonest-expiring call should have been evicted")
}

func TestCallRegistryReRegisterAtCapacityDoesNotEvict(t *testing.T) {
	now := time.Unix(1000, 0)
	r := newTestRegistry(&now)

	for i := range r.capacity {
		r.Register(fmt.Sprintf("rtc_%d", i), CallRoute{Model: "m"})
		now = now.Add(time.Millisecond)
	}
	// Overwriting an existing id does not grow the map, so no unrelated entry
	// may be evicted to make room.
	r.Register("rtc_5", CallRoute{Model: "updated"})

	route, ok := r.Lookup("rtc_5")
	require.True(t, ok)
	require.Equal(t, "updated", route.Model, "route = %+v, want the entry updated in place", route)
	_, ok = r.Lookup("rtc_0")
	require.True(t, ok, "re-registering an existing id must not evict an unrelated entry")
	require.Len(t, r.entries, r.capacity)
}
