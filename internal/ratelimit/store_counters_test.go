package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
		require.NoError(t, err, "LoadCounters %s: %v", step, err)

		byPartition := make(map[string]WindowSnapshot, len(got))
		for _, snap := range got {
			byPartition[snap.Partition] = snap
		}
		return byPartition
	}
	// Every field survives the round trip, per partition.
	err := store.SaveCounters(ctx, live)
	require.NoError(t, err)

	got := loaded(t, "after save")
	for _, want := range live {
		require.Equal(t, want, got[want.Partition])
	}
	// Omitting a row does not delete it: it stays restorable until it goes
	// two periods without a write.
	err = store.SaveCounters(ctx, live[:1])
	require.NoError(t, err)
	got = loaded(t, "after partial save")
	require.Len(t, got, 2)

	// Two periods without a write makes it collectable by the next save.
	seedStale(t, WindowSnapshot{
		Scope: string(ScopeUserPath), Subject: "/gone", PeriodSeconds: PeriodHourSeconds,
		RequestsCurrent: 4,
	}, time.Now().Unix()-3*PeriodHourSeconds)
	err = store.SaveCounters(ctx, live)
	require.NoError(t, err)
	got = loaded(t, "after collection")
	require.Len(t, got, 2)
	// A reset clears every partition of the definition, not just one.
	err = store.DeleteCounter(ctx, ScopeUserPath, "/customers", PeriodHourSeconds)
	require.NoError(t, err)
	got = loaded(t, "after delete")
	require.Empty(t, got)
	err = store.SaveCounters(ctx, live)
	require.NoError(t, err)
	err = store.DeleteAllCounters(ctx)
	require.NoError(t, err)
	got = loaded(t, "after delete all")
	require.Empty(t, got)
}
