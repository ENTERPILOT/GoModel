package virtualmodels

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

func upsertBalancedVM(t *testing.T, svc *Service, strategy string, affinity *bool) {
	t.Helper()
	err := svc.Upsert(context.Background(), VirtualModel{
		Source:          "smart",
		Strategy:        strategy,
		SessionAffinity: affinity,
		Targets: []Target{
			{Provider: "openai", Model: "gpt-4o"},
			{Provider: "anthropic", Model: "claude"},
			{Provider: "groq", Model: "llama"},
		},
		Enabled: true,
	})
	require.NoError(t, err)
}

// resolveSession resolves source once with a session id and returns the chosen target.
func resolveSession(t *testing.T, svc *Service, source, sessionID string) string {
	t.Helper()
	resolution, _, err := svc.resolveRequested(context.Background(), core.NewRequestedModelSelector(source, ""), "", false, sessionID)
	require.NoError(t, err)

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
				got := resolveSession(t, svc, "smart", "sess-a")
				require.Equal(t, first, got, "resolution %d: want pinned %q", i, first)
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
	require.NotEqual(t, b, a)
	got := resolveSession(t, svc, "smart", "sess-a")
	require.Equal(t, a, got)
	got = resolveSession(t, svc, "smart", "sess-b")
	require.Equal(t, b, got)
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
	require.Len(t, seen, 3)
}

func TestSticky_EmptySessionDoesNotPin(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertBalancedVM(t, svc, StrategyRoundRobin, nil)

	resolveSession(t, svc, "smart", "")
	got := len(svc.sticky.entries)
	require.Equal(t, 0, got)
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
	require.NotEqual(t, pinned, repinned)

	// The new pin holds even after the original target regains capacity.
	saturated[pinned] = false
	got := resolveSession(t, svc, "smart", "sess-a")
	require.Equal(t, repinned, got)
}

func TestSticky_SaturatedFallbackDoesNotPin(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertBalancedVM(t, svc, StrategyRoundRobin, nil)
	svc.SetTargetCapacity(func(string) bool { return false })
	// Every target saturated: the first declared target serves the honest-429
	// path and must not become the session's pin.
	got := resolveSession(t, svc, "smart", "sess-a")
	require.Equal(t, "openai/gpt-4o", got)

	require.Empty(t, svc.sticky.entries, "saturated fallback must not pin")
}

func TestSticky_SaturatedFallbackPreservesExistingPin(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertBalancedVM(t, svc, StrategyRoundRobin, nil)

	saturated := map[string]bool{}
	svc.SetTargetCapacity(func(qualified string) bool { return !saturated[qualified] })

	resolveSession(t, svc, "smart", "sess-a") // consume the first round-robin target
	pinned := resolveSession(t, svc, "smart", "sess-b")
	require.Equal(t, "anthropic/claude", pinned)

	for _, target := range []string{"openai/gpt-4o", "anthropic/claude", "groq/llama"} {
		saturated[target] = true
	}
	got := resolveSession(t, svc, "smart", "sess-b")
	require.Equal(t, "openai/gpt-4o", got)

	clear(saturated)
	got = resolveSession(t, svc, "smart", "sess-b")
	require.Equal(t, pinned, got)
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
	got := resolveSession(t, svc, "smart", "sess-a")
	require.NotEqual(t, pinned, got)
}

// stickyProbe resolves without picking: it reports the existing viable pin or
// "" and never assigns, so tests can inspect state through the public seam.
func stickyProbe(sticky *stickySessions, source, session string) string {
	qualified, _ := sticky.lookup(source, session, func(string) bool { return true })
	return qualified
}

// stickyAssign resolves with a fixed choice, pinning it.
func stickyAssign(sticky *stickySessions, source, session, qualified string) string {
	return sticky.resolve(source, session,
		func(string) bool { return true },
		qualified,
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
	got := stickyProbe(sticky, "smart", "sess-a")
	require.NotEmpty(t, got)

	current = current.Add(stickySessionTTL - time.Minute)
	got = stickyProbe(sticky, "smart", "sess-a")
	require.NotEmpty(t, got)
}

// Concurrent first requests of one session must agree on a single target even
// though strategy choice happens before the atomic pin lookup and assignment.
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
			resolution, _, err := svc.resolveRequested(context.Background(),
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
		require.NoError(t, errs[i])
		require.Equal(t, results[0], results[i])
	}
}

func TestSticky_PinnedRequestsDoNotAdvanceRoundRobin(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertBalancedVM(t, svc, StrategyRoundRobin, nil)
	got := resolveSession(t, svc, "smart", "sess-a")
	require.Equal(t, "openai/gpt-4o", got)

	for range 2 {
		resolveSession(t, svc, "smart", "sess-a")
	}
	got = resolveSession(t, svc, "smart", "sess-b")
	require.Equal(t, "anthropic/claude", got)
}

func TestSticky_PruneDropsDeletedSources(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertBalancedVM(t, svc, StrategyRoundRobin, nil)

	resolveSession(t, svc, "smart", "sess-a")
	require.Len(t, svc.sticky.entries, 1)
	err := svc.Delete(context.Background(), "smart")
	require.NoError(t, err)
	got := len(svc.sticky.entries)
	require.Equal(t, 0, got)
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
	require.Equal(t, maxStickySessions, len(sticky.entries))

	stickyAssign(sticky, "smart", "one-more", "openai/gpt-4o")
	require.Equal(t, maxStickySessions, len(sticky.entries))
	// The oldest pin was evicted; the newest survives.
	got := stickyProbe(sticky, "smart", "one-more")
	require.NotEmpty(t, got)
	got = stickyProbe(sticky, "smart", "sess-0")
	require.Empty(t, got)
}

// A multi-target redirect with only one target momentarily available must
// still pin: otherwise a target coming back online would let the strategy
// move an active session mid-conversation.
func TestSticky_PinsWhenOnlyOneTargetSupported(t *testing.T) {
	t.Parallel()
	catalog := balancingCatalog()
	catalog.stale = map[string]bool{"anthropic/claude": true, "groq/llama": true}
	svc, err := NewService(newSQLVMStore(t), catalog, true)
	require.NoError(t, err)

	upsertBalancedVM(t, svc, StrategyRoundRobin, nil)
	got := resolveSession(t, svc, "smart", "sess-a")
	require.Equal(t, "openai/gpt-4o", got)

	require.Len(t, svc.sticky.entries, 1, "the sole viable target must be pinned")

	// The other targets recover (the service shares the stale map): the
	// session stays where it was served.
	delete(catalog.stale, "anthropic/claude")
	delete(catalog.stale, "groq/llama")
	for i := range 4 {
		got := resolveSession(t, svc, "smart", "sess-a")
		require.Equal(t, "openai/gpt-4o", got, "resolution %d: session moved after targets recovered", i)
	}
}

// repin is the entry point the adaptive strategy records its choice through,
// so it owes the same capacity bound as resolve.
func TestSticky_RepinRespectsCapacity(t *testing.T) {
	t.Parallel()
	sticky := &stickySessions{}
	current := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	sticky.now = func() time.Time { return current }

	for i := range maxStickySessions + 50 {
		sticky.repin("smart", "sess-"+strconv.Itoa(i), "", "openai/gpt-4o")
		current = current.Add(time.Millisecond)
	}
	require.Equal(t, maxStickySessions, len(sticky.entries))
	got := stickyProbe(sticky, "smart", "sess-0")
	require.Empty(t, got)
}

// Re-pinning an existing session overwrites in place. Every request of an
// adaptive session takes this path, so it must neither grow the map nor
// sweep it.
func TestSticky_RepinOverwritesInPlace(t *testing.T) {
	t.Parallel()
	sticky := &stickySessions{}
	current := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	sticky.now = func() time.Time { return current }

	sticky.repin("smart", "sess-a", "", "openai/gpt-4o")
	// A pin for another session expires while sess-a keeps being re-pinned.
	sticky.repin("smart", "sess-b", "", "groq/llama")
	current = current.Add(stickySessionTTL + time.Minute)

	sticky.repin("smart", "sess-a", "openai/gpt-4o", "anthropic/claude")
	got := stickyProbe(sticky, "smart", "sess-a")
	require.Equal(t, "anthropic/claude", got)
	require.Len(t, sticky.entries, 2)

	// The expired pin is still collected by the normal sweeps.
	sticky.prune(map[string]*redirectEntry{"smart": {}})
	_, ok := sticky.entries[stickyKey{source: "smart", session: "sess-b"}]
	require.False(t, ok)
}

// An empty target is not a pin: the adaptive path must not record one when
// the strategy could not choose.
func TestSticky_RepinIgnoresEmptyTarget(t *testing.T) {
	t.Parallel()
	sticky := &stickySessions{}
	sticky.repin("smart", "sess-a", "", "")
	require.Empty(t, sticky.entries)
}
