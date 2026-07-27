package virtualmodels

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
)

func upsertBalancedVM(t *testing.T, svc *Service, strategy string, affinity *bool) {
	t.Helper()
	if err := svc.Upsert(context.Background(), VirtualModel{
		Source:          "smart",
		Strategy:        strategy,
		SessionAffinity: affinity,
		Targets: []Target{
			{Provider: "openai", Model: "gpt-4o"},
			{Provider: "anthropic", Model: "claude"},
			{Provider: "groq", Model: "llama"},
		},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
}

// resolveSession resolves source once with a session id and returns the chosen target.
func resolveSession(t *testing.T, svc *Service, source, sessionID string) string {
	t.Helper()
	resolution, _, err := svc.resolveRequested(core.NewRequestedModelSelector(source, ""), "", false, sessionID)
	if err != nil {
		t.Fatalf("resolveRequested() error = %v", err)
	}
	return resolution.Resolved.QualifiedModel()
}

func TestSticky_SameSessionSameTarget(t *testing.T) {
	t.Parallel()
	for _, strategy := range []string{StrategyRoundRobin, StrategyCost} {
		t.Run(strategy, func(t *testing.T) {
			svc := newBalancingService(t)
			upsertBalancedVM(t, svc, strategy, nil)

			first := resolveSession(t, svc, "smart", "sess-a")
			for i := range 5 {
				if got := resolveSession(t, svc, "smart", "sess-a"); got != first {
					t.Fatalf("resolution %d = %q, want pinned %q", i, got, first)
				}
			}
		})
	}
}

func TestSticky_SessionsDistributeAcrossTargets(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertBalancedVM(t, svc, StrategyRoundRobin, nil)

	// Distinct sessions land on rotating targets; each stays pinned.
	a := resolveSession(t, svc, "smart", "sess-a")
	b := resolveSession(t, svc, "smart", "sess-b")
	if a == b {
		t.Fatalf("two fresh sessions landed on the same target %q, want rotation", a)
	}
	if got := resolveSession(t, svc, "smart", "sess-a"); got != a {
		t.Fatalf("sess-a moved from %q to %q", a, got)
	}
	if got := resolveSession(t, svc, "smart", "sess-b"); got != b {
		t.Fatalf("sess-b moved from %q to %q", b, got)
	}
}

func TestSticky_AffinityDisabledRestoresRotation(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	off := false
	upsertBalancedVM(t, svc, StrategyRoundRobin, &off)

	seen := make(map[string]bool)
	for range 3 {
		seen[resolveSession(t, svc, "smart", "sess-a")] = true
	}
	if len(seen) != 3 {
		t.Fatalf("with affinity off, one session saw %d targets, want 3 (rotation)", len(seen))
	}
}

func TestSticky_EmptySessionDoesNotPin(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertBalancedVM(t, svc, StrategyRoundRobin, nil)

	resolveSession(t, svc, "smart", "")
	if got := len(svc.sticky.entries); got != 0 {
		t.Fatalf("sticky entries = %d after sessionless resolution, want 0", got)
	}
}

func TestSticky_RepinsWhenPinnedTargetLosesCapacity(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertBalancedVM(t, svc, StrategyRoundRobin, nil)

	saturated := map[string]bool{}
	svc.SetTargetCapacity(func(qualified string) bool { return !saturated[qualified] })

	pinned := resolveSession(t, svc, "smart", "sess-a")
	saturated[pinned] = true

	repinned := resolveSession(t, svc, "smart", "sess-a")
	if repinned == pinned {
		t.Fatalf("session stayed on saturated target %q", pinned)
	}
	// The new pin holds even after the original target regains capacity.
	saturated[pinned] = false
	if got := resolveSession(t, svc, "smart", "sess-a"); got != repinned {
		t.Fatalf("session moved from re-pinned %q to %q", repinned, got)
	}
}

func TestSticky_SaturatedFallbackDoesNotPin(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertBalancedVM(t, svc, StrategyRoundRobin, nil)
	svc.SetTargetCapacity(func(string) bool { return false })

	// Every target saturated: the first declared target serves the honest-429
	// path and must not become the session's pin.
	if got := resolveSession(t, svc, "smart", "sess-a"); got != "openai/gpt-4o" {
		t.Fatalf("saturated fallback = %q, want first declared target", got)
	}
	if got := len(svc.sticky.entries); got != 0 {
		t.Fatalf("sticky entries = %d after saturated fallback, want 0", got)
	}
}

func TestSticky_TTLExpiry(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertBalancedVM(t, svc, StrategyRoundRobin, nil)

	current := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	svc.sticky.now = func() time.Time { return current }

	pinned := resolveSession(t, svc, "smart", "sess-a")
	current = current.Add(stickySessionTTL + time.Minute)

	// The expired pin is dropped: the strategy picks fresh (round robin has
	// advanced once, so the next pick differs from the original).
	if got := resolveSession(t, svc, "smart", "sess-a"); got == pinned {
		t.Fatalf("expired session still pinned to %q", pinned)
	}
}

// stickyProbe resolves without picking: it reports the existing viable pin or
// "" and never assigns, so tests can inspect state through the public seam.
func stickyProbe(sticky *stickySessions, source, session string) string {
	return sticky.resolve(source, session,
		func(string) bool { return true },
		func() string { return "" },
		false,
	)
}

// stickyAssign resolves with a fixed choice, pinning it.
func stickyAssign(sticky *stickySessions, source, session, qualified string) string {
	return sticky.resolve(source, session,
		func(string) bool { return true },
		func() string { return qualified },
		true,
	)
}

func TestSticky_ResolveRefreshesTTL(t *testing.T) {
	t.Parallel()
	sticky := &stickySessions{}
	current := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	sticky.now = func() time.Time { return current }

	stickyAssign(sticky, "smart", "sess-a", "openai/gpt-4o")
	// Touch the pin just before expiry, then advance past the original TTL.
	current = current.Add(stickySessionTTL - time.Minute)
	if got := stickyProbe(sticky, "smart", "sess-a"); got == "" {
		t.Fatal("pin expired early")
	}
	current = current.Add(stickySessionTTL - time.Minute)
	if got := stickyProbe(sticky, "smart", "sess-a"); got == "" {
		t.Fatal("refreshed pin expired: resolve must extend the TTL")
	}
}

// Concurrent first requests of one session must agree on a single target:
// lookup, strategy choice, and pin share one critical section.
func TestSticky_ConcurrentFirstRequestsAgree(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertBalancedVM(t, svc, StrategyRoundRobin, nil)

	const workers = 16
	results := make([]string, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			resolution, _, err := svc.resolveRequested(
				core.NewRequestedModelSelector("smart", ""), "", false, "sess-a")
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = resolution.Resolved.QualifiedModel()
		})
	}
	wg.Wait()

	for i := range workers {
		if errs[i] != nil {
			t.Fatalf("resolveRequested() error = %v", errs[i])
		}
		if results[i] != results[0] {
			t.Fatalf("concurrent resolutions disagree: %q vs %q", results[i], results[0])
		}
	}
}

func TestSticky_PruneDropsDeletedSources(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertBalancedVM(t, svc, StrategyRoundRobin, nil)

	resolveSession(t, svc, "smart", "sess-a")
	if len(svc.sticky.entries) != 1 {
		t.Fatalf("sticky entries = %d, want 1", len(svc.sticky.entries))
	}
	if err := svc.Delete(context.Background(), "smart"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if got := len(svc.sticky.entries); got != 0 {
		t.Fatalf("sticky entries = %d after source deletion, want 0", got)
	}
}

func TestSticky_EvictsSoonestAtCapacity(t *testing.T) {
	t.Parallel()
	sticky := &stickySessions{}
	current := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	sticky.now = func() time.Time { return current }

	for i := range maxStickySessions {
		stickyAssign(sticky, "smart", "sess-"+strconv.Itoa(i), "openai/gpt-4o")
		current = current.Add(time.Millisecond)
	}
	if len(sticky.entries) != maxStickySessions {
		t.Fatalf("entries = %d, want %d", len(sticky.entries), maxStickySessions)
	}
	stickyAssign(sticky, "smart", "one-more", "openai/gpt-4o")
	if len(sticky.entries) != maxStickySessions {
		t.Fatalf("entries = %d after eviction, want %d", len(sticky.entries), maxStickySessions)
	}
	// The oldest pin was evicted; the newest survives.
	if got := stickyProbe(sticky, "smart", "one-more"); got == "" {
		t.Fatal("newest pin missing after eviction")
	}
	if got := stickyProbe(sticky, "smart", "sess-0"); got != "" {
		t.Fatal("soonest-expiring pin survived eviction")
	}
}

// A multi-target redirect with only one target momentarily available must
// still pin: otherwise a target coming back online would let the strategy
// move an active session mid-conversation.
func TestSticky_PinsWhenOnlyOneTargetSupported(t *testing.T) {
	t.Parallel()
	catalog := balancingCatalog()
	catalog.stale = map[string]bool{"anthropic/claude": true, "groq/llama": true}
	svc, err := NewService(newSQLVMStore(t), catalog, true)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	upsertBalancedVM(t, svc, StrategyRoundRobin, nil)

	if got := resolveSession(t, svc, "smart", "sess-a"); got != "openai/gpt-4o" {
		t.Fatalf("sole supported target = %q, want openai/gpt-4o", got)
	}
	if got := len(svc.sticky.entries); got != 1 {
		t.Fatalf("sticky entries = %d, want the sole viable target pinned", got)
	}

	// The other targets recover (the service shares the stale map): the
	// session stays where it was served.
	delete(catalog.stale, "anthropic/claude")
	delete(catalog.stale, "groq/llama")
	for i := range 4 {
		if got := resolveSession(t, svc, "smart", "sess-a"); got != "openai/gpt-4o" {
			t.Fatalf("resolution %d = %q, session moved after targets recovered", i, got)
		}
	}
}
