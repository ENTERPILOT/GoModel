package ratelimit

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestSnapshotRoundTripPreservesEstimate(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	period := PeriodHourSeconds
	rule := Rule{
		Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: period,
		MaxRequests: new(int64(100)), MaxTokens: new(int64(500)),
	}

	src := newLimiter()
	if _, _, err := src.admit([]Rule{rule}, now); err != nil {
		t.Fatalf("admit: %v", err)
	}
	src.recordTokens([]Rule{rule}, 40, now)
	wantReq := src.status(rule, now).RequestsUsed
	wantTok := src.status(rule, now).TokensUsed

	snaps := src.snapshot([]Rule{rule})
	if len(snaps) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(snaps))
	}
	if snaps[0].Partition != "" {
		t.Fatalf("partition = %q, want empty", snaps[0].Partition)
	}

	dst := newLimiter()
	dst.restore(snaps, []Rule{rule}, now)
	if got := dst.status(rule, now).RequestsUsed; got != wantReq {
		t.Fatalf("requests used = %d, want %d", got, wantReq)
	}
	if got := dst.status(rule, now).TokensUsed; got != wantTok {
		t.Fatalf("tokens used = %d, want %d", got, wantTok)
	}
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
	if !ok {
		t.Fatal("resolve child")
	}

	src := newLimiter()
	if _, _, err := src.admit([]Rule{shared, child}, now); err != nil {
		t.Fatalf("admit: %v", err)
	}
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
		if snap.PeriodSeconds == PeriodConcurrent {
			t.Fatal("concurrent snapshot written")
		}
	}

	dst := newLimiter()
	dst.restore(snaps, []Rule{template}, now)
	if got := dst.status(child, now).RequestsUsed; got != 0 {
		t.Fatalf("expired child restored used = %d, want 0", got)
	}
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
	if _, _, err := src.admit([]Rule{alice}, now); err != nil {
		t.Fatalf("alice admit: %v", err)
	}
	if _, _, err := src.admit([]Rule{bob}, now); err != nil {
		t.Fatalf("bob admit: %v", err)
	}

	snaps := src.snapshot([]Rule{template})
	if len(snaps) != 2 {
		t.Fatalf("snapshots = %d, want 2", len(snaps))
	}

	dst := newLimiter()
	dst.restore(snaps, []Rule{template}, now)
	if _, _, err := dst.admit([]Rule{alice}, now); err == nil {
		t.Fatal("alice should be exhausted after restore")
	}
	if _, _, err := dst.admit([]Rule{bob}, now); err == nil {
		t.Fatal("bob should be exhausted after restore")
	}
}

func TestStartLoadsAndCloseFlushes(t *testing.T) {
	now := time.Now().UTC()
	rule := Rule{
		Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodHourSeconds,
		MaxRequests: new(int64(1)), Source: SourceManual,
	}
	store := &memStore{}
	if err := store.UpsertRules(context.Background(), []Rule{rule}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	first, err := NewService(context.Background(), store)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if _, err := first.Acquire(onPath("/team"), now); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if len(store.counters) != 0 {
		t.Fatal("New without Start wrote counters")
	}
	first.Start(context.Background())
	first.Close()
	if len(store.counters) != 1 {
		t.Fatalf("counters after Close = %d, want 1", len(store.counters))
	}

	second, err := NewService(context.Background(), store)
	if err != nil {
		t.Fatalf("second NewService: %v", err)
	}
	t.Cleanup(second.Close)
	second.Start(context.Background())
	if _, err := second.Acquire(onPath("/team"), now); err == nil {
		t.Fatal("restored window admitted a second request")
	}
}

func TestCloseWithoutStartDoesNotWrite(t *testing.T) {
	store := &recordingStore{memStore: memStore{}}
	if err := store.UpsertRules(context.Background(), []Rule{{
		Subject: "/", PeriodSeconds: PeriodHourSeconds, MaxRequests: new(int64(5)), Source: SourceManual,
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	service, err := NewService(context.Background(), store)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if _, err := service.Acquire(onPath("/"), time.Now().UTC()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	service.Close()
	if store.saves.Load() != 0 {
		t.Fatalf("saves = %d, want 0", store.saves.Load())
	}
}

func TestResetClearsPersistedWindow(t *testing.T) {
	now := time.Now().UTC()
	rule := Rule{
		Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodHourSeconds,
		MaxRequests: new(int64(1)), Source: SourceManual,
	}
	store := &memStore{}
	if err := store.UpsertRules(context.Background(), []Rule{rule}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	service, err := NewService(context.Background(), store)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	service.Start(context.Background())
	if _, err := service.Acquire(onPath("/team"), now); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := service.ResetRule(ScopeUserPath, "/team", PeriodHourSeconds); err != nil {
		t.Fatalf("ResetRule: %v", err)
	}
	service.Close()

	next, err := NewService(context.Background(), store)
	if err != nil {
		t.Fatalf("second NewService: %v", err)
	}
	t.Cleanup(next.Close)
	next.Start(context.Background())
	if _, err := next.Acquire(onPath("/team"), now); err != nil {
		t.Fatalf("Acquire after reset restore: %v", err)
	}
}

func TestAdmitDoesNotSave(t *testing.T) {
	store := &recordingStore{memStore: memStore{}}
	if err := store.UpsertRules(context.Background(), []Rule{{
		Subject: "/", PeriodSeconds: PeriodHourSeconds, MaxRequests: new(int64(5)), Source: SourceManual,
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	service, err := NewService(context.Background(), store)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(service.Close)
	if _, err := service.Acquire(onPath("/"), time.Now().UTC()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if store.saves.Load() != 0 {
		t.Fatalf("Admit saved %d times", store.saves.Load())
	}
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
	if _, _, err := dst.admit([]Rule{child}, now); err != nil {
		t.Fatalf("shared row applied to per-child rule: %v", err)
	}
}

func TestFailedLoadDoesNotReplacePersistedWindows(t *testing.T) {
	now := time.Now().Unix()
	store := &failLoadStore{err: errors.New("store down")}
	store.counters = []WindowSnapshot{{
		Scope: string(ScopeUserPath), Subject: "/team", PeriodSeconds: PeriodHourSeconds,
		RequestsWindowStart: now, RequestsCurrent: 9,
	}}
	if err := store.UpsertRules(context.Background(), []Rule{{
		Scope: ScopeUserPath, Subject: "/team", PeriodSeconds: PeriodHourSeconds,
		MaxRequests: new(int64(10)), Source: SourceManual,
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	service, err := NewService(context.Background(), store, WithFlushInterval(10*time.Millisecond))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	service.Start(context.Background())
	if _, err := service.Acquire(onPath("/team"), time.Now().UTC()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	service.Close()
	time.Sleep(30 * time.Millisecond)
	if len(store.counters) != 1 || store.counters[0].RequestsCurrent != 9 {
		t.Fatalf("persisted windows = %+v, want the pre-failure snapshot", store.counters)
	}
}

func TestStartIsIdempotentAndCloseStopsTheLoop(t *testing.T) {
	store := &recordingStore{}
	if err := store.UpsertRules(context.Background(), []Rule{{
		Subject: "/", PeriodSeconds: PeriodHourSeconds, MaxRequests: new(int64(50)), Source: SourceManual,
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	service, err := NewService(context.Background(), store, WithFlushInterval(15*time.Millisecond))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	service.Start(context.Background())
	service.Start(context.Background())
	service.Close()
	afterClose := store.saves.Load()
	time.Sleep(50 * time.Millisecond)
	if got := store.saves.Load(); got != afterClose {
		t.Fatalf("saves after Close grew from %d to %d; an extra flush loop is still running", afterClose, got)
	}
}

func TestFlushIntervalWritesBeforeClose(t *testing.T) {
	store := &recordingStore{}
	if err := store.UpsertRules(context.Background(), []Rule{{
		Subject: "/", PeriodSeconds: PeriodHourSeconds, MaxRequests: new(int64(50)), Source: SourceManual,
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	service, err := NewService(context.Background(), store, WithFlushInterval(15*time.Millisecond))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(service.Close)
	if _, err := service.Acquire(onPath("/"), time.Now().UTC()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	service.Start(context.Background())
	deadline := time.Now().Add(200 * time.Millisecond)
	for store.saves.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if store.saves.Load() == 0 {
		t.Fatal("positive flush interval never wrote before Close")
	}
}

func TestFlushIntervalZeroOnlyWritesOnClose(t *testing.T) {
	store := &recordingStore{}
	if err := store.UpsertRules(context.Background(), []Rule{{
		Subject: "/", PeriodSeconds: PeriodHourSeconds, MaxRequests: new(int64(50)), Source: SourceManual,
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	service, err := NewService(context.Background(), store, WithFlushInterval(0))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if _, err := service.Acquire(onPath("/"), time.Now().UTC()); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	service.Start(context.Background())
	time.Sleep(30 * time.Millisecond)
	if store.saves.Load() != 0 {
		t.Fatalf("interval 0 wrote %d times before Close", store.saves.Load())
	}
	service.Close()
	if store.saves.Load() != 1 {
		t.Fatalf("saves after Close = %d, want 1", store.saves.Load())
	}
}

func TestResetRuleReturnsPersistError(t *testing.T) {
	store := &failDeleteStore{err: errors.New("delete failed")}
	if err := store.UpsertRules(context.Background(), []Rule{{
		Subject: "/team", PeriodSeconds: PeriodHourSeconds, MaxRequests: new(int64(1)), Source: SourceManual,
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	service, err := NewService(context.Background(), store)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(service.Close)
	if err := service.ResetRule(ScopeUserPath, "/team", PeriodHourSeconds); err == nil {
		t.Fatal("ResetRule() error = nil, want persist error")
	}
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
