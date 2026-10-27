package ratelimit

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRestoreMergesWritersByMax(t *testing.T) {
	period := PeriodHourSeconds
	start := int64(1_700_000_000) - int64(1_700_000_000)%period
	now := time.Unix(start+60, 0).UTC()
	rule := Rule{
		Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: period,
		MaxRequests: new(int64(100)), MaxTokens: new(int64(1000)),
	}
	row := func(writer string) WindowSnapshot {
		return WindowSnapshot{
			Scope: string(ScopeUserPath), Subject: "/team", PeriodSeconds: period, Writer: writer,
		}
	}
	// Same window: the larger current and the larger previous win,
	// independently and per counter kind.
	a := row("a")
	a.RequestsWindowStart, a.RequestsCurrent, a.RequestsPrevious = start, 5, 2
	a.TokensWindowStart, a.TokensCurrent, a.TokensPrevious = start, 10, 90
	b := row("b")
	b.RequestsWindowStart, b.RequestsCurrent, b.RequestsPrevious = start, 3, 7
	b.TokensWindowStart, b.TokensCurrent, b.TokensPrevious = start, 40, 20
	// One period behind: rolled forward first, so its current becomes
	// previous (9 beats 7) and its previous falls out.
	c := row("c")
	c.RequestsWindowStart, c.RequestsCurrent, c.RequestsPrevious = start-period, 9, 50
	// Two periods behind: holds nothing at now, whatever it counted.
	d := row("d")
	d.RequestsWindowStart, d.RequestsCurrent = start-2*period, 80
	d.TokensWindowStart, d.TokensCurrent = start-2*period, 800

	l := newLimiter()
	l.restore([]WindowSnapshot{a, b, c, d}, []Rule{rule}, now)

	key := keyForRule(rule)
	require.Contains(t, l.requests, key)
	assert.Equal(t, windowCounter{windowStart: start, current: 5, previous: 9}, *l.requests[key])
	require.Contains(t, l.tokens, key)
	assert.Equal(t, windowCounter{windowStart: start, current: 40, previous: 90}, *l.tokens[key])
}

func TestRestoreSkipsKeysThatHoldNothing(t *testing.T) {
	period := PeriodHourSeconds
	start := int64(1_700_000_000) - int64(1_700_000_000)%period
	now := time.Unix(start, 0).UTC()
	rule := Rule{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: period, MaxRequests: new(int64(1))}

	l := newLimiter()
	l.restore([]WindowSnapshot{{
		Scope: string(ScopeUserPath), Subject: "/team", PeriodSeconds: period, Writer: "a",
		RequestsWindowStart: start - 2*period, RequestsCurrent: 5,
	}}, []Rule{rule}, now)
	assert.Empty(t, l.requests)
	assert.Empty(t, l.tokens)
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

// flakyDeleteStore fails DeleteCounter while err is set.
type flakyDeleteStore struct {
	*memStore
	err error
}

func (s *flakyDeleteStore) DeleteCounter(ctx context.Context, scope RuleScope, subject string, periodSeconds int64) error {
	if s.err != nil {
		return s.err
	}
	return s.memStore.DeleteCounter(ctx, scope, subject, periodSeconds)
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

	third := startedService(t, store)
	assert.Equal(t, int64(3), requestsUsed(t, third, now))
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

	// A replica restarting takes the busiest peer's count as its own share
	// rather than the sum of all of them.
	restarted := startedService(t, store)
	assert.Equal(t, int64(4), requestsUsed(t, restarted, now))
}

func TestResetDeletesEveryWritersRows(t *testing.T) {
	now := time.Now().UTC()
	store := teamStore(t)

	a := startedService(t, store)
	b := startedService(t, store)
	acquireN(t, a, 4, now)
	acquireN(t, b, 1, now)
	b.Close()

	err := a.ResetRule(ScopeUserPath, "/team", PeriodDaySeconds)
	require.NoError(t, err)
	require.Empty(t, store.counters, "a reset on one replica clears every replica's row")

	// a's final flush writes nothing back: its own window was reset too.
	a.Close()
	restarted := startedService(t, store)
	assert.Equal(t, int64(0), requestsUsed(t, restarted, now))
}

// A rule change that resets a definition's live counters (here a shared to
// per-child round trip) must also drop every writer's saved rows, or the next
// restart would merge the pre-change count of a writer that no longer runs.
func TestRuleModeChangeDeletesEveryWritersRows(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := teamStore(t)

	first := startedService(t, store)
	acquireN(t, first, 90, now)
	first.Close()

	second := startedService(t, store)
	require.Equal(t, int64(90), requestsUsed(t, second, now))
	rule := Rule{
		Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDaySeconds,
		MaxRequests: new(int64(100)), Source: SourceManual,
	}
	perChild := rule
	perChild.PerChild = true
	require.NoError(t, second.UpsertRules(ctx, []Rule{perChild}))
	require.Empty(t, store.counters, "a mode change clears every writer's rows")
	require.NoError(t, second.UpsertRules(ctx, []Rule{rule}))
	acquireN(t, second, 1, now)
	second.Close()

	restarted := startedService(t, store)
	assert.Equal(t, int64(1), requestsUsed(t, restarted, now))
}

// A mode change whose row delete fails has already swapped its rules in, so
// retrying the same update finds nothing that differs. The delete must still
// be retried, or switching back and restarting restores the cleared count.
func TestRuleModeChangeRetriesFailedRowDelete(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := &flakyDeleteStore{memStore: teamStore(t)}

	first := startedService(t, store)
	acquireN(t, first, 90, now)
	first.Close()

	second := startedService(t, store)
	require.Equal(t, int64(90), requestsUsed(t, second, now))
	rule := Rule{
		Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDaySeconds,
		MaxRequests: new(int64(100)), Source: SourceManual,
	}
	perChild := rule
	perChild.PerChild = true
	store.err = errors.New("store unavailable")
	require.Error(t, second.UpsertRules(ctx, []Rule{perChild}))
	require.NotEmpty(t, store.counters)

	store.err = nil
	require.NoError(t, second.UpsertRules(ctx, []Rule{perChild}))
	require.Empty(t, store.counters, "the retried update deletes the rows the failed one left")
	require.NoError(t, second.UpsertRules(ctx, []Rule{rule}))
	acquireN(t, second, 1, now)
	second.Close()

	restarted := startedService(t, store)
	assert.Equal(t, int64(1), requestsUsed(t, restarted, now))
}
