package clustersync

import (
	"context"
	"errors"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStore is an in-memory Store whose reads and bumps can be made to fail.
type fakeStore struct {
	mu        sync.Mutex
	revisions map[string]int64
	readErr   error
	bumpErr   error
	bumps     []string
}

func newFakeStore(revisions map[string]int64) *fakeStore {
	return &fakeStore{revisions: maps.Clone(revisions)}
}

func (f *fakeStore) Bump(_ context.Context, entity string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bumps = append(f.bumps, entity)
	if f.bumpErr != nil {
		return f.bumpErr
	}
	if f.revisions == nil {
		f.revisions = map[string]int64{}
	}
	f.revisions[entity]++
	return nil
}

func (f *fakeStore) Revisions(context.Context) (map[string]int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return nil, f.readErr
	}
	return maps.Clone(f.revisions), nil
}

// countingRefresh counts calls and returns whatever err holds at the time.
type countingRefresh struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (c *countingRefresh) refresh(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return c.err
}

func (c *countingRefresh) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestSyncerSeedsFromCurrentRevisions(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(map[string]int64{"budgets": 7, "auth_keys": 3})
	syncer, err := NewSyncer(ctx, store)
	require.NoError(t, err)
	budgets, authKeys := &countingRefresh{}, &countingRefresh{}
	syncer.Register("budgets", budgets.refresh)
	syncer.Register("auth_keys", authKeys.refresh)

	syncer.poll(ctx)

	assert.Zero(t, budgets.count(), "revisions present at startup were already loaded")
	assert.Zero(t, authKeys.count())
}

func TestSyncerRefreshesOnlyChangedEntities(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(map[string]int64{"budgets": 1})
	syncer, err := NewSyncer(ctx, store)
	require.NoError(t, err)
	budgets, rateLimits := &countingRefresh{}, &countingRefresh{}
	syncer.Register("budgets", budgets.refresh)
	syncer.Register("rate_limits", rateLimits.refresh)

	notify := syncer.Notifier("budgets")
	notify(ctx)
	syncer.poll(ctx)
	assert.Equal(t, 1, budgets.count())
	assert.Zero(t, rateLimits.count())

	// Unchanged since the last poll: nothing to do.
	syncer.poll(ctx)
	assert.Equal(t, 1, budgets.count())

	// An entity bumped for the first time is a change too.
	syncer.Notifier("rate_limits")(ctx)
	syncer.poll(ctx)
	assert.Equal(t, 1, budgets.count())
	assert.Equal(t, 1, rateLimits.count())
}

func TestSyncerRetriesFailedRefresh(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(nil)
	syncer, err := NewSyncer(ctx, store)
	require.NoError(t, err)
	budgets := &countingRefresh{err: errors.New("database is down")}
	syncer.Register("budgets", budgets.refresh)

	syncer.Notifier("budgets")(ctx)
	syncer.poll(ctx)
	syncer.poll(ctx)
	assert.Equal(t, 2, budgets.count(), "a failed refresh is retried on the next tick")

	budgets.mu.Lock()
	budgets.err = nil
	budgets.mu.Unlock()
	syncer.poll(ctx)
	syncer.poll(ctx)
	assert.Equal(t, 3, budgets.count(), "a successful refresh marks the revision applied")
}

func TestSyncerSkipsTickWhenRevisionsCannotBeRead(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(nil)
	syncer, err := NewSyncer(ctx, store)
	require.NoError(t, err)
	budgets := &countingRefresh{}
	syncer.Register("budgets", budgets.refresh)
	syncer.Notifier("budgets")(ctx)

	store.mu.Lock()
	store.readErr = errors.New("connection refused")
	store.mu.Unlock()
	syncer.poll(ctx)
	assert.Zero(t, budgets.count())

	store.mu.Lock()
	store.readErr = nil
	store.mu.Unlock()
	syncer.poll(ctx)
	assert.Equal(t, 1, budgets.count())
}

func TestSyncerIgnoresUnregisteredEntities(t *testing.T) {
	ctx := context.Background()
	syncer, err := NewSyncer(ctx, newFakeStore(nil))
	require.NoError(t, err)
	syncer.Notifier("mcp_servers")(ctx)

	assert.NotPanics(t, func() { syncer.poll(ctx) })
}

func TestNewSyncerFailsWhenRevisionsCannotBeRead(t *testing.T) {
	store := newFakeStore(nil)
	store.readErr = errors.New("no such table")
	_, err := NewSyncer(context.Background(), store)
	require.Error(t, err)

	_, err = NewSyncer(context.Background(), nil)
	require.Error(t, err)
}

func TestSyncerStartAndClose(t *testing.T) {
	ctx := context.Background()
	syncer, err := NewSyncer(ctx, newFakeStore(nil))
	require.NoError(t, err)
	syncer.interval = 5 * time.Millisecond
	budgets := &countingRefresh{}
	syncer.Register("budgets", budgets.refresh)

	syncer.Start(ctx)
	syncer.Start(ctx) // idempotent
	syncer.Notifier("budgets")(ctx)
	require.Eventually(t, func() bool { return budgets.count() == 1 }, time.Second, 5*time.Millisecond)

	require.NoError(t, syncer.Close())
	require.NoError(t, syncer.Close())
	syncer.Start(ctx) // a closed syncer stays closed
	syncer.Notifier("budgets")(ctx)
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, 1, budgets.count())
}

func TestNilSyncerIsInert(t *testing.T) {
	var syncer *Syncer
	syncer.Register("budgets", func(context.Context) error { return nil })
	syncer.Start(context.Background())
	assert.Nil(t, syncer.Notifier("budgets"))
	assert.NoError(t, syncer.Close())
}

func TestSyncerRetriesFailedAnnouncements(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(nil)
	syncer, err := NewSyncer(ctx, store)
	require.NoError(t, err)
	budgets := &countingRefresh{}
	syncer.Register("budgets", budgets.refresh)
	notify := syncer.Notifier("budgets")

	store.mu.Lock()
	store.bumpErr = errors.New("database unavailable")
	store.mu.Unlock()
	notify(ctx)
	notify(ctx)
	syncer.poll(ctx)
	revisions, err := store.Revisions(ctx)
	require.NoError(t, err)
	assert.Zero(t, revisions["budgets"], "nothing is announced while bumps fail")

	store.mu.Lock()
	store.bumpErr = nil
	store.mu.Unlock()
	syncer.poll(ctx)
	revisions, err = store.Revisions(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), revisions["budgets"], "the failed announcements are retried, coalesced")
	assert.Equal(t, 1, budgets.count())

	syncer.poll(ctx)
	revisions, err = store.Revisions(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), revisions["budgets"], "an announced change is not announced again")
}

func TestSyncerStopsRetryingAnnouncementsAfterFirstFailure(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(nil)
	syncer, err := NewSyncer(ctx, store)
	require.NoError(t, err)

	store.mu.Lock()
	store.bumpErr = errors.New("database unavailable")
	store.mu.Unlock()
	for _, entity := range []string{"budgets", "auth_keys", "rate_limits"} {
		syncer.Notifier(entity)(ctx)
	}

	store.mu.Lock()
	store.bumps = nil
	store.mu.Unlock()
	syncer.poll(ctx)
	store.mu.Lock()
	assert.Equal(t, []string{"budgets"}, store.bumps, "the oldest failure is retried first and a failure ends the round")
	store.bumps = nil
	store.mu.Unlock()

	syncer.poll(ctx)
	store.mu.Lock()
	assert.Equal(t, []string{"auth_keys"}, store.bumps, "an entity that failed again moves behind the others")
	store.bumps = nil
	store.bumpErr = nil
	store.mu.Unlock()

	syncer.poll(ctx)
	store.mu.Lock()
	assert.Equal(t, []string{"rate_limits", "budgets", "auth_keys"}, store.bumps, "a healthy store drains every pending announcement")
	store.mu.Unlock()
}

func TestSyncerCloseFlushesPendingAnnouncements(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(nil)
	syncer, err := NewSyncer(ctx, store)
	require.NoError(t, err)

	store.mu.Lock()
	store.bumpErr = errors.New("database unavailable")
	store.mu.Unlock()
	syncer.Notifier("budgets")(ctx)

	store.mu.Lock()
	store.bumpErr = nil
	store.mu.Unlock()
	require.NoError(t, syncer.Close())

	revisions, err := store.Revisions(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), revisions["budgets"], "a pending announcement is flushed on close")
}
