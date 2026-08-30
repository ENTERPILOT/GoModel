package virtualmodels

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

// upsertRedirect stores an enabled redirect over the given target models.
func upsertRedirect(t *testing.T, svc *Service, source, strategy string, models ...string) {
	t.Helper()
	targets := make([]Target, len(models))
	for i, model := range models {
		targets[i] = Target{Model: model}
	}
	err := svc.Upsert(context.Background(), VirtualModel{Source: source, Targets: targets, Strategy: strategy, Enabled: true})
	if err != nil {
		t.Fatalf("Upsert(%s) error = %v", source, err)
	}
}

func TestChain_ResolvesThroughVirtualModel(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "cheap", "", "groq/llama")
	upsertRedirect(t, svc, "production", "", "cheap")

	sel, changed, err := svc.ResolveModel(core.NewRequestedModelSelector("production", ""))
	if err != nil || !changed {
		t.Fatalf("ResolveModel() = %v, %v, %v; want change", sel, changed, err)
	}
	if got := sel.QualifiedModel(); got != "groq/llama" {
		t.Fatalf("ResolveModel() = %q, want groq/llama", got)
	}
	if !svc.Supports("production") {
		t.Fatalf("Supports(production) = false, want true")
	}
	if got := svc.GetProviderType("production"); got != "openai" {
		t.Fatalf("GetProviderType(production) = %q, want openai", got)
	}
	refresh, ok, _ := svc.ResolveRefreshTarget(core.NewRequestedModelSelector("production", ""))
	if !ok || refresh.QualifiedModel() != "groq/llama" {
		t.Fatalf("ResolveRefreshTarget(production) = %v, %v; want groq/llama", refresh, ok)
	}
}

func TestChain_OuterStrategyComposesWithInner(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "cheap", StrategyRoundRobin, "groq/llama", "local/mistral")
	upsertRedirect(t, svc, "smart", StrategyRoundRobin, "cheap", "openai/gpt-4o")

	// The outer strategy alternates between its two legs; the inner one only
	// advances when its leg is chosen, so every inner target keeps its share.
	got := resolvedModels(t, svc, "smart", 4)
	want := []string{"groq/llama", "openai/gpt-4o", "local/mistral", "openai/gpt-4o"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("resolved = %v, want %v", got, want)
	}
}

func TestChain_CostPricesLegAtCheapestLeaf(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "budget", StrategyCost, "anthropic/claude", "groq/llama")
	upsertRedirect(t, svc, "frugal", StrategyCost, "openai/gpt-4o", "budget")

	for _, got := range resolvedModels(t, svc, "frugal", 3) {
		if got != "groq/llama" {
			t.Fatalf("cost strategy resolved %q, want groq/llama through the budget leg", got)
		}
	}
}

func TestChain_DisabledOrUnavailableInnerLegIsSkipped(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "cheap", "", "groq/llama")
	upsertRedirect(t, svc, "smart", StrategyRoundRobin, "cheap", "openai/gpt-4o")

	ctx := context.Background()
	if err := svc.Upsert(ctx, VirtualModel{Source: "cheap", Targets: []Target{{Model: "groq/llama"}}, Enabled: false}); err != nil {
		t.Fatalf("Upsert(disable cheap) error = %v", err)
	}
	for _, got := range resolvedModels(t, svc, "smart", 3) {
		if got != "openai/gpt-4o" {
			t.Fatalf("resolved %q through a disabled leg, want openai/gpt-4o", got)
		}
	}

	// The refresh target skips the disabled leg and lands on the next concrete one.
	refresh, ok, _ := svc.ResolveRefreshTarget(core.NewRequestedModelSelector("smart", ""))
	if !ok || refresh.QualifiedModel() != "openai/gpt-4o" {
		t.Fatalf("ResolveRefreshTarget(smart) = %v, %v; want openai/gpt-4o past the disabled leg", refresh, ok)
	}

	// An outer alias whose only leg is a disabled virtual model does not resolve.
	upsertRedirect(t, svc, "only-cheap", "", "cheap")
	if _, changed, _ := svc.ResolveModel(core.NewRequestedModelSelector("only-cheap", "")); changed {
		t.Fatalf("ResolveModel(only-cheap) changed = true, want fall-through")
	}
	if svc.Supports("only-cheap") {
		t.Fatalf("Supports(only-cheap) = true, want false")
	}
}

func TestChain_ExposedModelsProjectLeafMetadata(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "cheap", "", "groq/llama")
	upsertRedirect(t, svc, "production", "", "cheap")

	var found bool
	for _, model := range svc.ExposedModels() {
		if model.ID == "production" {
			found = true
			if model.Metadata == nil || model.Metadata.Pricing == nil {
				t.Fatalf("production exposed without leaf pricing metadata")
			}
		}
	}
	if !found {
		t.Fatalf("ExposedModels() did not list the chained alias")
	}
	// The exposure filter sees the concrete leaf, not the alias name.
	filtered := svc.ExposedModelsFiltered(func(sel core.ModelSelector) bool { return sel.Provider != "groq" })
	for _, model := range filtered {
		if model.ID == "production" {
			t.Fatalf("ExposedModelsFiltered() listed production although its leaf is filtered out")
		}
	}

	for _, view := range svc.ListViews() {
		if view.Source == "production" && (view.ResolvedModel != "groq/llama" || !view.Valid) {
			t.Fatalf("view = %+v, want resolved groq/llama and valid", view)
		}
	}
}

func TestChain_RejectsCycles(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "a", "", "openai/gpt-4o")
	upsertRedirect(t, svc, "b", "", "a")
	upsertRedirect(t, svc, "c", "", "b")

	err := svc.Upsert(context.Background(), VirtualModel{Source: "a", Targets: []Target{{Model: "c"}}, Enabled: true})
	if err == nil {
		t.Fatalf("Upsert(cycle) error = nil, want rejection")
	}
	if !IsValidationError(err) {
		t.Fatalf("Upsert(cycle) error = %v, want validation error", err)
	}
	if !strings.Contains(err.Error(), "a -> c -> b -> a") {
		t.Fatalf("Upsert(cycle) error = %q, want the cycle spelled out", err)
	}
}

func TestChain_RejectsTooDeepChains(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "v1", "", "openai/gpt-4o")
	for i := 2; i <= MaxChainDepth; i++ {
		upsertRedirect(t, svc, fmt.Sprintf("v%d", i), "", fmt.Sprintf("v%d", i-1))
	}

	err := svc.Upsert(context.Background(), VirtualModel{
		Source:  fmt.Sprintf("v%d", MaxChainDepth+1),
		Targets: []Target{{Model: fmt.Sprintf("v%d", MaxChainDepth)}},
		Enabled: true,
	})
	if err == nil || !IsValidationError(err) {
		t.Fatalf("Upsert(too deep) error = %v, want validation error", err)
	}
}

func TestChain_RejectsUnknownTargetAndDeletingReferencedModel(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	ctx := context.Background()

	// A target must be a catalog model or an existing virtual model.
	err := svc.Upsert(ctx, VirtualModel{Source: "outer", Targets: []Target{{Model: "missing"}}, Enabled: true})
	if err == nil || !IsValidationError(err) {
		t.Fatalf("Upsert(unknown target) error = %v, want validation error", err)
	}

	upsertRedirect(t, svc, "cheap", "", "groq/llama")
	upsertRedirect(t, svc, "outer", "", "cheap")

	if err := svc.Delete(ctx, "cheap"); err == nil || !IsValidationError(err) || !strings.Contains(err.Error(), "outer") {
		t.Fatalf("Delete(referenced) error = %v, want validation error naming outer", err)
	}
	renamed := VirtualModel{Source: "cheaper", Targets: []Target{{Model: "groq/llama"}}, Enabled: true}
	if err := svc.Rename(ctx, "cheap", renamed); err == nil || !IsValidationError(err) {
		t.Fatalf("Rename(referenced) error = %v, want validation error", err)
	}

	if err := svc.Delete(ctx, "outer"); err != nil {
		t.Fatalf("Delete(outer) error = %v", err)
	}
	if err := svc.Delete(ctx, "cheap"); err != nil {
		t.Fatalf("Delete(cheap) after removing outer error = %v", err)
	}
}

func TestChain_SessionAffinityPinsThroughChain(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "cheap", StrategyRoundRobin, "groq/llama", "local/mistral")
	upsertRedirect(t, svc, "smart", StrategyRoundRobin, "cheap", "openai/gpt-4o")

	first := resolveSession(t, svc, "smart", "sess-a")
	for range 5 {
		if got := resolveSession(t, svc, "smart", "sess-a"); got != first {
			t.Fatalf("session moved from %q to %q", first, got)
		}
	}
}

func TestChain_SlashNamedVirtualModelIsChained(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	ctx := context.Background()
	// "team/cheap" parses like provider "team" + model "cheap", but no such
	// provider exists: as a target without an explicit provider it must be
	// read as the virtual model of that name.
	upsertRedirect(t, svc, "team/cheap", "", "groq/llama")
	upsertRedirect(t, svc, "outer", "", "team/cheap")

	sel, changed, err := svc.ResolveModel(core.NewRequestedModelSelector("outer", ""))
	if err != nil || !changed || sel.QualifiedModel() != "groq/llama" {
		t.Fatalf("ResolveModel(outer) = %v, %v, %v; want groq/llama", sel, changed, err)
	}
	if err := svc.Delete(ctx, "team/cheap"); err == nil || !IsValidationError(err) {
		t.Fatalf("Delete(team/cheap) error = %v, want dependent rejection", err)
	}

	// An explicit provider pins the target to a concrete model even when a
	// virtual model of the same qualified name exists.
	err = svc.Upsert(ctx, VirtualModel{Source: "pinned", Targets: []Target{{Provider: "team", Model: "cheap"}}, Enabled: true})
	if err == nil || !IsValidationError(err) {
		t.Fatalf("Upsert(pinned) error = %v, want unknown provider rejection", err)
	}
}

// A redirect that shadows its own source adds failover to a real model rather
// than replacing it, so other redirects that name the model reach the model —
// not the shadow's fallback list. Two models protecting each other is the
// common legacy failover-rule shape and must not read as a cycle.
func TestChain_SelfShadowingRedirectIsNotAChainLeg(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	ctx := context.Background()
	upsertRedirect(t, svc, "openai/gpt-4o", StrategyFailover, "openai/gpt-4o", "groq/llama")
	// Mutual protection: previously rejected as openai/gpt-4o -> groq/llama -> openai/gpt-4o.
	upsertRedirect(t, svc, "groq/llama", StrategyFailover, "groq/llama", "openai/gpt-4o")

	for _, source := range []string{"openai/gpt-4o", "groq/llama"} {
		sel, _, err := svc.ResolveModel(core.NewRequestedModelSelector(source, ""))
		if err != nil || sel.QualifiedModel() != source {
			t.Fatalf("ResolveModel(%s) = %v, %v; want the model itself", source, sel, err)
		}
	}

	// An alias on a shadowed model resolves to the concrete model, not through
	// the shadow's fallbacks, and does not pin the shadow in place.
	upsertRedirect(t, svc, "prod", "", "openai/gpt-4o")
	sel, _, err := svc.ResolveModel(core.NewRequestedModelSelector("prod", ""))
	if err != nil || sel.QualifiedModel() != "openai/gpt-4o" {
		t.Fatalf("ResolveModel(prod) = %v, %v; want openai/gpt-4o", sel, err)
	}
	if err := svc.Delete(ctx, "openai/gpt-4o"); err != nil {
		t.Fatalf("Delete(self-shadow referenced by prod) error = %v, want success", err)
	}

	// A redirect that replaces the model (no self target) is still a chain leg.
	upsertRedirect(t, svc, "openai/gpt-4o", "", "groq/llama")
	sel, _, err = svc.ResolveModel(core.NewRequestedModelSelector("prod", ""))
	if err != nil || sel.QualifiedModel() != "groq/llama" {
		t.Fatalf("ResolveModel(prod) through replacing shadow = %v, %v; want groq/llama", sel, err)
	}
	if err := svc.Delete(ctx, "openai/gpt-4o"); err == nil || !IsValidationError(err) {
		t.Fatalf("Delete(replacing shadow referenced by prod) error = %v, want validation error", err)
	}
}

// Legacy failover rules that fall back to each other migrate into a set of
// self-shadowing redirects that must load together.
func TestChain_MutualMigratedFailoverRulesLoad(t *testing.T) {
	t.Parallel()
	a, ok := failoverModel("openai/gpt-4o", []string{"groq/llama"}, true)
	if !ok {
		t.Fatalf("failoverModel(a) = !ok")
	}
	b, ok := failoverModel("groq/llama", []string{"openai/gpt-4o"}, true)
	if !ok {
		t.Fatalf("failoverModel(b) = !ok")
	}
	if _, err := buildSnapshot([]VirtualModel{a, b}, true); err != nil {
		t.Fatalf("buildSnapshot(mutual rules) error = %v", err)
	}
	snap, _ := buildSnapshot([]VirtualModel{a, b}, true)
	if err := validateChains(&snap); err != nil {
		t.Fatalf("validateChains(mutual rules) error = %v, want none", err)
	}
}
