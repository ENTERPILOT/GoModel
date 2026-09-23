package budget

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeClock drives the spend cache's TTL in tests.
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

func newCachedTestService(t *testing.T, store *fakeStore, options ...ServiceOption) (*Service, *fakeClock) {
	t.Helper()
	service, err := NewService(context.Background(), store, options...)
	require.NoError(t, err)
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}
	if service.spends != nil {
		service.spends.now = clock.Now
	}
	return service, clock
}

func teamBudgetStore(spent *float64) *fakeStore {
	return &fakeStore{
		budgets: []Budget{{Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodDailySeconds, Amount: 10}},
		sum: func(SpendWindow) (float64, bool, error) {
			return *spent, true, nil
		},
	}
}

func TestServiceCheckReusesSpendWithinTTL(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)
	spent := 1.0
	store := teamBudgetStore(&spent)
	service, clock := newCachedTestService(t, store)

	require.NoError(t, service.Check(ctx, path("/team/app"), now))
	spent = 20
	require.NoError(t, service.Check(ctx, path("/team/app"), now.Add(time.Second)), "cached spend is reused")
	assert.Equal(t, 1, store.sumCalls)

	clock.Advance(defaultSpendCacheTTL)
	var exceeded *ExceededError
	require.ErrorAs(t, service.Check(ctx, path("/team/app"), now.Add(2*time.Second)), &exceeded)
	assert.Equal(t, 2, store.sumCalls)
}

func TestServiceInvalidateSpendForcesFreshSum(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)
	spent := 1.0
	store := teamBudgetStore(&spent)
	service, _ := newCachedTestService(t, store)

	require.NoError(t, service.Check(ctx, path("/team/app"), now))
	spent = 20
	service.InvalidateSpend()

	var exceeded *ExceededError
	require.ErrorAs(t, service.Check(ctx, path("/team/app"), now), &exceeded)
	assert.Equal(t, 2, store.sumCalls)
}

func TestServiceStatusesAlwaysSumFreshAndRefreshCache(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)
	spent := 1.0
	store := teamBudgetStore(&spent)
	service, _ := newCachedTestService(t, store)

	require.NoError(t, service.Check(ctx, path("/team/app"), now))
	spent = 20
	statuses, err := service.StatusesFor(ctx, path("/team/app"), now)
	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.InDelta(t, 20, statuses[0].Spent, 0)

	var exceeded *ExceededError
	require.ErrorAs(t, service.Check(ctx, path("/team/app"), now), &exceeded, "statuses refreshed the cache")
	assert.Equal(t, 2, store.sumCalls)
}

func TestServiceResetMissesSpendCache(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)
	spent := 1.0
	store := teamBudgetStore(&spent)
	service, _ := newCachedTestService(t, store)

	require.NoError(t, service.Check(ctx, path("/team/app"), now))
	resetAt := now.Add(-time.Minute)
	store.budgets[0].LastResetAt = &resetAt
	require.NoError(t, service.Refresh(ctx))

	require.NoError(t, service.Check(ctx, path("/team/app"), now))
	assert.Equal(t, 2, store.sumCalls, "a reset moves the window start")
	assert.Equal(t, resetAt, store.lastWindows[0].Start)
}

func TestServiceSpendCacheDisabled(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, time.April, 25, 12, 0, 0, 0, time.UTC)
	spent := 1.0
	store := teamBudgetStore(&spent)
	service, _ := newCachedTestService(t, store, WithSpendCacheTTL(0))

	require.NoError(t, service.Check(ctx, path("/team/app"), now))
	require.NoError(t, service.Check(ctx, path("/team/app"), now))
	assert.Equal(t, 2, store.sumCalls)
	service.InvalidateSpend()
}

func TestSpendCacheDropsResultsSummedBeforeClear(t *testing.T) {
	cache := newSpendCache(time.Minute)
	windows := []SpendWindow{{Scope: ScopeUserPath, Subject: "/team", Start: time.Unix(100, 0)}}

	_, misses, generation := cache.get(windows)
	require.Equal(t, []int{0}, misses)
	cache.clear() // a usage flush lands while the query runs
	cache.put(windows, []Spend{{Total: 1, HasUsage: true}}, generation)

	_, misses, _ = cache.get(windows)
	assert.Equal(t, []int{0}, misses)
}

func TestSpendCacheSweepsExpiredWindows(t *testing.T) {
	cache := newSpendCache(time.Second)
	clock := &fakeClock{now: time.Unix(1_000_000, 0)}
	cache.now = clock.Now
	old := []SpendWindow{{Scope: ScopeUserPath, Subject: "/team/a", Start: time.Unix(100, 0)}}
	cache.put(old, []Spend{{}}, 0)

	clock.Advance(time.Second)
	cache.put([]SpendWindow{{Scope: ScopeUserPath, Subject: "/team/b", Start: time.Unix(100, 0)}}, []Spend{{}}, 0)
	assert.Len(t, cache.entries, 1)
}
