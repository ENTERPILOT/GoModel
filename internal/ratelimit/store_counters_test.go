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
// round-trip on PostgreSQL and MongoDB too. seedStale writes a row saved at
// updatedAt, so the suite can age one without waiting an hour.
// expire collects what expired, as PruneCounters and whatever else the
// backend relies on (a MongoDB TTL index) do.
func runCounterStoreSuite(t *testing.T, store Store, seedStale func(t *testing.T, snap WindowSnapshot, updatedAt int64), expire func(t *testing.T)) {
	t.Helper()
	ctx := context.Background()
	saveStart := time.Now().Unix()
	live := []WindowSnapshot{
		{
			Scope: string(ScopeUserPath), Subject: "/customers", Partition: "/customers/alice",
			PeriodSeconds: PeriodHourSeconds, Writer: "writer-a",
			RequestsWindowStart: 1700000000, RequestsCurrent: 3, RequestsPrevious: 1,
			TokensWindowStart: 1700000000, TokensCurrent: 40, TokensPrevious: 10,
			OwnCounts: true,
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
		got, err := store.LoadCounters(ctx, 0, "")
		require.NoError(t, err, "LoadCounters %s: %v", step, err)

		byPartition := make(map[string]WindowSnapshot, len(got))
		for _, snap := range got {
			if snap.PeriodSeconds != PeriodConcurrent {
				byPartition[snap.Partition] = snap
			}
		}
		return byPartition
	}
	// gauges returns the in-flight rows, which loaded leaves out.
	gauges := func(t *testing.T) []WindowSnapshot {
		t.Helper()
		rows, err := store.LoadCounters(ctx, 0, "")
		require.NoError(t, err)
		var out []WindowSnapshot
		for _, row := range rows {
			if row.PeriodSeconds == PeriodConcurrent {
				out = append(out, row)
			}
		}
		return unstamped(out)
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
	recent, err := store.LoadCounters(ctx, saveStart, "")
	require.NoError(t, err)
	require.ElementsMatch(t, live, unstamped(recent))
	older, err := store.LoadCounters(ctx, saveStart-60, "")
	require.NoError(t, err)
	require.Len(t, older, 3)
	none, err := store.LoadCounters(ctx, time.Now().Unix()+60, "")
	require.NoError(t, err)
	require.Empty(t, none)
	// A load leaves out the excluded writer's rows: a replica reads only
	// the others'.
	others, err := store.LoadCounters(ctx, 0, "writer-a")
	require.NoError(t, err)
	require.Empty(t, others)
	others, err = store.LoadCounters(ctx, saveStart, "writer-b")
	require.NoError(t, err)
	require.ElementsMatch(t, live, unstamped(others))
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
	require.Len(t, gauges(t), 3, "a save deletes nothing")
	expire(t)
	require.ElementsMatch(t, []WindowSnapshot{inFlight, idle}, gauges(t))
	// A reset leaves in-flight rows alone: they hold slots live requests
	// still take, not counts, and the writer keeps them in memory too.
	require.NoError(t, store.ResetCounter(ctx, ScopeProvider, "openai", PeriodConcurrent))
	require.ElementsMatch(t, []WindowSnapshot{inFlight, idle}, gauges(t), "a reset keeps in-flight rows")

	// Two periods without a write makes a row collectable; one within them
	// stays.
	seedStale(t, WindowSnapshot{
		Scope: string(ScopeUserPath), Subject: "/gone", Partition: "/gone/x", PeriodSeconds: PeriodHourSeconds,
		RequestsCurrent: 4,
	}, time.Now().Unix()-3*PeriodHourSeconds)
	seedStale(t, WindowSnapshot{
		Scope: string(ScopeUserPath), Subject: "/quiet", Partition: "/quiet/x", PeriodSeconds: PeriodHourSeconds,
		RequestsCurrent: 2,
	}, time.Now().Unix()-PeriodHourSeconds)
	err = store.SaveCounters(ctx, live)
	require.NoError(t, err)
	require.Len(t, loaded(t, "before collection"), 4, "a save deletes nothing")
	expire(t)
	got = loaded(t, "after collection")
	require.Len(t, got, 3)
	require.Contains(t, got, "/quiet/x", "a row within its retention stays")
	require.NotContains(t, got, "/gone/x")
	require.NoError(t, store.ResetCounter(ctx, ScopeUserPath, "/quiet", PeriodHourSeconds))
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
	allRows, err := store.LoadCounters(ctx, 0, "")
	require.NoError(t, err)
	var windows []WindowSnapshot
	for _, row := range unstamped(allRows) {
		if row.PeriodSeconds != PeriodConcurrent {
			windows = append(windows, row)
		}
	}
	require.ElementsMatch(t, append(append([]WindowSnapshot(nil), live...), peer...), windows)
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
	require.ElementsMatch(t, []WindowSnapshot{inFlight, idle}, gauges(t), "reset all keeps in-flight rows")
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

// counterBatchSizes straddle SQLite's 62 rows per statement, so a save takes
// one statement, two, and several.
var counterBatchSizes = []int{1, 70, 500}

// runCounterBatchSuite saves n snapshots in one call, twice, and checks that
// every row is written and then updated in place, every column with its own
// row's value.
func runCounterBatchSuite(t *testing.T, store Store, n int) {
	t.Helper()
	ctx := context.Background()
	snapshots := benchSnapshots("writer-a", n)
	require.NoError(t, store.SaveCounters(ctx, snapshots))
	rows, err := store.LoadCounters(ctx, 0, "")
	require.NoError(t, err)
	require.ElementsMatch(t, snapshots, unstamped(rows))

	for i := range snapshots {
		snapshots[i].RequestsCurrent += int64(i)
		snapshots[i].TokensPrevious = int64(i)
		snapshots[i].OwnCounts = i%2 == 1
	}
	require.NoError(t, store.SaveCounters(ctx, snapshots))
	rows, err = store.LoadCounters(ctx, 0, "")
	require.NoError(t, err)
	require.ElementsMatch(t, snapshots, unstamped(rows))
}
