package ratelimit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// remoteOf installs rows as another writer's, summed as a load does.
func remoteOf(l *limiter, rows []WindowSnapshot, rules []Rule, now time.Time) {
	var view peerView
	view.merge(rows, "self", true)
	l.setRemote(view.sum(rules, now, time.Minute))
}

func TestPeerSumAddsWritersRolledForward(t *testing.T) {
	period := PeriodHourSeconds
	start := int64(1_700_000_000) - int64(1_700_000_000)%period
	now := time.Unix(start+60, 0).UTC()
	rule := Rule{
		Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: period,
		MaxRequests: new(int64(100)), MaxTokens: new(int64(1000)), CounterEpoch: "e1",
	}
	row := func(writer string) WindowSnapshot {
		return WindowSnapshot{
			Scope: string(ScopeUserPath), Subject: "/team", PeriodSeconds: period, Writer: writer, Epoch: "e1",
		}
	}
	// Same window: added field by field, per counter kind.
	a := row("a")
	a.RequestsWindowStart, a.RequestsCurrent, a.RequestsPrevious = start, 5, 2
	a.TokensWindowStart, a.TokensCurrent, a.TokensPrevious = start, 10, 90
	b := row("b")
	b.RequestsWindowStart, b.RequestsCurrent, b.RequestsPrevious = start, 3, 7
	b.TokensWindowStart, b.TokensCurrent, b.TokensPrevious = start, 40, 20
	// One period behind: rolled forward first, so its current becomes
	// previous and its previous falls out.
	c := row("c")
	c.RequestsWindowStart, c.RequestsCurrent, c.RequestsPrevious = start-period, 9, 50
	// Two periods behind: holds nothing at now, whatever it counted, and
	// leaves the cache.
	d := row("d")
	d.RequestsWindowStart, d.RequestsCurrent = start-2*period, 80
	d.TokensWindowStart, d.TokensCurrent = start-2*period, 800
	// Another epoch: skipped, but kept, as this replica may not have
	// reloaded the rule yet.
	e := row("e")
	e.Epoch = "e0"
	e.RequestsWindowStart, e.RequestsCurrent = start, 1000
	// This writer's own row: already counted locally.
	self := row("self")
	self.RequestsWindowStart, self.RequestsCurrent = start, 1000

	var view peerView
	view.merge([]WindowSnapshot{a, b, c, d, e, self}, "self", true)
	requests, tokens, inFlight := view.sum([]Rule{rule}, now, time.Minute)

	key := keyForRule(rule)
	assert.Equal(t, map[ruleKey]windowCounter{key: {windowStart: start, current: 8, previous: 18}}, requests)
	assert.Equal(t, map[ruleKey]windowCounter{key: {windowStart: start, current: 50, previous: 110}}, tokens)
	assert.Nil(t, inFlight)
	assert.Len(t, view.rows, 4, "the rolled-out row leaves the cache, the other epoch's stays")
	assert.NotContains(t, view.rows, identityOf(d))

	// The limiter checks the local count plus the sum.
	l := newLimiter()
	l.setRemote(requests, tokens, inFlight)
	_, _, exceeded := l.admit([]Rule{rule}, now)
	require.Nil(t, exceeded)
	assert.Equal(t, int64(9+18*(period-60)/period), l.status(rule, now).RequestsUsed)
}

func TestPeerSumSkipsKeysThatHoldNothing(t *testing.T) {
	period := PeriodHourSeconds
	start := int64(1_700_000_000) - int64(1_700_000_000)%period
	now := time.Unix(start, 0).UTC()
	rule := Rule{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: period, MaxRequests: new(int64(1))}

	var view peerView
	view.merge([]WindowSnapshot{{
		Scope: string(ScopeUserPath), Subject: "/team", PeriodSeconds: period, Writer: "a",
		RequestsWindowStart: start - 2*period, RequestsCurrent: 5,
	}, {
		Scope: string(ScopeUserPath), Subject: "/team", PeriodSeconds: period, Writer: "b",
		RequestsWindowStart: start,
	}}, "self", true)
	requests, tokens, inFlight := view.sum([]Rule{rule}, now, time.Minute)
	assert.Nil(t, requests)
	assert.Nil(t, tokens)
	assert.Nil(t, inFlight)
}

// A replica whose clock runs a little ahead can save a row in the next
// window before this one reaches it. Its counts are folded into this
// window rather than dropped.
func TestRolledToFoldsCounterFromALaterWindow(t *testing.T) {
	period := PeriodMinuteSeconds
	start := int64(1_700_000_000) - int64(1_700_000_000)%period
	now := time.Unix(start+period-1, 0)
	ahead := windowCounter{windowStart: start + period, current: 2, previous: 5}
	assert.Equal(t, windowCounter{windowStart: start, current: 7}, ahead.rolledTo(now, period))
	behind := windowCounter{windowStart: start - period, current: 3, previous: 4}
	assert.Equal(t, windowCounter{windowStart: start, previous: 3}, behind.rolledTo(now, period))
}

// startedService builds and starts a service on store, closing it at cleanup
// (Close is idempotent, so a test may close it earlier to flush).
func startedService(t *testing.T, store Store) *Service {
	t.Helper()
	service, err := NewService(context.Background(), store, WithFlushInterval(0))
	require.NoError(t, err)
	t.Cleanup(service.Close)
	service.Start(context.Background())
	return service
}

func requestsUsed(t *testing.T, service *Service, now time.Time) int64 {
	t.Helper()
	statuses := service.Statuses(now)
	require.Len(t, statuses, 1)
	return statuses[0].RequestsUsed
}

func teamStore(t *testing.T) *memStore {
	t.Helper()
	store := &memStore{}
	err := store.UpsertRules(context.Background(), []Rule{{
		Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDaySeconds,
		MaxRequests: new(int64(100)), Source: SourceManual,
	}})
	require.NoError(t, err)
	return store
}

func acquireN(t *testing.T, service *Service, n int, now time.Time) {
	t.Helper()
	for range n {
		_, err := service.Acquire(onPath("/team"), now)
		require.NoError(t, err)
	}
}

// flakyDeleteStore fails ResetCounter while err is set.
type flakyDeleteStore struct {
	*memStore
	err error
}

func (s *flakyDeleteStore) ResetCounter(ctx context.Context, scope RuleScope, subject string, periodSeconds int64) error {
	if s.err != nil {
		return s.err
	}
	return s.memStore.ResetCounter(ctx, scope, subject, periodSeconds)
}

// A single instance restarting comes back under a new writer ID each time,
// next to the rows of its earlier runs. It must get exactly its own count
// back: not lose it, and not add the runs up.
func TestRestartUnderNewWriterRestoresOwnCounts(t *testing.T) {
	now := time.Now().UTC()
	store := teamStore(t)

	first := startedService(t, store)
	acquireN(t, first, 2, now)
	first.Close()

	second := startedService(t, store)
	require.NotEqual(t, first.writer, second.writer)
	require.Equal(t, int64(2), requestsUsed(t, second, now))
	acquireN(t, second, 1, now)
	second.Close()
	require.Len(t, store.counters, 2, "each run keeps its own row")
	byWriter := make(map[string]int64, len(store.counters))
	for _, snap := range store.counters {
		byWriter[snap.Writer] = snap.RequestsCurrent
	}
	require.Equal(t, map[string]int64{first.writer: 2, second.writer: 1}, byWriter,
		"a row holds only what its run counted, never what it loaded")

	third := startedService(t, store)
	assert.Equal(t, int64(3), requestsUsed(t, third, now))
	acquireN(t, third, 4, now)
	third.Close()

	// Each generation is counted once, however many came before it.
	fourth := startedService(t, store)
	assert.Equal(t, int64(7), requestsUsed(t, fourth, now))
}

func TestReplicasDoNotOverwriteEachOther(t *testing.T) {
	now := time.Now().UTC()
	store := teamStore(t)

	a := startedService(t, store)
	b := startedService(t, store)
	acquireN(t, a, 4, now)
	acquireN(t, b, 1, now)
	a.Close()
	b.Close()

	byWriter := make(map[string]int64, len(store.counters))
	for _, snap := range store.counters {
		byWriter[snap.Writer] = snap.RequestsCurrent
	}
	require.Equal(t, map[string]int64{a.writer: 4, b.writer: 1}, byWriter)

	// A replica restarting sees what the cluster counted: every replica's
	// row, each once.
	restarted := startedService(t, store)
	assert.Equal(t, int64(5), requestsUsed(t, restarted, now))
}

func TestResetDeletesEveryWritersRows(t *testing.T) {
	now := time.Now().UTC()
	store := teamStore(t)

	a := startedService(t, store)
	b := startedService(t, store)
	acquireN(t, a, 4, now)
	acquireN(t, b, 1, now)
	b.Close()

	err := a.ResetRule(context.Background(), ScopeUserPath, "/team", PeriodDaySeconds)
	require.NoError(t, err)
	require.Empty(t, store.counters, "a reset on one replica clears every replica's row")

	// a's final flush writes nothing back: its own window was reset too.
	a.Close()
	restarted := startedService(t, store)
	assert.Equal(t, int64(0), requestsUsed(t, restarted, now))
}

// A reset on one replica clears every replica, once config sync has them
// reload the rules. Rows saved under the old epoch before then — another
// replica's flush in the gap, or legacy rows a starting replica's import
// writes back on standalone MongoDB — are never restored.
func TestResetClearsEveryReplica(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := teamStore(t)
	// A rule stored before epochs existed, with a legacy row imported for it.
	store.rules[0].CounterEpoch = ""
	imported := WindowSnapshot{
		Scope: string(ScopeUserPath), Subject: "/team", PeriodSeconds: PeriodDaySeconds,
		RequestsWindowStart: now.Unix() - now.Unix()%PeriodDaySeconds, RequestsCurrent: 50,
	}
	require.NoError(t, store.SaveCounters(ctx, []WindowSnapshot{imported}))

	a := startedService(t, store)
	b := startedService(t, store)
	assert.Equal(t, int64(50), requestsUsed(t, b, now), "the upgrade keeps the imported count")
	acquireN(t, b, 1, now)
	a.SetChangeNotifier(func(ctx context.Context) { require.NoError(t, b.Refresh(ctx)) })

	b.flush(ctx)
	// The notifier fires after the store write, so b can still save its old
	// count in between; simulate that with a hook on a's local reload.
	a.onRulesSwapped = func() {
		a.onRulesSwapped = nil
		b.flush(ctx)
		require.NoError(t, store.SaveCounters(ctx, []WindowSnapshot{imported}))
	}
	require.NoError(t, a.ResetRule(ctx, ScopeUserPath, "/team", PeriodDaySeconds))
	assert.Equal(t, int64(0), requestsUsed(t, a, now))
	assert.Equal(t, int64(0), requestsUsed(t, b, now), "the reset reached the other replica")

	a.Close()
	b.Close()
	restarted := startedService(t, store)
	assert.Equal(t, int64(0), requestsUsed(t, restarted, now), "rows saved under the old epoch are not restored")
}

func teamRule(perChild bool, maxRequests int64) Rule {
	return Rule{
		Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDaySeconds,
		PerChild: perChild, MaxRequests: new(maxRequests), Source: SourceManual,
	}
}

// A rule change that resets a definition's live counters (here a shared to
// per-child round trip) must keep a restart from merging the pre-change count
// of a writer that no longer runs — even though nothing deletes that row.
func TestRuleModeRoundTripDoesNotRestoreEarlierCounts(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	// Deleting rows always fails, to show restore does not depend on it.
	store := &flakyDeleteStore{memStore: teamStore(t), err: errors.New("store unavailable")}

	first := startedService(t, store)
	acquireN(t, first, 90, now)
	first.Close()

	second := startedService(t, store)
	require.Equal(t, int64(90), requestsUsed(t, second, now))
	require.NoError(t, second.UpsertRules(ctx, []Rule{teamRule(true, 100)}))
	require.NoError(t, second.UpsertRules(ctx, []Rule{teamRule(false, 100)}))
	require.NotEmpty(t, store.counters, "the old writer's row is still saved")
	acquireN(t, second, 1, now)
	second.Close()

	restarted := startedService(t, store)
	assert.Equal(t, int64(1), requestsUsed(t, restarted, now))
}

// The rule can change, and change back, while no gateway runs to see it; the
// next start must still not restore the counts the change cleared.
func TestRuleRoundTripWhileStoppedDoesNotRestoreEarlierCounts(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := teamStore(t)

	first := startedService(t, store)
	acquireN(t, first, 90, now)
	first.Close()

	require.NoError(t, store.UpsertRules(ctx, []Rule{teamRule(true, 100)}))
	require.NoError(t, store.UpsertRules(ctx, []Rule{teamRule(false, 100)}))

	restarted := startedService(t, store)
	assert.Equal(t, int64(0), requestsUsed(t, restarted, now))
}

// Rows another replica saves under the new rule belong to it: nothing the
// replica that made the change does later may drop them.
func TestRuleChangeKeepsRowsSavedUnderNewRule(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := &flakyDeleteStore{memStore: teamStore(t), err: errors.New("store unavailable")}

	a := startedService(t, store)
	b := startedService(t, store)
	acquireN(t, a, 90, now)
	require.NoError(t, a.UpsertRules(ctx, []Rule{teamRule(true, 100)}))
	require.NoError(t, a.UpsertRules(ctx, []Rule{teamRule(false, 100)}))

	require.NoError(t, b.Refresh(ctx))
	acquireN(t, b, 5, now)
	b.Close()

	store.err = nil
	require.NoError(t, a.Refresh(ctx))
	a.Close()

	restarted := startedService(t, store)
	assert.Equal(t, int64(5), requestsUsed(t, restarted, now))
}

// Editing a limit is not a reset: the counts survive it and a restart.
func TestRuleLimitEditKeepsCounts(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := teamStore(t)

	first := startedService(t, store)
	acquireN(t, first, 90, now)
	require.NoError(t, first.UpsertRules(ctx, []Rule{teamRule(false, 200)}))
	require.Equal(t, int64(90), requestsUsed(t, first, now))
	first.Close()

	restarted := startedService(t, store)
	assert.Equal(t, int64(90), requestsUsed(t, restarted, now))
}

// A rule deleted and re-added on another replica, in the same mode, is a new
// definition there: a refresh here drops the counts of the old one.
func TestRefreshDropsCountsOfRuleReaddedElsewhere(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := teamStore(t)

	service := startedService(t, store)
	acquireN(t, service, 90, now)
	require.NoError(t, store.DeleteRule(ctx, ScopeUserPath, "/team", PeriodDaySeconds))
	require.NoError(t, store.UpsertRules(ctx, []Rule{teamRule(false, 100)}))
	require.NoError(t, service.Refresh(ctx))
	assert.Equal(t, int64(0), requestsUsed(t, service, now))
}

// A refresh that clears counts must not interleave with a flush: one that
// saw the new rule's epoch next to the counts not yet cleared would save them
// as the new rule's, and a restart would restore them.
func TestRefreshDoesNotInterleaveWithFlush(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := teamStore(t)
	service := startedService(t, store)
	acquireN(t, service, 90, now)

	// Deleted and re-added elsewhere: same mode, new epoch.
	require.NoError(t, store.DeleteRule(ctx, ScopeUserPath, "/team", PeriodDaySeconds))
	require.NoError(t, store.UpsertRules(ctx, []Rule{teamRule(false, 100)}))

	// Between the swap and the reset, a flush must not be able to start.
	swapped := false
	service.onRulesSwapped = func() {
		swapped = true
		if !assert.False(t, service.persistMu.TryLock(), "a flush could run between the swap and the reset") {
			service.persistMu.Unlock()
		}
	}
	require.NoError(t, service.Refresh(ctx))
	require.True(t, swapped)

	service.flush(ctx)
	service.Close()
	restarted := startedService(t, store)
	assert.Equal(t, int64(0), requestsUsed(t, restarted, now))
}
