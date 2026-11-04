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
			PeriodSeconds: PeriodHourSeconds, Writer: "writer-a",
			RequestsWindowStart: 1700000000, RequestsCurrent: 3, RequestsPrevious: 1,
			TokensWindowStart: 1700000000, TokensCurrent: 40, TokensPrevious: 10,
		},
		{
			Scope: string(ScopeUserPath), Subject: "/customers", Partition: "/customers/bob",
			PeriodSeconds: PeriodHourSeconds, Writer: "writer-a",
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
	// A second writer saving the same keys gets rows of its own: neither
	// replica overwrites the other's.
	peer := make([]WindowSnapshot, len(live))
	for i, snap := range live {
		snap.Writer = "writer-b"
		snap.RequestsCurrent += 100
		peer[i] = snap
	}
	err = store.SaveCounters(ctx, peer)
	require.NoError(t, err)
	all, err := store.LoadCounters(ctx)
	require.NoError(t, err)
	require.ElementsMatch(t, append(append([]WindowSnapshot(nil), live...), peer...), all)
	// A reset clears every partition and every writer of the definition, and
	// gives the rule a new counter epoch, leaving other rules alone.
	err = store.UpsertRules(ctx, []Rule{
		{Scope: ScopeUserPath, Subject: "/customers", PeriodSeconds: PeriodHourSeconds, MaxRequests: new(int64(10)), Source: SourceManual},
		{Scope: ScopeUserPath, Subject: "/other", PeriodSeconds: PeriodHourSeconds, MaxRequests: new(int64(10)), Source: SourceManual},
	})
	require.NoError(t, err)
	before := epochs(t, store)
	err = store.ResetCounter(ctx, ScopeUserPath, "/customers", PeriodHourSeconds)
	require.NoError(t, err)
	got = loaded(t, "after reset")
	require.Empty(t, got)
	afterReset := epochs(t, store)
	require.NotEmpty(t, afterReset["/customers"])
	require.NotEqual(t, before["/customers"], afterReset["/customers"])
	require.Equal(t, before["/other"], afterReset["/other"])

	err = store.SaveCounters(ctx, live)
	require.NoError(t, err)
	err = store.SaveCounters(ctx, peer)
	require.NoError(t, err)
	err = store.ResetAllCounters(ctx)
	require.NoError(t, err)
	got = loaded(t, "after reset all")
	require.Empty(t, got)
	afterResetAll := epochs(t, store)
	require.Len(t, afterResetAll, len(afterReset), "reset all keeps every rule")
	for subject, epoch := range afterReset {
		require.Contains(t, afterResetAll, subject)
		require.NotEmpty(t, afterResetAll[subject], subject)
		require.NotEqual(t, epoch, afterResetAll[subject], subject)
	}
}

// epochs returns each stored rule's counter epoch by subject.
func epochs(t *testing.T, store Store) map[string]string {
	t.Helper()
	rules, err := store.ListRules(context.Background())
	require.NoError(t, err)
	bySubject := make(map[string]string, len(rules))
	for _, rule := range rules {
		bySubject[rule.Subject] = rule.CounterEpoch
	}
	return bySubject
}
