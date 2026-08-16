package ratelimit

import (
	"context"
	"testing"
	"time"
)

// runCounterStoreSuite asserts the counter half of the Store contract. Every
// backend runs the same body, so a window that round-trips on SQLite has to
// round-trip on PostgreSQL and MongoDB too. seedStale writes a row directly,
// bypassing SaveCounters, so the suite can age one without waiting an hour.
func runCounterStoreSuite(t *testing.T, store Store, seedStale func(t *testing.T, snap WindowSnapshot, updatedAt int64)) {
	t.Helper()
	ctx := context.Background()
	live := []WindowSnapshot{
		{
			Scope: string(ScopeUserPath), Subject: "/customers", Partition: "/customers/alice",
			PeriodSeconds:       PeriodHourSeconds,
			RequestsWindowStart: 1700000000, RequestsCurrent: 3, RequestsPrevious: 1,
			TokensWindowStart: 1700000000, TokensCurrent: 40, TokensPrevious: 10,
		},
		{
			Scope: string(ScopeUserPath), Subject: "/customers", Partition: "/customers/bob",
			PeriodSeconds:       PeriodHourSeconds,
			RequestsWindowStart: 1700000060, RequestsCurrent: 1, RequestsPrevious: 2,
			TokensWindowStart: 1700000060, TokensCurrent: 7, TokensPrevious: 8,
		},
	}
	loaded := func(t *testing.T, step string) map[string]WindowSnapshot {
		t.Helper()
		got, err := store.LoadCounters(ctx)
		if err != nil {
			t.Fatalf("LoadCounters %s: %v", step, err)
		}
		byPartition := make(map[string]WindowSnapshot, len(got))
		for _, snap := range got {
			byPartition[snap.Partition] = snap
		}
		return byPartition
	}

	// Every field survives the round trip, per partition.
	if err := store.SaveCounters(ctx, live); err != nil {
		t.Fatalf("SaveCounters: %v", err)
	}
	got := loaded(t, "after save")
	for _, want := range live {
		if got[want.Partition] != want {
			t.Fatalf("%s = %+v, want %+v", want.Partition, got[want.Partition], want)
		}
	}

	// Omitting a row does not delete it: it stays restorable until it goes
	// two periods without a write.
	if err := store.SaveCounters(ctx, live[:1]); err != nil {
		t.Fatalf("SaveCounters partial: %v", err)
	}
	if got = loaded(t, "after partial save"); len(got) != 2 {
		t.Fatalf("partial save dropped a live row: %+v", got)
	}

	// Two periods without a write makes it collectable by the next save.
	seedStale(t, WindowSnapshot{
		Scope: string(ScopeUserPath), Subject: "/gone", PeriodSeconds: PeriodHourSeconds,
		RequestsCurrent: 4,
	}, time.Now().Unix()-3*PeriodHourSeconds)
	if err := store.SaveCounters(ctx, live); err != nil {
		t.Fatalf("SaveCounters collecting: %v", err)
	}
	if got = loaded(t, "after collection"); len(got) != 2 {
		t.Fatalf("stale row not collected: %+v", got)
	}

	// A reset clears every partition of the definition, not just one.
	if err := store.DeleteCounter(ctx, ScopeUserPath, "/customers", PeriodHourSeconds); err != nil {
		t.Fatalf("DeleteCounter: %v", err)
	}
	if got = loaded(t, "after delete"); len(got) != 0 {
		t.Fatalf("after delete = %+v, want every partition gone", got)
	}

	if err := store.SaveCounters(ctx, live); err != nil {
		t.Fatalf("SaveCounters again: %v", err)
	}
	if err := store.DeleteAllCounters(ctx); err != nil {
		t.Fatalf("DeleteAllCounters: %v", err)
	}
	if got = loaded(t, "after delete all"); len(got) != 0 {
		t.Fatalf("after delete all = %+v, want empty", got)
	}
}
