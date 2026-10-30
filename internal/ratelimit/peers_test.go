package ratelimit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock is a settable clock shared by services and their store, so
// tests age rows without sleeping.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// clusterStore is a memStore on a fake clock, recording the since of every
// load.
type clusterStore struct {
	memStore
	clock *fakeClock
	loads []int64
	saves int
}

func newClusterStore(t *testing.T, rules ...Rule) *clusterStore {
	t.Helper()
	clock := &fakeClock{now: windowBase.Add(10 * time.Second)}
	store := &clusterStore{clock: clock}
	store.now = clock.Now
	require.NoError(t, store.UpsertRules(context.Background(), rules))
	return store
}

func (s *clusterStore) LoadCounters(ctx context.Context, since int64) ([]WindowSnapshot, error) {
	s.loads = append(s.loads, since)
	return s.memStore.LoadCounters(ctx, since)
}

func (s *clusterStore) SaveCounters(ctx context.Context, snapshots []WindowSnapshot) error {
	s.saves++
	return s.memStore.SaveCounters(ctx, snapshots)
}

// replica starts a service on store's clock. Its flush interval only sets
// the in-flight liveness bound: the loop never runs, tests call flush.
func replica(t *testing.T, store *clusterStore) *Service {
	t.Helper()
	service, err := NewService(context.Background(), store, WithFlushInterval(0))
	require.NoError(t, err)
	t.Cleanup(service.Close)
	service.now = store.clock.Now
	service.Start(context.Background())
	service.flushInterval = time.Second
	return service
}

var teamOnOpenAI = Subjects{UserPath: "/team", Provider: "openai", Model: "openai/gpt-4o"}

func TestReplicasEnforceOneLimitTogether(t *testing.T) {
	ctx := context.Background()
	store := newClusterStore(t,
		Rule{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodHourSeconds, MaxRequests: new(int64(10)), MaxTokens: new(int64(1000)), Source: SourceManual},
		Rule{Scope: ScopeProvider, Subject: "openai", PeriodSeconds: PeriodHourSeconds, MaxRequests: new(int64(100)), Source: SourceManual},
	)
	a := replica(t, store)
	b := replica(t, store)
	now := store.clock.Now

	// Each stays under the limit on its own.
	for range 6 {
		_, err := a.Acquire(teamOnOpenAI, now())
		require.NoError(t, err)
	}
	for range 4 {
		_, err := b.Acquire(teamOnOpenAI, now())
		require.NoError(t, err)
	}
	a.RecordTokens(onPath("/team"), 300, now())
	b.RecordTokens(onPath("/team"), 200, now())
	require.Equal(t, int64(6), requestsUsedFor(t, a, "/team", now()))

	// One flush cycle later both see the cluster's count and refuse.
	a.flush(ctx)
	b.flush(ctx)
	a.flush(ctx)
	for _, service := range []*Service{a, b} {
		assert.Equal(t, int64(10), requestsUsedFor(t, service, "/team", now()))
		statuses := service.StatusesForUserPath("/team", now())
		require.Len(t, statuses, 1)
		assert.Equal(t, int64(500), statuses[0].TokensUsed)
		_, err := service.Acquire(teamOnOpenAI, now())
		var exceeded *ExceededError
		require.ErrorAs(t, err, &exceeded)
		assert.Equal(t, int64(10), exceeded.Observed)
		assert.Greater(t, exceeded.RetryAfter, time.Duration(0))
	}

	// The provider rule, with room left, sees the cluster too: its
	// headers and the routing probe count both replicas.
	store.rules = store.rules[1:]
	require.NoError(t, a.Refresh(ctx))
	reservation, err := a.Acquire(teamOnOpenAI, now())
	require.NoError(t, err)
	assert.Equal(t, int64(100-11), reservation.Headers().RequestRemaining)
	assert.True(t, a.routeAvailableAt("openai", "openai/gpt-4o", now()))

	// Rows hold each replica's own counts only.
	byWriter := make(map[string]int64)
	for _, row := range store.counters {
		if row.Subject == "/team" {
			byWriter[row.Writer] = row.RequestsCurrent
		}
	}
	assert.Equal(t, map[string]int64{a.writer: 6, b.writer: 4}, byWriter)
}

func requestsUsedFor(t *testing.T, service *Service, path string, now time.Time) int64 {
	t.Helper()
	statuses := service.StatusesForUserPath(path, now)
	require.Len(t, statuses, 1)
	return statuses[0].RequestsUsed
}

func TestResetClearsPeerCountsOnEveryReplica(t *testing.T) {
	ctx := context.Background()
	store := newClusterStore(t, teamRule(false, 100))
	a := replica(t, store)
	b := replica(t, store)
	now := store.clock.Now
	a.SetChangeNotifier(func(ctx context.Context) { require.NoError(t, b.Refresh(ctx)) })

	acquireN(t, a, 5, now())
	acquireN(t, b, 3, now())
	a.flush(ctx)
	b.flush(ctx)
	a.flush(ctx)
	require.Equal(t, int64(8), requestsUsed(t, a, now()))
	require.Equal(t, int64(8), requestsUsed(t, b, now()))

	require.NoError(t, a.ResetRule(ctx, ScopeUserPath, "/team", PeriodDaySeconds))
	assert.Equal(t, int64(0), requestsUsed(t, a, now()), "the replica that reset forgets the peer's count")
	assert.Equal(t, int64(0), requestsUsed(t, b, now()), "the peer forgets the resetting replica's count")

	// Still cached rows of the old epoch never come back.
	a.flush(ctx)
	b.flush(ctx)
	assert.Equal(t, int64(0), requestsUsed(t, a, now()))
	assert.Equal(t, int64(0), requestsUsed(t, b, now()))

	// A mode round trip on one replica clears the other's view the same way.
	acquireN(t, a, 2, now())
	a.flush(ctx)
	b.flush(ctx)
	require.Equal(t, int64(2), requestsUsed(t, b, now()))
	require.NoError(t, a.UpsertRules(ctx, []Rule{teamRule(true, 100)}))
	require.NoError(t, a.UpsertRules(ctx, []Rule{teamRule(false, 100)}))
	assert.Equal(t, int64(0), requestsUsed(t, b, now()))
	b.flush(ctx)
	assert.Equal(t, int64(0), requestsUsed(t, b, now()))
}

func TestPeerInFlightCountsWhileFreshAndExpires(t *testing.T) {
	ctx := context.Background()
	store := newClusterStore(t, Rule{
		Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodConcurrent,
		MaxRequests: new(int64(2)), Source: SourceManual,
	})
	a := replica(t, store)
	b := replica(t, store)
	now := store.clock.Now

	first, err := a.Acquire(onPath("/team"), now())
	require.NoError(t, err)
	second, err := a.Acquire(onPath("/team"), now())
	require.NoError(t, err)
	a.flush(ctx)
	b.flush(ctx)
	statuses := b.StatusesForUserPath("/team", now())
	require.Len(t, statuses, 1)
	assert.Equal(t, int64(2), statuses[0].InFlight)
	_, err = b.Acquire(onPath("/team"), now())
	var exceeded *ExceededError
	require.ErrorAs(t, err, &exceeded, "the peer's slots count")
	assert.Equal(t, ScopeConcurrency, exceeded.Scope)

	// A release is published as it happens, not left to go stale.
	first.Release()
	a.flush(ctx)
	b.flush(ctx)
	held, err := b.Acquire(onPath("/team"), now())
	require.NoError(t, err)
	held.Release()

	// A peer that keeps flushing keeps its slot...
	store.clock.Advance(4 * time.Second)
	a.flush(ctx)
	store.clock.Advance(4 * time.Second)
	b.flush(ctx)
	held, err = b.Acquire(onPath("/team"), now())
	require.NoError(t, err)
	_, err = b.Acquire(onPath("/team"), now())
	require.Error(t, err, "a live peer's slot still counts")
	held.Release()

	// ...and one that stops, crashed, loses it once the row goes stale.
	store.clock.Advance(minInFlightLiveness + time.Second)
	b.flush(ctx)
	for range 2 {
		_, err = b.Acquire(onPath("/team"), now())
		require.NoError(t, err)
	}
	second.Release()
}

func TestIncrementalLoadKeepsUnchangedRowsAndDropsRolledOutOnes(t *testing.T) {
	ctx := context.Background()
	store := newClusterStore(t, Rule{
		Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodHourSeconds,
		MaxRequests: new(int64(100)), Source: SourceManual,
	})
	a := replica(t, store)
	b := replica(t, store)
	now := store.clock.Now
	require.Equal(t, []int64{0, 0}, store.loads, "Start loads every row")

	acquireN(t, a, 3, now())
	a.flush(ctx)
	b.flush(ctx)
	require.Equal(t, int64(3), requestsUsed(t, b, now()))
	since := store.loads[len(store.loads)-1]
	assert.Equal(t, now().Add(-peerLoadOverlap).Unix(), since, "a flush loads only rows saved since the last load")

	// a stops counting: its row is not rewritten, so loads stop returning
	// it, and b keeps counting it from the cache.
	saves := store.saves
	a.flush(ctx)
	assert.Equal(t, saves, store.saves, "nothing changed, nothing is written")
	store.clock.Advance(30 * time.Second)
	b.flush(ctx)
	b.flush(ctx)
	rows, err := store.memStore.LoadCounters(ctx, store.loads[len(store.loads)-1])
	require.NoError(t, err)
	require.Empty(t, rows)
	assert.Equal(t, int64(3), requestsUsed(t, b, now()))

	// A change is picked up by the next load.
	acquireN(t, a, 2, now())
	a.flush(ctx)
	b.flush(ctx)
	assert.Equal(t, int64(5), requestsUsed(t, b, now()))

	// Once a minute the load is a full one.
	store.clock.Advance(peerFullLoadInterval)
	b.flush(ctx)
	assert.Equal(t, int64(0), store.loads[len(store.loads)-1])
	assert.Equal(t, int64(5), requestsUsed(t, b, now()))

	// Two periods on, the row holds nothing and leaves the cache.
	store.clock.Advance(2 * time.Hour)
	b.flush(ctx)
	assert.Equal(t, int64(0), requestsUsed(t, b, now()))
	b.persistMu.Lock()
	assert.Empty(t, b.peers.rows)
	b.persistMu.Unlock()
}

// failingSaveStore fails SaveCounters while err is set.
type failingSaveStore struct {
	*clusterStore
	err error
}

func (s *failingSaveStore) SaveCounters(ctx context.Context, snapshots []WindowSnapshot) error {
	if s.err != nil {
		return s.err
	}
	return s.clusterStore.SaveCounters(ctx, snapshots)
}

// Rows are written only when they change, so a failed save must leave them
// to the next one rather than lose them.
func TestFailedSaveIsRetried(t *testing.T) {
	ctx := context.Background()
	store := &failingSaveStore{clusterStore: newClusterStore(t,
		teamRule(false, 100),
		Rule{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodConcurrent, MaxRequests: new(int64(5)), Source: SourceManual},
	)}
	service, err := NewService(ctx, store, WithFlushInterval(0))
	require.NoError(t, err)
	t.Cleanup(service.Close)
	service.now = store.clock.Now
	service.Start(ctx)

	held, err := service.Acquire(onPath("/team"), store.clock.Now())
	require.NoError(t, err)
	service.flush(ctx)
	held.Release()
	store.err = errors.New("store down")
	held, err = service.Acquire(onPath("/team"), store.clock.Now())
	require.NoError(t, err)
	held.Release()
	service.flush(ctx)
	store.err = nil
	service.flush(ctx)

	byPeriod := make(map[int64]WindowSnapshot)
	for _, row := range store.counters {
		byPeriod[row.PeriodSeconds] = row
	}
	assert.Equal(t, int64(2), byPeriod[PeriodDaySeconds].RequestsCurrent)
	assert.Equal(t, int64(0), byPeriod[PeriodConcurrent].InFlight, "the released slot is published after all")
}
