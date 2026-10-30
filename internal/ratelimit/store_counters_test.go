package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// unstamped returns rows without the save stamp a store adds, for comparing
// them with the snapshots that were saved.
func unstamped(rows []WindowSnapshot) []WindowSnapshot {
	out := make([]WindowSnapshot, len(rows))
	for i, row := range rows {
		row.UpdatedAt = 0
		out[i] = row
	}
	return out
}

// runCounterStoreSuite asserts the counter half of the Store contract. Every
// backend runs the same body, so a window that round-trips on SQLite has to
// round-trip on PostgreSQL and MongoDB too. seedStale writes a row directly,
// bypassing SaveCounters, so the suite can age one without waiting an hour.
func runCounterStoreSuite(t *testing.T, store Store, seedStale func(t *testing.T, snap WindowSnapshot, updatedAt int64)) {
	t.Helper()
	ctx := context.Background()
	saveStart := time.Now().Unix()
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
		got, err := store.LoadCounters(ctx, 0)
		require.NoError(t, err, "LoadCounters %s: %v", step, err)

		byPartition := make(map[string]WindowSnapshot, len(got))
		for _, snap := range got {
			byPartition[snap.Partition] = snap
		}
		return byPartition
	}
	// Every field survives the round trip, per partition, and the store
	// stamps the save whatever the snapshot carried.
	stamped := append([]WindowSnapshot(nil), live...)
	stamped[0].UpdatedAt = 1
	err := store.SaveCounters(ctx, stamped)
	require.NoError(t, err)

	got := loaded(t, "after save")
	for _, want := range live {
		row := got[want.Partition]
		require.GreaterOrEqual(t, row.UpdatedAt, saveStart, "the save stamps the row")
		require.LessOrEqual(t, row.UpdatedAt, time.Now().Unix())
		require.Equal(t, want, unstamped([]WindowSnapshot{row})[0])
	}

	// A load since a time returns only the rows saved since: an incremental
	// load must see every save, and skip rows nobody saved since.
	seedStale(t, WindowSnapshot{
		Scope: string(ScopeUserPath), Subject: "/customers", Partition: "/customers/carol",
		PeriodSeconds: PeriodHourSeconds, Writer: "writer-a",
		RequestsWindowStart: 1700000000, RequestsCurrent: 5,
	}, saveStart-60)
	recent, err := store.LoadCounters(ctx, saveStart)
	require.NoError(t, err)
	require.ElementsMatch(t, live, unstamped(recent))
	older, err := store.LoadCounters(ctx, saveStart-60)
	require.NoError(t, err)
	require.Len(t, older, 3)
	none, err := store.LoadCounters(ctx, time.Now().Unix()+60)
	require.NoError(t, err)
	require.Empty(t, none)
	// Omitting a row does not delete it: it stays restorable until it goes
	// two periods without a write.
	err = store.SaveCounters(ctx, live[:1])
	require.NoError(t, err)
	got = loaded(t, "after partial save")
	require.Len(t, got, 3)
	require.NoError(t, store.ResetCounter(ctx, ScopeUserPath, "/customers", PeriodHourSeconds))
	require.NoError(t, store.SaveCounters(ctx, live))

	// An in-flight row round-trips its gauge. It has no window, so the store
	// keeps it a day after its last save, and drops it after that.
	inFlight := WindowSnapshot{
		Scope: string(ScopeProvider), Subject: "openai", PeriodSeconds: PeriodConcurrent,
		Writer: "writer-a", Epoch: "e1", InFlight: 3,
	}
	require.NoError(t, store.SaveCounters(ctx, []WindowSnapshot{inFlight}))
	idle := inFlight
	idle.Writer, idle.InFlight = "writer-idle", 1
	seedStale(t, idle, time.Now().Unix()-PeriodHourSeconds)
	gone := inFlight
	gone.Writer = "writer-gone"
	seedStale(t, gone, time.Now().Unix()-inFlightRowRetention-60)
	require.NoError(t, store.SaveCounters(ctx, live))
	rows, err := store.LoadCounters(ctx, 0)
	require.NoError(t, err)
	var gauges []WindowSnapshot
	for _, row := range rows {
		if row.PeriodSeconds == PeriodConcurrent {
			gauges = append(gauges, row)
		}
	}
	require.ElementsMatch(t, []WindowSnapshot{inFlight, idle}, unstamped(gauges))
	require.NoError(t, store.ResetCounter(ctx, ScopeProvider, "openai", PeriodConcurrent))

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
	all, err := store.LoadCounters(ctx, 0)
	require.NoError(t, err)
	require.ElementsMatch(t, append(append([]WindowSnapshot(nil), live...), peer...), unstamped(all))
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
