package virtualmodels

import (
	"context"
	"testing"

	"gomodel/internal/core"
)

func floatPtr(v float64) *float64 { return &v }

// pricedModel builds a catalog model with input/output per-Mtok pricing.
func pricedModel(id string, inputPerMtok, outputPerMtok float64) core.Model {
	return core.Model{
		ID:     id,
		Object: "model",
		Metadata: &core.ModelMetadata{
			Pricing: &core.ModelPricing{
				InputPerMtok:  floatPtr(inputPerMtok),
				OutputPerMtok: floatPtr(outputPerMtok),
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
	for i := 0; i < n; i++ {
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
	for i := uint64(0); i < 6; i++ {
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
