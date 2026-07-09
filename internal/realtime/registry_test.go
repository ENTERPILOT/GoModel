package realtime

import (
	"fmt"
	"testing"
	"time"
)

func newTestRegistry(now *time.Time) *CallRegistry {
	r := NewCallRegistry()
	r.now = func() time.Time { return *now }
	return r
}

func TestCallRegistryRegisterAndLookup(t *testing.T) {
	now := time.Unix(1000, 0)
	r := newTestRegistry(&now)

	r.Register("rtc_1", CallRoute{Model: "gpt-realtime", Provider: "openai"})

	route, ok := r.Lookup("rtc_1")
	if !ok {
		t.Fatal("expected registered call to be found")
	}
	if route.Model != "gpt-realtime" || route.Provider != "openai" {
		t.Errorf("route = %+v, want registered model and provider", route)
	}
	if _, ok := r.Lookup("rtc_unknown"); ok {
		t.Error("unknown call id must not resolve")
	}
}

func TestCallRegistryExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	r := newTestRegistry(&now)

	r.Register("rtc_1", CallRoute{Model: "m", Provider: "p"})
	now = now.Add(DefaultCallTTL + time.Second)

	if _, ok := r.Lookup("rtc_1"); ok {
		t.Error("expired call must not resolve")
	}
}

func TestCallRegistryIgnoresEmptyAndNil(t *testing.T) {
	now := time.Unix(1000, 0)
	r := newTestRegistry(&now)
	r.Register("  ", CallRoute{Model: "m"})
	if _, ok := r.Lookup(""); ok {
		t.Error("empty call id must not resolve")
	}

	var nilRegistry *CallRegistry
	nilRegistry.Register("rtc_1", CallRoute{}) // must not panic
	if _, ok := nilRegistry.Lookup("rtc_1"); ok {
		t.Error("nil registry must not resolve")
	}
}

func TestCallRegistryEvictsAtCapacity(t *testing.T) {
	now := time.Unix(1000, 0)
	r := newTestRegistry(&now)

	for i := range maxCalls {
		r.Register(fmt.Sprintf("rtc_%d", i), CallRoute{Model: "m"})
		now = now.Add(time.Millisecond) // strictly ordered expiries
	}
	r.Register("rtc_new", CallRoute{Model: "m"})

	if len(r.entries) > maxCalls {
		t.Errorf("registry grew to %d entries, want capped at %d", len(r.entries), maxCalls)
	}
	if _, ok := r.Lookup("rtc_new"); !ok {
		t.Error("newest call must survive eviction")
	}
	if _, ok := r.Lookup("rtc_0"); ok {
		t.Error("soonest-expiring call should have been evicted")
	}
}

func TestCallRegistryReRegisterAtCapacityDoesNotEvict(t *testing.T) {
	now := time.Unix(1000, 0)
	r := newTestRegistry(&now)

	for i := range maxCalls {
		r.Register(fmt.Sprintf("rtc_%d", i), CallRoute{Model: "m"})
		now = now.Add(time.Millisecond)
	}
	// Overwriting an existing id does not grow the map, so no unrelated entry
	// may be evicted to make room.
	r.Register("rtc_5", CallRoute{Model: "updated"})

	if route, ok := r.Lookup("rtc_5"); !ok || route.Model != "updated" {
		t.Errorf("route = %+v (found %v), want the entry updated in place", route, ok)
	}
	if _, ok := r.Lookup("rtc_0"); !ok {
		t.Error("re-registering an existing id must not evict an unrelated entry")
	}
	if len(r.entries) != maxCalls {
		t.Errorf("registry has %d entries, want %d", len(r.entries), maxCalls)
	}
}
