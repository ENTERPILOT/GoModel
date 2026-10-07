package virtualmodels

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// disableModel stores a policy row that turns one concrete model off, the way
// an operator disables a model in the dashboard.
func disableModel(t *testing.T, svc *Service, model string) {
	t.Helper()
	err := svc.Upsert(context.Background(), VirtualModel{Source: model, Enabled: false})
	require.NoError(t, err)
}

// resolveForRequest resolves source on the request path and returns the
// concrete model chosen.
func resolveForRequest(t *testing.T, ctx context.Context, svc *Service, source string) string {
	t.Helper()
	sel, changed, err := svc.ResolveModelForUserPath(ctx, core.NewRequestedModelSelector(source, ""))
	require.NoError(t, err)
	require.True(t, changed, "%s must resolve through its redirect", source)
	return sel.QualifiedModel()
}

func TestBalancer_SkipsDisabledTargets(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		vm    VirtualModel
		setup func(*Service)
	}{
		{name: "failover", vm: VirtualModel{Strategy: StrategyFailover}},
		// groq/llama carries most of the weight, so rotation would favor it.
		{name: "weighted round robin", vm: VirtualModel{Strategy: StrategyRoundRobin}},
		// groq/llama is the cheapest target, so cost would pick it.
		{name: "cost", vm: VirtualModel{Strategy: StrategyCost}},
		{name: "adaptive", vm: VirtualModel{Strategy: StrategyAdaptive}, setup: func(svc *Service) {
			svc.SetRouteSelector(&scriptedSelector{answer: "groq/llama"})
		}},
		{name: "plugin", vm: VirtualModel{Strategy: StrategyPlugin, StrategyPlugin: "lat"}, setup: func(svc *Service) {
			svc.SetRouteResolver(&fakeResolver{strategy: &scriptedStrategy{answer: "groq/llama"}})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := newBalancingService(t)
			if tc.setup != nil {
				tc.setup(svc)
			}
			vm := tc.vm
			vm.Source = "smart"
			vm.Enabled = true
			vm.Targets = []Target{
				{Provider: "groq", Model: "llama", Weight: 5},
				{Provider: "anthropic", Model: "claude"},
				{Provider: "openai", Model: "gpt-4o"},
			}
			require.NoError(t, svc.Upsert(context.Background(), vm))
			disableModel(t, svc, "groq/llama")

			ctx := core.WithRequestID(context.Background(), "req-1")
			for i := range 4 {
				got := resolveForRequest(t, ctx, svc, "smart")
				assert.NotEqual(t, "groq/llama", got, "resolution[%d] chose the disabled target", i)
				selector, err := core.ParseModelSelector(got, "")
				require.NoError(t, err)
				assert.NoError(t, svc.ValidateModelAccess(ctx, selector))
			}
		})
	}
}

func TestBalancer_DisabledTargetFailsOverToNext(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "smart", StrategyFailover, "groq/llama", "anthropic/claude")
	disableModel(t, svc, "groq/llama")

	ctx := context.Background()
	require.Equal(t, "anthropic/claude", resolveForRequest(t, ctx, svc, "smart"))

	// The disabled target stays in the failover chain; the gateway skips it
	// when sweeping, exactly like any other target the caller may not use.
	resolution := &core.RequestModelResolution{
		Requested:        core.NewRequestedModelSelector("smart", ""),
		ResolvedSelector: core.ModelSelector{Provider: "anthropic", Model: "claude"},
		AliasApplied:     true,
	}
	chain := svc.ResolveFailovers(resolution, core.OperationChatCompletions)
	require.Len(t, chain, 1)
	assert.False(t, svc.AllowsModel(ctx, chain[0]))

	// Re-enabling the model restores it as the primary.
	err := svc.Upsert(ctx, VirtualModel{Source: "groq/llama", Enabled: true})
	require.NoError(t, err)
	assert.Equal(t, "groq/llama", resolveForRequest(t, ctx, svc, "smart"))
}

func TestSticky_SessionLeavesDisabledTarget(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "smart", StrategyRoundRobin, "groq/llama", "anthropic/claude")

	ctx := core.WithSessionID(context.Background(), "session-1")
	require.Equal(t, "groq/llama", resolveForRequest(t, ctx, svc, "smart"))

	disableModel(t, svc, "groq/llama")
	for range 3 {
		assert.Equal(t, "anthropic/claude", resolveForRequest(t, ctx, svc, "smart"))
	}
}

func TestChain_SkipsDisabledOnlyLeg(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		inner []string
		want  string
	}{
		{name: "all inner disabled", inner: []string{"groq/llama"}, want: "anthropic/claude"},
		{name: "inner falls through to its next target", inner: []string{"groq/llama", "local/mistral"}, want: "local/mistral"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc := newBalancingService(t)
			upsertRedirect(t, svc, "cheap", StrategyFailover, tc.inner...)
			upsertRedirect(t, svc, "smart", StrategyFailover, "cheap", "anthropic/claude")
			disableModel(t, svc, "groq/llama")

			assert.Equal(t, tc.want, resolveForRequest(t, context.Background(), svc, "smart"))
		})
	}
}

func TestBalancer_AllTargetsDisabledKeepsAccessError(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "cheap", StrategyFailover, "groq/llama")
	upsertRedirect(t, svc, "smart", StrategyRoundRobin, "cheap", "anthropic/claude")
	disableModel(t, svc, "groq/llama")
	disableModel(t, svc, "anthropic/claude")

	// With nothing usable the redirect still resolves to its first declared
	// target, so access validation answers instead of a misleading
	// "model not found" for the virtual model's own name.
	ctx := context.Background()
	for range 3 {
		got := resolveForRequest(t, ctx, svc, "smart")
		require.Equal(t, "groq/llama", got)

		err := svc.ValidateModelAccess(ctx, core.ModelSelector{Provider: "groq", Model: "llama"})
		require.Error(t, err)
		gatewayErr, ok := err.(*core.GatewayError)
		require.True(t, ok)
		require.NotNil(t, gatewayErr.Code)
		assert.Equal(t, "model_access_denied", *gatewayErr.Code)
		assert.Equal(t, "requested model is not available", gatewayErr.Message)
	}
}

// allowOnly permits the listed qualified models and denies the rest, standing
// in for an API key's model allowlist.
type allowOnly map[string]bool

func (a allowOnly) AllowsModel(_ context.Context, selector core.ModelSelector) bool {
	return a[selector.QualifiedModel()]
}

func TestBalancer_SkipsDeniedTargets(t *testing.T) {
	t.Parallel()
	t.Run("api key allowlist", func(t *testing.T) {
		t.Parallel()
		svc := newBalancingService(t)
		upsertRedirect(t, svc, "smart", StrategyFailover, "groq/llama", "anthropic/claude")
		svc.SetAccessPolicy(allowOnly{"anthropic/claude": true})

		assert.Equal(t, "anthropic/claude", resolveForRequest(t, context.Background(), svc, "smart"))
	})

	t.Run("model user paths", func(t *testing.T) {
		t.Parallel()
		svc := newBalancingService(t)
		upsertRedirect(t, svc, "smart", StrategyFailover, "groq/llama", "anthropic/claude")
		err := svc.Upsert(context.Background(), VirtualModel{Source: "groq/llama", UserPaths: []string{"/team"}, Enabled: true})
		require.NoError(t, err)

		inScope := core.WithEffectiveUserPath(context.Background(), "/team/alice")
		outOfScope := core.WithEffectiveUserPath(context.Background(), "/other")
		assert.Equal(t, "groq/llama", resolveForRequest(t, inScope, svc, "smart"))
		assert.Equal(t, "anthropic/claude", resolveForRequest(t, outOfScope, svc, "smart"))
	})

	t.Run("every target denied", func(t *testing.T) {
		t.Parallel()
		svc := newBalancingService(t)
		upsertRedirect(t, svc, "smart", StrategyFailover, "groq/llama", "anthropic/claude")
		svc.SetAccessPolicy(allowOnly{})

		ctx := context.Background()
		require.Equal(t, "groq/llama", resolveForRequest(t, ctx, svc, "smart"))
		err := svc.ValidateModelAccess(ctx, core.ModelSelector{Provider: "groq", Model: "llama"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not available for this API key")
	})
}

func TestService_DirectRequestForDisabledModelStillFails(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	upsertRedirect(t, svc, "smart", StrategyFailover, "groq/llama", "anthropic/claude")
	disableModel(t, svc, "groq/llama")

	ctx := context.Background()
	sel, changed, err := svc.ResolveModelForUserPath(ctx, core.NewRequestedModelSelector("groq/llama", ""))
	require.NoError(t, err)
	require.False(t, changed)
	assert.Error(t, svc.ValidateModelAccess(ctx, sel))
}

func TestChain_DisabledLeafIgnoredForRouting(t *testing.T) {
	t.Parallel()
	t.Run("capacity", func(t *testing.T) {
		t.Parallel()
		svc := newBalancingService(t)
		// Only the disabled leaf behind "pool" has room; the leg must not
		// count as having capacity, or the request reaches a 429.
		svc.SetTargetCapacity(func(qualified string) bool { return qualified != "anthropic/claude" })
		upsertRedirect(t, svc, "pool", StrategyFailover, "groq/llama", "anthropic/claude")
		upsertRedirect(t, svc, "smart", StrategyFailover, "pool", "openai/gpt-4o")
		disableModel(t, svc, "groq/llama")

		assert.Equal(t, "openai/gpt-4o", resolveForRequest(t, context.Background(), svc, "smart"))
	})

	t.Run("cost", func(t *testing.T) {
		t.Parallel()
		svc := newBalancingService(t)
		// The disabled groq/llama is the cheapest leaf behind "budget"; the
		// leg must be priced at anthropic/claude, which is dearer than gpt-4o.
		upsertRedirect(t, svc, "budget", StrategyCost, "anthropic/claude", "groq/llama")
		upsertRedirect(t, svc, "frugal", StrategyCost, "budget", "openai/gpt-4o")
		disableModel(t, svc, "groq/llama")

		assert.Equal(t, "openai/gpt-4o", resolveForRequest(t, context.Background(), svc, "frugal"))
	})
}
