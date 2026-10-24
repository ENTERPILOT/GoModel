package ratelimit

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSnapshotRoundTripPreservesEstimate(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	period := PeriodHourSeconds
	rule := Rule{
		Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: period,
		MaxRequests: new(int64(100)), MaxTokens: new(int64(500)),
	}

	src := newLimiter()
	_, _, err := src.admit([]Rule{rule}, now)
	require.Nil(t, err)

	src.recordTokens([]Rule{rule}, 40, now)
	wantReq := src.status(rule, now).RequestsUsed
	wantTok := src.status(rule, now).TokensUsed

	snaps := src.snapshot([]Rule{rule})
	require.Len(t, snaps, 1)
	require.Empty(t, snaps[0].Partition)

	dst := newLimiter()
	dst.restore(snaps, []Rule{rule}, now)
	got := dst.status(rule, now).RequestsUsed
	require.Equal(t, wantReq, got)
	got = dst.status(rule, now).TokensUsed
	require.Equal(t, wantTok, got)
}

func TestSnapshotSkipsConcurrentAndExpiredChild(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	shared := Rule{
		Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodConcurrent,
		MaxRequests: new(int64(3)),
	}
	template := Rule{
		Scope: ScopeUserPath, Subject: "/customers", PerChild: true,
		PeriodSeconds: PeriodHourSeconds, MaxRequests: new(int64(10)),
	}
	child, ok := template.resolve(Subjects{UserPath: "/customers/alice"})
	require.True(t, ok)

	src := newLimiter()
	_, _, err := src.admit([]Rule{shared, child}, now)
	require.Nil(t, err)

	// Force the child window into the distant past so restore drops it.
	src.mu.Lock()
	for _, counter := range src.requests {
		if counter != nil {
			counter.windowStart = now.Unix() - 10*PeriodHourSeconds
		}
	}
	src.mu.Unlock()

	snaps := src.snapshot([]Rule{shared, template})
	for _, snap := range snaps {
		require.NotEqual(t, PeriodConcurrent, snap.PeriodSeconds)
	}

	dst := newLimiter()
	dst.restore(snaps, []Rule{template}, now)
	got := dst.status(child, now).RequestsUsed
	require.Equal(t, int64(0), got)
}

func TestSnapshotIsolatesPerChildPartitions(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	template := Rule{
		Scope: ScopeUserPath, Subject: "/customers", PerChild: true,
		PeriodSeconds: PeriodHourSeconds, MaxRequests: new(int64(1)),
	}
	alice, _ := template.resolve(Subjects{UserPath: "/customers/alice/app"})
	bob, _ := template.resolve(Subjects{UserPath: "/customers/bob"})

	src := newLimiter()
	_, _, err := src.admit([]Rule{alice}, now)
	require.Nil(t, err)
	_, _, err = src.admit([]Rule{bob}, now)
	require.Nil(t, err)

	snaps := src.snapshot([]Rule{template})
	require.Len(t, snaps, 2)

	dst := newLimiter()
	dst.restore(snaps, []Rule{template}, now)
	_, _, err = dst.admit([]Rule{alice}, now)
	require.NotNil(t, err)
	_, _, err = dst.admit([]Rule{bob}, now)
	require.NotNil(t, err)
}

func TestStartLoadsAndCloseFlushes(t *testing.T) {
	now := time.Now().UTC()
	rule := Rule{
		Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodHourSeconds,
		MaxRequests: new(int64(1)), Source: SourceManual,
	}
	store := &memStore{}
	err := store.UpsertRules(context.Background(), []Rule{rule})
	require.NoError(t, err)

	first, err := NewService(context.Background(), store)
	require.NoError(t, err)
	_, err = first.Acquire(onPath("/team"), now)
	require.NoError(t, err)
	require.Empty(t, store.counters)

	first.Start(context.Background())
	first.Close()
	require.Len(t, store.counters, 1)

	second, err := NewService(context.Background(), store)
	require.NoError(t, err)

	t.Cleanup(second.Close)
	second.Start(context.Background())
	_, err = second.Acquire(onPath("/team"), now)
	require.Error(t, err)
}

// TestServiceWithoutStartNeverWrites covers both halves of the "not this
// generation" rule: admission never touches storage, and a service that was
// built but never started writes nothing on the way out either — a discarded
// reload replacement must not flush its empty windows over the live ones.
func TestServiceWithoutStartNeverWrites(t *testing.T) {
	store := &recordingStore{}
	err := store.UpsertRules(context.Background(), []Rule{{
		Subject: "/", PeriodSeconds: PeriodHourSeconds, MaxRequests: new(int64(5)), Source: SourceManual,
	}})
	require.NoError(t, err)

	service, err := NewService(context.Background(), store)
	require.NoError(t, err)
	_, err = service.Acquire(onPath("/"), time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, int64(0), store.saves.Load())

	service.Close()
	require.Equal(t, int64(0), store.saves.Load())
}

func TestResetClearsPersistedWindow(t *testing.T) {
	now := time.Now().UTC()
	rule := Rule{
		Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodHourSeconds,
		MaxRequests: new(int64(1)), Source: SourceManual,
	}
	store := &memStore{}
	err := store.UpsertRules(context.Background(), []Rule{rule})
	require.NoError(t, err)

	service, err := NewService(context.Background(), store)
	require.NoError(t, err)

	service.Start(context.Background())
	_, err = service.Acquire(onPath("/team"), now)
	require.NoError(t, err)
	err = service.ResetRule(ScopeUserPath, "/team", PeriodHourSeconds)
	require.NoError(t, err)

	service.Close()

	next, err := NewService(context.Background(), store)
	require.NoError(t, err)

	t.Cleanup(next.Close)
	next.Start(context.Background())
	_, err = next.Acquire(onPath("/team"), now)
	require.NoError(t, err)
}

func TestRestoreIgnoresSharedRowOnPerChildRule(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	template := Rule{
		Scope: ScopeUserPath, Subject: "/customers", PerChild: true,
		PeriodSeconds: PeriodHourSeconds, MaxRequests: new(int64(1)),
	}
	dst := newLimiter()
	dst.restore([]WindowSnapshot{{
		Scope: string(ScopeUserPath), Subject: "/customers", Partition: "",
		PeriodSeconds: PeriodHourSeconds, RequestsWindowStart: now.Unix(), RequestsCurrent: 1,
	}}, []Rule{template}, now)
	child, _ := template.resolve(Subjects{UserPath: "/customers/alice"})
	_, _, err := dst.admit([]Rule{child}, now)
	require.Nil(t, err)
}

func TestFailedLoadDoesNotReplacePersistedWindows(t *testing.T) {
	now := time.Now().Unix()
	store := &failLoadStore{err: errors.New("store down")}
	store.counters = []WindowSnapshot{{
		Scope: string(ScopeUserPath), Subject: "/team", PeriodSeconds: PeriodHourSeconds,
		RequestsWindowStart: now, RequestsCurrent: 9,
	}}
	err := store.UpsertRules(context.Background(), []Rule{{
		Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodHourSeconds,
		MaxRequests: new(int64(10)), Source: SourceManual,
	}})
	require.NoError(t, err)

	service, err := NewService(context.Background(), store, WithFlushInterval(10*time.Millisecond))
	require.NoError(t, err)

	service.Start(context.Background())
	_, err = service.Acquire(onPath("/team"), time.Now().UTC())
	require.NoError(t, err)

	service.Close()
	time.Sleep(30 * time.Millisecond)
	require.Len(t, store.counters, 1)
	require.Equal(t, int64(9), store.counters[0].RequestsCurrent)
}

func TestStartIsIdempotentAndCloseStopsTheLoop(t *testing.T) {
	store := &recordingStore{}
	err := store.UpsertRules(context.Background(), []Rule{{
		Subject: "/", PeriodSeconds: PeriodHourSeconds, MaxRequests: new(int64(50)), Source: SourceManual,
	}})
	require.NoError(t, err)

	service, err := NewService(context.Background(), store, WithFlushInterval(15*time.Millisecond))
	require.NoError(t, err)

	service.Start(context.Background())
	service.Start(context.Background())
	service.Close()
	afterClose := store.saves.Load()
	time.Sleep(50 * time.Millisecond)
	got := store.saves.Load()
	require.Equal(t, afterClose, got)
}

func TestFlushIntervalWritesBeforeClose(t *testing.T) {
	store := &recordingStore{}
	err := store.UpsertRules(context.Background(), []Rule{{
		Subject: "/", PeriodSeconds: PeriodHourSeconds, MaxRequests: new(int64(50)), Source: SourceManual,
	}})
	require.NoError(t, err)

	service, err := NewService(context.Background(), store, WithFlushInterval(15*time.Millisecond))
	require.NoError(t, err)

	t.Cleanup(service.Close)
	_, err = service.Acquire(onPath("/"), time.Now().UTC())
	require.NoError(t, err)

	service.Start(context.Background())
	deadline := time.Now().Add(200 * time.Millisecond)
	for store.saves.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	require.NotEqual(t, int64(0), store.saves.Load())
}

func TestFlushIntervalZeroOnlyWritesOnClose(t *testing.T) {
	store := &recordingStore{}
	err := store.UpsertRules(context.Background(), []Rule{{
		Subject: "/", PeriodSeconds: PeriodHourSeconds, MaxRequests: new(int64(50)), Source: SourceManual,
	}})
	require.NoError(t, err)

	service, err := NewService(context.Background(), store, WithFlushInterval(0))
	require.NoError(t, err)
	_, err = service.Acquire(onPath("/"), time.Now().UTC())
	require.NoError(t, err)

	service.Start(context.Background())
	time.Sleep(30 * time.Millisecond)
	require.Equal(t, int64(0), store.saves.Load())

	service.Close()
	require.Equal(t, int64(1), store.saves.Load())
}

func TestCloseDuringLoadDoesNotRestore(t *testing.T) {
	now := time.Now().Unix()
	store := &blockingLoadStore{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	store.counters = []WindowSnapshot{{
		Scope: string(ScopeUserPath), Subject: "/team", PeriodSeconds: PeriodHourSeconds,
		RequestsWindowStart: now, RequestsCurrent: 1,
	}}
	err := store.UpsertRules(context.Background(), []Rule{{
		Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodHourSeconds,
		MaxRequests: new(int64(1)), Source: SourceManual,
	}})
	require.NoError(t, err)

	service, err := NewService(context.Background(), store)
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		service.Start(context.Background())
	}()
	<-store.started
	service.Close()
	close(store.release)
	<-done
	_, err = service.Acquire(onPath("/team"), time.Now().UTC())
	require.NoError(t, err)
}

func TestResetRuleReturnsPersistError(t *testing.T) {
	store := &failDeleteStore{err: errors.New("delete failed")}
	err := store.UpsertRules(context.Background(), []Rule{{
		Subject: "/team", PeriodSeconds: PeriodHourSeconds, MaxRequests: new(int64(1)), Source: SourceManual,
	}})
	require.NoError(t, err)

	service, err := NewService(context.Background(), store)
	require.NoError(t, err)

	t.Cleanup(service.Close)
	require.Error(t, service.ResetRule(ScopeUserPath, "/team", PeriodHourSeconds))
}

type recordingStore struct {
	memStore
	saves atomic.Int64
}

func (s *recordingStore) SaveCounters(ctx context.Context, snapshots []WindowSnapshot) error {
	s.saves.Add(1)
	return s.memStore.SaveCounters(ctx, snapshots)
}

type failLoadStore struct {
	memStore
	err error
}

func (s *failLoadStore) LoadCounters(context.Context) ([]WindowSnapshot, error) {
	return nil, s.err
}

type failDeleteStore struct {
	memStore
	err error
}

func (s *failDeleteStore) DeleteCounter(context.Context, RuleScope, string, int64) error {
	return s.err
}

type blockingLoadStore struct {
	memStore
	started chan struct{}
	release chan struct{}
}

func (s *blockingLoadStore) LoadCounters(context.Context) ([]WindowSnapshot, error) {
	close(s.started)
	<-s.release
	return s.memStore.LoadCounters(context.Background())
}

// TestDeleteRuleStopsEnforcingWhenSnapshotDeleteFails: the rule row is already
// gone, so the in-memory refresh has to happen even when the snapshot row
// cannot be removed. The caller still hears about the row.
func TestDeleteRuleStopsEnforcingWhenSnapshotDeleteFails(t *testing.T) {
	ctx := context.Background()
	store := &failDeleteStore{err: errors.New("delete failed")}
	err := store.UpsertRules(ctx, []Rule{{
		Subject: "/team", PeriodSeconds: PeriodHourSeconds, MaxRequests: new(int64(1)), Source: SourceManual,
	}})
	require.NoError(t, err)

	service, err := NewService(ctx, store)
	require.NoError(t, err)

	t.Cleanup(service.Close)

	require.Error(t, service.DeleteRule(ctx, ScopeUserPath, "/team", PeriodHourSeconds))
	rules := service.Rules()
	require.Empty(t, rules)

	// Two admissions: the deleted one-per-hour rule is no longer enforced.
	for i := range 2 {
		_, err := service.Acquire(onPath("/team"), time.Now().UTC())
		require.NoError(t, err, "Acquire %d after delete: %v", i, err)
	}
}
