package virtualmodels

import (
	"context"
	"fmt"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

// upsertRedirect stores an enabled redirect over the given target models.
func upsertRedirect(t *testing.T, svc *Service, source, strategy string, models ...string) {
	t.Helper()
	targets := make([]Target, len(models))
	for i, model := range models {
		targets[i] = Target{Model: model}
	}
	err := svc.Upsert(context.Background(), VirtualModel{Source: source, Targets: targets, Strategy: strategy, Enabled: true})
	require.NoError(t, err)
}

func TestChain_ResolvesThroughVirtualModel(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "cheap", "", "groq/llama")
	upsertRedirect(t, svc, "production", "", "cheap")

	sel, changed, err := svc.ResolveModel(core.NewRequestedModelSelector("production", ""))
	require.NoError(t, err)
	require.True(t, changed, "ResolveModel() = %v, %v, %v; want change", sel, changed, err)
	got := sel.QualifiedModel()
	require.Equal(t, "groq/llama", got)
	require.True(t, svc.Supports("production"))
	got = svc.GetProviderType("production")
	require.Equal(t, "openai", got)

	refresh, ok, _ := svc.ResolveRefreshTarget(core.NewRequestedModelSelector("production", ""))
	require.True(t, ok)
	require.Equal(t, "groq/llama", refresh.QualifiedModel(), "ResolveRefreshTarget(production) = %v, %v; want groq/llama", refresh, ok)
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
	require.Equal(t, want, got)
}

func TestChain_CostPricesLegAtCheapestLeaf(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "budget", StrategyCost, "anthropic/claude", "groq/llama")
	upsertRedirect(t, svc, "frugal", StrategyCost, "openai/gpt-4o", "budget")

	for _, got := range resolvedModels(t, svc, "frugal", 3) {
		require.Equal(t, "groq/llama", got)
	}
}

func TestChain_DisabledOrUnavailableInnerLegIsSkipped(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "cheap", "", "groq/llama")
	upsertRedirect(t, svc, "smart", StrategyRoundRobin, "cheap", "openai/gpt-4o")

	ctx := context.Background()
	err := svc.Upsert(ctx, VirtualModel{Source: "cheap", Targets: []Target{{Model: "groq/llama"}}, Enabled: false})
	require.NoError(t, err)

	for _, got := range resolvedModels(t, svc, "smart", 3) {
		require.Equal(t, "openai/gpt-4o", got)
	}

	// The refresh target skips the disabled leg and lands on the next concrete one.
	refresh, ok, _ := svc.ResolveRefreshTarget(core.NewRequestedModelSelector("smart", ""))
	require.True(t, ok)
	require.Equal(t, "openai/gpt-4o", refresh.QualifiedModel(), "ResolveRefreshTarget(smart) = %v, %v; want openai/gpt-4o past the disabled leg", refresh, ok)

	// An outer alias whose only leg is a disabled virtual model does not resolve.
	upsertRedirect(t, svc, "only-cheap", "", "cheap")
	_, changed, _ := svc.ResolveModel(core.NewRequestedModelSelector("only-cheap", ""))
	require.False(t, changed)
	require.False(t, svc.Supports("only-cheap"))
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
			require.NotNil(t, model.Metadata)
			require.NotNil(t, model.Metadata.Pricing)
		}
	}
	require.True(t, found)

	// The exposure filter sees the concrete leaf, not the alias name.
	filtered := svc.ExposedModelsFiltered(func(sel core.ModelSelector) bool { return sel.Provider != "groq" })
	for _, model := range filtered {
		require.NotEqual(t, "production", model.ID)
	}

	for _, view := range svc.ListViews() {
		if view.Source != "production" {
			continue
		}
		require.Equal(t, "groq/llama", view.ResolvedModel)
		require.True(t, view.Valid, "view = %+v", view)
	}
}

func TestChain_RejectsCycles(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "a", "", "openai/gpt-4o")
	upsertRedirect(t, svc, "b", "", "a")
	upsertRedirect(t, svc, "c", "", "b")

	err := svc.Upsert(context.Background(), VirtualModel{Source: "a", Targets: []Target{{Model: "c"}}, Enabled: true})
	require.ErrorContains(t, err, "a -> c -> b -> a")
	require.True(t, IsValidationError(err))
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
	require.Error(t, err)
	require.True(t, IsValidationError(err))
}

func TestChain_RejectsUnknownTargetAndDeletingReferencedModel(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	ctx := context.Background()

	// A target must be a catalog model or an existing virtual model.
	err := svc.Upsert(ctx, VirtualModel{Source: "outer", Targets: []Target{{Model: "missing"}}, Enabled: true})
	require.Error(t, err)
	require.True(t, IsValidationError(err))

	upsertRedirect(t, svc, "cheap", "", "groq/llama")
	upsertRedirect(t, svc, "outer", "", "cheap")
	err = svc.Delete(ctx, "cheap")
	require.ErrorContains(t, err, "outer")
	require.True(t, IsValidationError(err))

	renamed := VirtualModel{Source: "cheaper", Targets: []Target{{Model: "groq/llama"}}, Enabled: true}
	err = svc.Rename(ctx, "cheap", renamed)
	require.Error(t, err)
	require.True(t, IsValidationError(err))
	err = svc.Delete(ctx, "outer")
	require.NoError(t, err)
	err = svc.Delete(ctx, "cheap")
	require.NoError(t, err)
}

func TestChain_SessionAffinityPinsThroughChain(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "cheap", StrategyRoundRobin, "groq/llama", "local/mistral")
	upsertRedirect(t, svc, "smart", StrategyRoundRobin, "cheap", "openai/gpt-4o")

	first := resolveSession(t, svc, "smart", "sess-a")
	for range 5 {
		got := resolveSession(t, svc, "smart", "sess-a")
		require.Equal(t, first, got)
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
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "groq/llama", sel.QualifiedModel(), "ResolveModel(outer) = %v, %v, %v; want groq/llama", sel, changed, err)
	err = svc.Delete(ctx, "team/cheap")
	require.Error(t, err)
	require.True(t, IsValidationError(err))

	// An explicit provider pins the target to a concrete model even when a
	// virtual model of the same qualified name exists.
	err = svc.Upsert(ctx, VirtualModel{Source: "pinned", Targets: []Target{{Provider: "team", Model: "cheap"}}, Enabled: true})
	require.Error(t, err)
	require.True(t, IsValidationError(err))
}

// A redirect that shadows its own source covers a real model rather than
// replacing it. Two such chains referencing each other reach each other's
// concrete model — mutual protection, the common legacy failover-rule shape,
// must not read as a cycle — while any other reference still chains into the
// shadow so it gets the same balancing and failover a direct request gets.
func TestChain_SelfShadowingRedirectIsNotAChainLeg(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	ctx := context.Background()
	upsertRedirect(t, svc, "openai/gpt-4o", StrategyFailover, "openai/gpt-4o", "groq/llama")
	// Mutual protection: previously rejected as openai/gpt-4o -> groq/llama -> openai/gpt-4o.
	upsertRedirect(t, svc, "groq/llama", StrategyFailover, "groq/llama", "openai/gpt-4o")

	for _, source := range []string{"openai/gpt-4o", "groq/llama"} {
		sel, _, err := svc.ResolveModel(core.NewRequestedModelSelector(source, ""))
		require.NoError(t, err)
		require.Equal(t, source, sel.QualifiedModel(), "ResolveModel(%s) = %v, %v; want the model itself", source, sel, err)
	}

	// An alias on a shadowed model chains into the shadow (a failover shadow
	// serves its primary first), and does not pin the shadow in place: the
	// alias reverts to the concrete model when the shadow is deleted.
	upsertRedirect(t, svc, "prod", "", "openai/gpt-4o")
	sel, _, err := svc.ResolveModel(core.NewRequestedModelSelector("prod", ""))
	require.NoError(t, err)
	require.Equal(t, "openai/gpt-4o", sel.QualifiedModel(), "ResolveModel(prod) = %v, %v; want openai/gpt-4o", sel, err)
	err = svc.Delete(ctx, "openai/gpt-4o")
	require.NoError(t, err)

	sel, _, err = svc.ResolveModel(core.NewRequestedModelSelector("prod", ""))
	require.NoError(t, err)
	require.Equal(t, "openai/gpt-4o", sel.QualifiedModel(), "ResolveModel(prod) after shadow delete = %v, %v; want openai/gpt-4o", sel, err)
	// A redirect that replaces the model (no self target) is still a chain leg.
	// The groq/llama self-shadow must go first: its fallback reference to
	// openai/gpt-4o would chain into the replacing shadow and genuinely cycle.
	err = svc.Delete(ctx, "groq/llama")
	require.NoError(t, err)

	upsertRedirect(t, svc, "openai/gpt-4o", "", "groq/llama")
	sel, _, err = svc.ResolveModel(core.NewRequestedModelSelector("prod", ""))
	require.NoError(t, err)
	require.Equal(t, "groq/llama", sel.QualifiedModel(), "ResolveModel(prod) through replacing shadow = %v, %v; want groq/llama", sel, err)
	err = svc.Delete(ctx, "openai/gpt-4o")
	require.Error(t, err)
	require.True(t, IsValidationError(err))
}

// A load-balanced shadow (source enlisted among its targets) balances the
// same way for a direct request and for an alias that names the model, and
// two balanced shadows may reference each other without forming a cycle.
func TestChain_SelfShadowingRedirectBalancesForReferences(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "openai/gpt-4o", StrategyRoundRobin, "openai/gpt-4o", "groq/llama")
	upsertRedirect(t, svc, "prod", "", "openai/gpt-4o")

	direct := map[string]bool{}
	for _, got := range resolvedModels(t, svc, "openai/gpt-4o", 8) {
		direct[got] = true
	}
	viaAlias := map[string]bool{}
	for _, got := range resolvedModels(t, svc, "prod", 8) {
		viaAlias[got] = true
	}
	for _, want := range []string{"openai/gpt-4o", "groq/llama"} {
		require.True(t, direct[want])
		require.True(t, viaAlias[want], "rotation: direct=%v viaAlias=%v; want both to include %s", direct, viaAlias, want)
	}

	// Mutual balanced shadows load like mutual failover shadows.
	upsertRedirect(t, svc, "groq/llama", StrategyRoundRobin, "groq/llama", "openai/gpt-4o")
}

// Legacy failover rules that fall back to each other migrate into a set of
// self-shadowing redirects that must load together.
func TestChain_MutualMigratedFailoverRulesLoad(t *testing.T) {
	t.Parallel()
	a, ok := failoverModel("openai/gpt-4o", []string{"groq/llama"}, true)
	require.True(t, ok)

	b, ok := failoverModel("groq/llama", []string{"openai/gpt-4o"}, true)
	require.True(t, ok)
	_, err := buildSnapshot([]VirtualModel{a, b}, true)
	require.NoError(t, err)

	snap, _ := buildSnapshot([]VirtualModel{a, b}, true)
	err = validateChains(&snap)
	require.NoError(t, err)
}
