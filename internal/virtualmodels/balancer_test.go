package virtualmodels

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

// pricedModel builds a catalog model with input/output per-Mtok pricing.
func pricedModel(id string, inputPerMtok, outputPerMtok float64) core.Model {
	return core.Model{
		ID:     id,
		Object: "model",
		Metadata: &core.ModelMetadata{
			Pricing: &core.ModelPricing{
				InputPerMtok:  new(inputPerMtok),
				OutputPerMtok: new(outputPerMtok),
			},
		},
	}
}

// balancingCatalog supports several priced targets plus one unpriced local model.
func balancingCatalog() fakeCatalog {
	return fakeCatalog{
		providers: []string{"openai", "anthropic", "groq", "local"},
		supported: map[string]core.Model{
			"openai/gpt-4o":    pricedModel("openai/gpt-4o", 2.5, 10),
			"anthropic/claude": pricedModel("anthropic/claude", 3, 15),
			"groq/llama":       pricedModel("groq/llama", 0.5, 0.8),
			"local/mistral":    {ID: "local/mistral", Object: "model"}, // unpriced
		},
	}
}

func newBalancingService(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(newSQLiteVMStore(t), balancingCatalog(), true)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return svc
}

// resolvedModels resolves source n times and returns the qualified targets chosen.
func resolvedModels(t *testing.T, svc *Service, source string, n int) []string {
	t.Helper()
	out := make([]string, 0, n)
	for range n {
		sel, _, err := svc.ResolveModel(core.NewRequestedModelSelector(source, ""))
		if err != nil {
			t.Fatalf("ResolveModel() error = %v", err)
		}
		out = append(out, sel.QualifiedModel())
	}
	return out
}

func countByModel(models []string) map[string]int {
	counts := make(map[string]int)
	for _, m := range models {
		counts[m]++
	}
	return counts
}

// A target whose provider inventory is stale (latest refresh failed) is still
// catalog-supported but must be skipped by load balancing.
func TestBalancer_SkipsStaleProviderTargets(t *testing.T) {
	t.Parallel()
	catalog := balancingCatalog()
	catalog.stale = map[string]bool{"openai/gpt-4o": true}
	svc, err := NewService(newSQLiteVMStore(t), catalog, true)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if err := svc.Upsert(context.Background(), VirtualModel{
		Source:   "smart",
		Strategy: StrategyRoundRobin,
		Targets: []Target{
			{Provider: "openai", Model: "gpt-4o"},
			{Provider: "anthropic", Model: "claude"},
		},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	for i, got := range resolvedModels(t, svc, "smart", 4) {
		if got != "anthropic/claude" {
			t.Fatalf("resolution[%d] = %q, want stale openai target skipped (anthropic/claude)", i, got)
		}
	}
}

func TestBalancer_RoundRobinRotates(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	if err := svc.Upsert(context.Background(), VirtualModel{
		Source:   "smart",
		Strategy: StrategyRoundRobin,
		Targets: []Target{
			{Provider: "openai", Model: "gpt-4o"},
			{Provider: "anthropic", Model: "claude"},
			{Provider: "groq", Model: "llama"},
		},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	got := resolvedModels(t, svc, "smart", 6)
	want := []string{
		"openai/gpt-4o", "anthropic/claude", "groq/llama",
		"openai/gpt-4o", "anthropic/claude", "groq/llama",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("round robin[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestBalancer_RoundRobinHonorsWeight(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	if err := svc.Upsert(context.Background(), VirtualModel{
		Source:   "smart",
		Strategy: StrategyRoundRobin,
		Targets: []Target{
			{Provider: "openai", Model: "gpt-4o", Weight: 2},
			{Provider: "groq", Model: "llama", Weight: 1},
		},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	counts := countByModel(resolvedModels(t, svc, "smart", 9))
	if counts["openai/gpt-4o"] != 6 || counts["groq/llama"] != 3 {
		t.Fatalf("weighted distribution = %v, want gpt-4o:6 llama:3", counts)
	}
}

func TestBalancer_CostPicksCheapest(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	if err := svc.Upsert(context.Background(), VirtualModel{
		Source:   "cheap",
		Strategy: StrategyCost,
		Targets: []Target{
			{Provider: "openai", Model: "gpt-4o"},
			{Provider: "anthropic", Model: "claude"},
			{Provider: "groq", Model: "llama"},
		},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	for _, got := range resolvedModels(t, svc, "cheap", 4) {
		if got != "groq/llama" {
			t.Fatalf("cost strategy chose %q, want groq/llama", got)
		}
	}
}

func TestBalancer_CostFallsBackWhenUnpriced(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	if err := svc.Upsert(context.Background(), VirtualModel{
		Source:   "cheap",
		Strategy: StrategyCost,
		Targets: []Target{
			{Provider: "local", Model: "mistral"}, // unpriced, declared first
			{Provider: "openai", Model: "gpt-4o"}, // priced
		},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	// The priced target wins over the unpriced one regardless of declaration order.
	for _, got := range resolvedModels(t, svc, "cheap", 3) {
		if got != "openai/gpt-4o" {
			t.Fatalf("cost strategy chose %q, want the priced openai/gpt-4o", got)
		}
	}
}

func TestBalancer_SkipsUnavailableTargets(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	// One target is not in the catalog at all; it must be skipped, never returned,
	// and must not consume a round-robin slot.
	if err := svc.store.Upsert(context.Background(), VirtualModel{
		Source:   "smart",
		Strategy: StrategyRoundRobin,
		Targets: []Target{
			{Provider: "openai", Model: "gpt-4o"},
			{Provider: "groq", Model: "llama"},
		},
		Enabled: true,
	}); err != nil {
		t.Fatalf("store.Upsert() error = %v", err)
	}
	// Drop groq/llama from the catalog after storing the redirect.
	cat := balancingCatalog()
	delete(cat.supported, "groq/llama")
	svc.catalog = cat
	if err := svc.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	for _, got := range resolvedModels(t, svc, "smart", 4) {
		if got != "openai/gpt-4o" {
			t.Fatalf("resolved %q, want only available target openai/gpt-4o", got)
		}
	}
}

func TestBalancer_WeightedIndexPlainWhenEqual(t *testing.T) {
	t.Parallel()
	targets := []resolvedTarget{{}, {}, {}}
	for i := range uint64(6) {
		if got := weightedIndex(targets, i); got != int(i%3) {
			t.Fatalf("weightedIndex(%d) = %d, want %d", i, got, i%3)
		}
	}
}

func TestRoundRobin_PruneRemovesStaleCounters(t *testing.T) {
	t.Parallel()
	var rr roundRobin
	rr.next("keep")
	rr.next("gone")

	rr.prune(map[string]redirectEntry{"keep": {}})

	if _, ok := rr.counters.Load("keep"); !ok {
		t.Fatalf("keep counter removed, want retained")
	}
	if _, ok := rr.counters.Load("gone"); ok {
		t.Fatalf("gone counter retained, want pruned")
	}
}

func TestBalancer_PrefersTargetsWithCapacity(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	// openai/gpt-4o is rate-saturated; the alias must route around it.
	svc.SetTargetCapacity(func(qualified string) bool { return qualified != "openai/gpt-4o" })
	if err := svc.Upsert(context.Background(), VirtualModel{
		Source:   "smart",
		Strategy: StrategyRoundRobin,
		Targets: []Target{
			{Provider: "openai", Model: "gpt-4o"},
			{Provider: "anthropic", Model: "claude"},
		},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	for i, got := range resolvedModels(t, svc, "smart", 4) {
		if got != "anthropic/claude" {
			t.Fatalf("resolution[%d] = %q, want saturated openai target skipped (anthropic/claude)", i, got)
		}
	}
}

// TestBalancer_AllSaturatedFallsBackToFirstTarget pins the honest-429 path:
// when every live target is rate-saturated, the alias still resolves — to its
// first declared target — so admission rejects with 429 and Retry-After (or
// defers to failover) instead of the all-targets-down error.
func TestBalancer_AllSaturatedFallsBackToFirstTarget(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	svc.SetTargetCapacity(func(string) bool { return false })
	if err := svc.Upsert(context.Background(), VirtualModel{
		Source:   "smart",
		Strategy: StrategyRoundRobin,
		Targets: []Target{
			{Provider: "openai", Model: "gpt-4o"},
			{Provider: "anthropic", Model: "claude"},
		},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	for i, got := range resolvedModels(t, svc, "smart", 3) {
		if got != "openai/gpt-4o" {
			t.Fatalf("resolution[%d] = %q, want deterministic first target (openai/gpt-4o)", i, got)
		}
	}
}

// Capacity steers choice among live targets only: a target the catalog marks
// unavailable stays excluded even when everything else is saturated.
func TestBalancer_SaturationFallbackSkipsUnavailableTargets(t *testing.T) {
	t.Parallel()
	catalog := balancingCatalog()
	catalog.stale = map[string]bool{"openai/gpt-4o": true}
	svc, err := NewService(newSQLiteVMStore(t), catalog, true)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	svc.SetTargetCapacity(func(string) bool { return false })
	if err := svc.Upsert(context.Background(), VirtualModel{
		Source:   "smart",
		Strategy: StrategyRoundRobin,
		Targets: []Target{
			{Provider: "openai", Model: "gpt-4o"},
			{Provider: "anthropic", Model: "claude"},
		},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	for i, got := range resolvedModels(t, svc, "smart", 2) {
		if got != "anthropic/claude" {
			t.Fatalf("resolution[%d] = %q, want first LIVE target (anthropic/claude)", i, got)
		}
	}
}

// The cost strategy prices only the capacity-filtered pool: a cheaper but
// rate-saturated target loses to a costlier one that can actually serve.
func TestBalancer_CostSkipsSaturatedCheapestTarget(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	// groq/llama (0.5/0.8 per Mtok) is the cheapest but saturated; the next
	// cheapest with capacity is openai/gpt-4o (2.5/10), ahead of
	// anthropic/claude (3/15).
	svc.SetTargetCapacity(func(qualified string) bool { return qualified != "groq/llama" })
	if err := svc.Upsert(context.Background(), VirtualModel{
		Source:   "cheap",
		Strategy: StrategyCost,
		Targets: []Target{
			{Provider: "groq", Model: "llama"},
			{Provider: "anthropic", Model: "claude"},
			{Provider: "openai", Model: "gpt-4o"},
		},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	for i, got := range resolvedModels(t, svc, "cheap", 3) {
		if got != "openai/gpt-4o" {
			t.Fatalf("resolution[%d] = %q, want cheapest target WITH capacity (openai/gpt-4o)", i, got)
		}
	}
}
