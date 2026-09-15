package virtualmodels

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(newSQLVMStore(t), testCatalog(), true)
	require.NoError(t, err)

	return svc
}

func TestService_RedirectResolves(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	err := svc.Upsert(ctx, VirtualModel{
		Source:  "fast",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: true,
	})
	require.NoError(t, err)

	sel, changed, err := svc.ResolveModel(core.NewRequestedModelSelector("fast", ""))
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "openai/gpt-4o", sel.QualifiedModel())
}

func TestService_PolicyGatesAccess(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	err := svc.Upsert(ctx, VirtualModel{
		Source:    "openai/gpt-4o",
		UserPaths: []string{"/team"},
		Enabled:   true,
	})
	require.NoError(t, err)

	selector := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}

	// No user path on the request -> access denied.
	require.Error(t, svc.ValidateModelAccess(ctx, selector))

	// Matching ancestor user path -> allowed.
	allowedCtx := core.WithEffectiveUserPath(ctx, "/team/alice")
	err = svc.ValidateModelAccess(allowedCtx, selector)
	require.NoError(t, err)
}

func TestService_DisabledPolicyTurnsModelOff(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	// Default-on catalog model, then a disabled policy row for it.
	err := svc.Upsert(ctx, VirtualModel{Source: "openai/gpt-4o", Enabled: false})
	require.NoError(t, err)

	selector := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}
	state := svc.EffectiveState(selector)
	require.False(t, state.Enabled)
	require.False(t, svc.AllowsModel(ctx, selector))
	require.Error(t, svc.ValidateModelAccess(ctx, selector))
}

func TestService_EnabledPolicyEmptyUserPathsAllowsAll(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	// Empty user_paths is allowed and means "all paths".
	err := svc.Upsert(ctx, VirtualModel{Source: "openai/gpt-4o", Enabled: true})
	require.NoError(t, err)

	selector := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}
	require.True(t, svc.AllowsModel(ctx, selector))
	err = svc.ValidateModelAccess(ctx, selector)
	require.NoError(t, err)
}

func TestService_UpsertReplacesRedirectWithPolicy(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	err := svc.Upsert(ctx, VirtualModel{
		Source:      "gpt-fast",
		Targets:     []Target{{Provider: "openai", Model: "gpt-4o"}},
		Description: "Team model",
		UserPaths:   []string{"/team"},
		Enabled:     true,
	})
	require.NoError(t, err)
	err = svc.Upsert(ctx, VirtualModel{
		Source:      "gpt-fast",
		Description: "Team model",
		UserPaths:   []string{"/team"},
		Enabled:     true,
	})
	require.NoError(t, err)

	got, ok := svc.Get("gpt-fast")
	require.True(t, ok)
	require.False(t, got.IsRedirect())
	require.Equal(t, "Team model", got.Description)
	require.Len(t, got.UserPaths, 1)
	require.Equal(t, "/team", got.UserPaths[0], "replacement policy = %#v", got)
}

func TestService_AcceptsMultiTargetRedirect(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	ctx := context.Background()
	err := svc.Upsert(ctx, VirtualModel{
		Source:   "smart",
		Strategy: StrategyRoundRobin,
		Targets: []Target{
			{Provider: "openai", Model: "gpt-4o"},
			{Provider: "anthropic", Model: "claude"},
		},
		Enabled: true,
	})
	require.NoError(t, err)

	view, ok := svc.Get("smart")
	require.True(t, ok)
	require.Len(t, view.Targets, 2)
	require.Equal(t, StrategyRoundRobin, view.Strategy)
}

func TestService_RejectsUnknownStrategy(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)

	err := svc.Upsert(context.Background(), VirtualModel{
		Source:   "smart",
		Strategy: "least-latency",
		Targets:  []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled:  true,
	})
	require.Error(t, err)
	require.True(t, IsValidationError(err))
}

func TestService_RejectsSelfTargetingRedirect(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	err := svc.Upsert(ctx, VirtualModel{
		Source:  "openai/gpt-4o",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: true,
	})
	require.Error(t, err)
}

func TestService_RejectsRedirectToMissingTarget(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	err := svc.Upsert(ctx, VirtualModel{
		Source:  "fast",
		Targets: []Target{{Provider: "openai", Model: "missing"}},
		Enabled: true,
	})
	require.Error(t, err)
}

func TestService_DisabledRedirectDoesNotResolveOrExpose(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	err := svc.Upsert(ctx, VirtualModel{Source: "fast", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: false})
	require.NoError(t, err)
	_, changed, _ := svc.ResolveModel(core.NewRequestedModelSelector("fast", ""))
	require.False(t, changed)
	require.False(t, svc.Supports("fast"))
	exposed := svc.ExposedModels()
	require.Empty(t, exposed)
}

func TestService_ExposedModelsProjectsEnabledRedirects(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	err := svc.Upsert(ctx, VirtualModel{Source: "fast", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true})
	require.NoError(t, err)

	exposed := svc.ExposedModels()
	require.Len(t, exposed, 1)
	require.Equal(t, "fast", exposed[0].ID)
}

func TestService_ExposedModelsForUserPathHidesScopedRedirects(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	err := svc.Upsert(ctx, VirtualModel{Source: "open", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true})
	require.NoError(t, err)
	err = svc.Upsert(ctx, VirtualModel{Source: "team-only", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, UserPaths: []string{"/team"}, Enabled: true})
	require.NoError(t, err)

	ids := func(models []core.Model) map[string]bool {
		out := make(map[string]bool, len(models))
		for _, m := range models {
			out[m.ID] = true
		}
		return out
	}
	// Matching caller sees both; non-matching caller sees only the unscoped one.
	got := ids(svc.ExposedModelsForUserPath("/team/alice", nil))
	require.True(t, got["open"])
	require.True(t, got["team-only"], "ExposedModelsForUserPath(/team/alice) = %v, want open and team-only", got)

	got = ids(svc.ExposedModelsForUserPath("/other", nil))
	require.True(t, got["open"])
	require.False(t, got["team-only"], "ExposedModelsForUserPath(/other) = %v, want open only (team-only hidden)", got)
	// The unscoped filter is unchanged (backward compatible).
	got = ids(svc.ExposedModelsFiltered(nil))
	require.True(t, got["open"])
	require.True(t, got["team-only"], "ExposedModelsFiltered = %v, want both", got)
}

func TestService_ExplicitProviderBypassesRedirect(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	err := svc.Upsert(ctx, VirtualModel{Source: "fast", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true})
	require.NoError(t, err)

	// Explicit provider means no redirect lookup.
	_, changed, err := svc.ResolveModel(core.RequestedModelSelector{Model: "fast", ExplicitProvider: true, ProviderHint: "openai"})
	require.NoError(t, err)
	require.False(t, changed)
}

func TestService_RejectsRedirectWithInvalidUserPath(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	// An invalid user_path (contains ':') must fail loudly rather than be
	// silently dropped, which would widen the scoped redirect to all callers.
	err := svc.Upsert(context.Background(), VirtualModel{
		Source:    "smart",
		Targets:   []Target{{Provider: "openai", Model: "gpt-4o"}},
		UserPaths: []string{"/bad:path"},
		Enabled:   true,
	})
	require.Error(t, err)
	require.True(t, IsValidationError(err))
	_, ok := svc.Get("smart")
	require.False(t, ok)
}

func TestService_ScopedRedirectAppliesOnlyToMatchingUserPath(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	// A redirect scoped to /team: applies for callers under /team, falls
	// through to the literal name for everyone else (PR #387 use case).
	err := svc.Upsert(ctx, VirtualModel{
		Source:    "smart",
		Targets:   []Target{{Provider: "openai", Model: "gpt-4o"}},
		UserPaths: []string{"/team"},
		Enabled:   true,
	})
	require.NoError(t, err)

	requested := core.NewRequestedModelSelector("smart", "")

	// Matching caller: redirect applies.
	matchCtx := core.WithEffectiveUserPath(ctx, "/team/alice")
	sel, changed, err := svc.ResolveModelForUserPath(matchCtx, requested)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "openai/gpt-4o", sel.QualifiedModel())

	// Non-matching caller: redirect does not apply, falls through to literal.
	missCtx := core.WithEffectiveUserPath(ctx, "/other")
	sel, changed, err = svc.ResolveModelForUserPath(missCtx, requested)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, "smart", sel.Model)
	// No user path at all: also falls through.
	_, changed, _ = svc.ResolveModelForUserPath(ctx, requested)
	require.False(t, changed)
	// Unscoped ResolveModel still resolves regardless of user path (used by
	// Supports/exposed-model projection).
	_, changed, _ = svc.ResolveModel(requested)
	require.True(t, changed)
}

func TestService_PolicyScopePrecedence(t *testing.T) {
	t.Parallel()
	catalog := fakeCatalog{
		providers: []string{"openai"},
		supported: map[string]core.Model{
			"openai/gpt-4o": {ID: "openai/gpt-4o", Object: "model", OwnedBy: "openai"},
		},
	}
	svc, err := NewService(newSQLVMStore(t), catalog, true)
	require.NoError(t, err)

	ctx := context.Background()
	// Global, provider-wide, and exact policies; exact must win.
	err = svc.Upsert(ctx, VirtualModel{Source: "/", UserPaths: []string{"/global"}, Enabled: true})
	require.NoError(t, err)
	err = svc.Upsert(ctx, VirtualModel{Source: "openai/", UserPaths: []string{"/provider"}, Enabled: true})
	require.NoError(t, err)
	err = svc.Upsert(ctx, VirtualModel{Source: "openai/gpt-4o", UserPaths: []string{"/exact"}, Enabled: true})
	require.NoError(t, err)

	state := svc.EffectiveState(core.ModelSelector{Provider: "openai", Model: "gpt-4o"})
	require.Len(t, state.UserPaths, 1)
	require.Equal(t, "/exact", state.UserPaths[0])

	// A model with no exact/provider match falls back to global.
	otherState := svc.EffectiveState(core.ModelSelector{Provider: "anthropic", Model: "claude"})
	require.Len(t, otherState.UserPaths, 1)
	require.Equal(t, "/global", otherState.UserPaths[0])
}

func TestService_FilterPublicModels(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	err := svc.Upsert(ctx, VirtualModel{Source: "openai/gpt-4o", UserPaths: []string{"/team"}, Enabled: true})
	require.NoError(t, err)

	models := []core.Model{{ID: "openai/gpt-4o"}}
	// Without the user path the model is filtered out.
	got := svc.FilterPublicModels(ctx, models)
	require.Empty(t, got)

	// With a matching ancestor the model is retained.
	allowedCtx := core.WithEffectiveUserPath(ctx, "/team/alice")
	got = svc.FilterPublicModels(allowedCtx, models)
	require.Len(t, got, 1)
}

func TestService_ListViewsAndDeleteRoute(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	err := svc.Upsert(ctx, VirtualModel{Source: "fast", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true})
	require.NoError(t, err)
	err = svc.Upsert(ctx, VirtualModel{Source: "openai/gpt-4o", UserPaths: []string{"/team"}, Enabled: true})
	require.NoError(t, err)

	views := svc.ListViews()
	require.Len(t, views, 2)

	kinds := map[string]View{}
	for _, v := range views {
		kinds[v.Source] = v
	}
	got := kinds["fast"]
	require.Equal(t, KindRedirect, got.Kind)
	require.Equal(t, "openai/gpt-4o", got.ResolvedModel)
	require.True(t, got.Valid, "views[fast] = %#v, want valid redirect to openai/gpt-4o", got)
	got = kinds["openai/gpt-4o"]
	require.Equal(t, KindPolicy, got.Kind)
	require.NotEmpty(t, got.ScopeKind, "views[openai/gpt-4o] = %#v, want policy with scope", got)
	err = svc.Delete(ctx, "fast")
	require.NoError(t, err)
	err = svc.Delete(ctx, "openai/gpt-4o")
	require.NoError(t, err)
	views = svc.ListViews()
	require.Empty(t, views)
}

func TestService_DeleteMissingReturnsErrNotFound(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	err := svc.Delete(context.Background(), "nope")
	require.Equal(t, ErrNotFound, err)
}

func TestService_UpsertPrunesOnlyRedundantPolicies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("matching default", func(t *testing.T) {
		svc := newTestService(t)
		err := svc.Upsert(ctx, VirtualModel{
			Source:      "openai/gpt-4o",
			Description: "temporary note",
			Enabled:     true,
		})
		require.NoError(t, err)
		err = svc.Upsert(ctx, VirtualModel{Source: "openai/gpt-4o", Enabled: true})
		require.NoError(t, err)
		_, ok := svc.Get("openai/gpt-4o")
		require.False(t, ok)
	})

	t.Run("different from default", func(t *testing.T) {
		svc, err := NewService(newSQLVMStore(t), testCatalog(), false)
		require.NoError(t, err)
		err = svc.Upsert(ctx, VirtualModel{Source: "openai/gpt-4o", Enabled: true})
		require.NoError(t, err)
		_, ok := svc.Get("openai/gpt-4o")
		require.True(t, ok)
	})

	t.Run("overrides inherited paths", func(t *testing.T) {
		svc := newTestService(t)
		err := svc.Upsert(ctx, VirtualModel{Source: "/", UserPaths: []string{"/team"}, Enabled: true})
		require.NoError(t, err)
		err = svc.Upsert(ctx, VirtualModel{Source: "openai/gpt-4o", Enabled: true})
		require.NoError(t, err)
		_, ok := svc.Get("openai/gpt-4o")
		require.True(t, ok)
	})
}

func TestService_UpsertReplacesPolicyWithRedirect(t *testing.T) {
	t.Parallel()
	catalog := testCatalog()
	catalog.supported["openai/gpt-4o-mini"] = core.Model{ID: "openai/gpt-4o-mini", Object: "model", OwnedBy: "openai"}
	svc, err := NewService(newSQLVMStore(t), catalog, true)
	require.NoError(t, err)

	ctx := context.Background()
	err = svc.Upsert(ctx, VirtualModel{
		Source:    "openai/gpt-4o",
		UserPaths: []string{"/team"},
		Enabled:   true,
	})
	require.NoError(t, err)

	err = svc.Upsert(ctx, VirtualModel{
		Source:  "openai/gpt-4o",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o-mini"}},
		Enabled: true,
	})
	require.NoError(t, err)

	vm, ok := svc.Get("openai/gpt-4o")
	require.True(t, ok)
	require.True(t, vm.IsRedirect(), "Upsert() result = %#v, found=%v; want redirect", vm, ok)
}

func TestService_ResolveUpsertEnabled(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()
	err := svc.Upsert(ctx, VirtualModel{
		Source:  "fast",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: false,
	})
	require.NoError(t, err)

	enabled := true
	got := svc.ResolveUpsertEnabled("fast", "", &enabled)
	require.True(t, got)
	got = svc.ResolveUpsertEnabled("fast", "", nil)
	require.False(t, got)
	got = svc.ResolveUpsertEnabled("renamed", "fast", nil)
	require.False(t, got)
	got = svc.ResolveUpsertEnabled("brand-new", "", nil)
	require.True(t, got)
}
