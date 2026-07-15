package virtualmodels

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(newSQLiteVMStore(t), testCatalog(), true)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return svc
}

func TestService_RedirectResolves(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Upsert(ctx, VirtualModel{
		Source:  "fast",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert(redirect) error = %v", err)
	}

	sel, changed, err := svc.ResolveModel(core.NewRequestedModelSelector("fast", ""))
	if err != nil {
		t.Fatalf("ResolveModel() error = %v", err)
	}
	if !changed {
		t.Fatalf("ResolveModel() changed = false, want true")
	}
	if sel.QualifiedModel() != "openai/gpt-4o" {
		t.Fatalf("ResolveModel() = %q, want openai/gpt-4o", sel.QualifiedModel())
	}
}

func TestService_PolicyGatesAccess(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Upsert(ctx, VirtualModel{
		Source:    "openai/gpt-4o",
		UserPaths: []string{"/team"},
		Enabled:   true,
	}); err != nil {
		t.Fatalf("Upsert(policy) error = %v", err)
	}

	selector := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}

	// No user path on the request -> access denied.
	if err := svc.ValidateModelAccess(ctx, selector); err == nil {
		t.Fatalf("ValidateModelAccess(no path) error = nil, want denied")
	}

	// Matching ancestor user path -> allowed.
	allowedCtx := core.WithEffectiveUserPath(ctx, "/team/alice")
	if err := svc.ValidateModelAccess(allowedCtx, selector); err != nil {
		t.Fatalf("ValidateModelAccess(/team/alice) error = %v, want allowed", err)
	}
}

func TestService_DisabledPolicyTurnsModelOff(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	// Default-on catalog model, then a disabled policy row for it.
	if err := svc.Upsert(ctx, VirtualModel{Source: "openai/gpt-4o", Enabled: false}); err != nil {
		t.Fatalf("Upsert(disabled policy) error = %v", err)
	}

	selector := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}
	state := svc.EffectiveState(selector)
	if state.Enabled {
		t.Fatalf("EffectiveState.Enabled = true, want false (disabled policy)")
	}
	if svc.AllowsModel(ctx, selector) {
		t.Fatalf("AllowsModel = true, want false (disabled policy)")
	}
	if err := svc.ValidateModelAccess(ctx, selector); err == nil {
		t.Fatalf("ValidateModelAccess = nil, want denied (disabled policy)")
	}
}

func TestService_EnabledPolicyEmptyUserPathsAllowsAll(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	// Empty user_paths is allowed and means "all paths".
	if err := svc.Upsert(ctx, VirtualModel{Source: "openai/gpt-4o", Enabled: true}); err != nil {
		t.Fatalf("Upsert(policy, empty user_paths) error = %v", err)
	}

	selector := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}
	if !svc.AllowsModel(ctx, selector) {
		t.Fatalf("AllowsModel = false, want true (empty user_paths allows all)")
	}
	if err := svc.ValidateModelAccess(ctx, selector); err != nil {
		t.Fatalf("ValidateModelAccess error = %v, want allowed", err)
	}
}

func TestService_RejectsCrossKindClobber(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Upsert(ctx, VirtualModel{Source: "gpt-fast", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true}); err != nil {
		t.Fatalf("Upsert(redirect) error = %v", err)
	}
	// A policy with the same source must be rejected, not silently clobber it.
	err := svc.Upsert(ctx, VirtualModel{Source: "gpt-fast", UserPaths: []string{"/team"}})
	if err == nil {
		t.Fatalf("Upsert(policy over redirect) error = nil, want rejection")
	}
	if !IsValidationError(err) {
		t.Fatalf("Upsert(policy over redirect) error = %v, want validation error", err)
	}

	// The redirect must survive intact.
	got, getErr := svc.store.Get(ctx, "gpt-fast")
	if getErr != nil {
		t.Fatalf("store.Get() error = %v", getErr)
	}
	if !got.IsRedirect() {
		t.Fatalf("redirect was clobbered: %#v", got)
	}
}

func TestService_AcceptsMultiTargetRedirect(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	ctx := context.Background()

	if err := svc.Upsert(ctx, VirtualModel{
		Source:   "smart",
		Strategy: StrategyRoundRobin,
		Targets: []Target{
			{Provider: "openai", Model: "gpt-4o"},
			{Provider: "anthropic", Model: "claude"},
		},
		Enabled: true,
	}); err != nil {
		t.Fatalf("Upsert(multi-target) error = %v", err)
	}

	view, ok := svc.Get("smart")
	if !ok {
		t.Fatalf("Get(smart) not found after upsert")
	}
	if len(view.Targets) != 2 {
		t.Fatalf("stored targets = %d, want 2", len(view.Targets))
	}
	if view.Strategy != StrategyRoundRobin {
		t.Fatalf("stored strategy = %q, want %q", view.Strategy, StrategyRoundRobin)
	}
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
	if err == nil {
		t.Fatalf("Upsert(unknown strategy) error = nil, want rejection")
	}
	if !IsValidationError(err) {
		t.Fatalf("Upsert(unknown strategy) error = %v, want validation error", err)
	}
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
	if err == nil {
		t.Fatalf("Upsert(self-target) error = nil, want rejection")
	}
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
	if err == nil {
		t.Fatalf("Upsert(missing target) error = nil, want rejection")
	}
}

func TestService_RejectsRedirectTargetingAnotherRedirect(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Upsert(ctx, VirtualModel{Source: "fast", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true}); err != nil {
		t.Fatalf("Upsert(fast) error = %v", err)
	}
	// "faster" targeting "fast" (another redirect's source) must be rejected.
	err := svc.Upsert(ctx, VirtualModel{Source: "faster", Targets: []Target{{Model: "fast"}}, Enabled: true})
	if err == nil {
		t.Fatalf("Upsert(redirect targeting redirect) error = nil, want rejection")
	}
}

func TestService_DisabledRedirectDoesNotResolveOrExpose(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Upsert(ctx, VirtualModel{Source: "fast", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: false}); err != nil {
		t.Fatalf("Upsert(disabled redirect) error = %v", err)
	}

	if _, changed, _ := svc.ResolveModel(core.NewRequestedModelSelector("fast", "")); changed {
		t.Fatalf("ResolveModel(disabled redirect) changed = true, want false")
	}
	if svc.Supports("fast") {
		t.Fatalf("Supports(disabled redirect) = true, want false")
	}
	if exposed := svc.ExposedModels(); len(exposed) != 0 {
		t.Fatalf("ExposedModels = %#v, want empty for disabled redirect", exposed)
	}
}

func TestService_ExposedModelsProjectsEnabledRedirects(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Upsert(ctx, VirtualModel{Source: "fast", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true}); err != nil {
		t.Fatalf("Upsert(redirect) error = %v", err)
	}

	exposed := svc.ExposedModels()
	if len(exposed) != 1 || exposed[0].ID != "fast" {
		t.Fatalf("ExposedModels = %#v, want one entry with ID fast", exposed)
	}
}

func TestService_ExposedModelsForUserPathHidesScopedRedirects(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Upsert(ctx, VirtualModel{Source: "open", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true}); err != nil {
		t.Fatalf("Upsert(open) error = %v", err)
	}
	if err := svc.Upsert(ctx, VirtualModel{Source: "team-only", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, UserPaths: []string{"/team"}, Enabled: true}); err != nil {
		t.Fatalf("Upsert(team-only) error = %v", err)
	}

	ids := func(models []core.Model) map[string]bool {
		out := make(map[string]bool, len(models))
		for _, m := range models {
			out[m.ID] = true
		}
		return out
	}

	// Matching caller sees both; non-matching caller sees only the unscoped one.
	if got := ids(svc.ExposedModelsForUserPath("/team/alice", nil)); !got["open"] || !got["team-only"] {
		t.Fatalf("ExposedModelsForUserPath(/team/alice) = %v, want open and team-only", got)
	}
	if got := ids(svc.ExposedModelsForUserPath("/other", nil)); !got["open"] || got["team-only"] {
		t.Fatalf("ExposedModelsForUserPath(/other) = %v, want open only (team-only hidden)", got)
	}
	// The unscoped filter is unchanged (backward compatible).
	if got := ids(svc.ExposedModelsFiltered(nil)); !got["open"] || !got["team-only"] {
		t.Fatalf("ExposedModelsFiltered = %v, want both", got)
	}
}

func TestService_ExplicitProviderBypassesRedirect(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Upsert(ctx, VirtualModel{Source: "fast", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true}); err != nil {
		t.Fatalf("Upsert(redirect) error = %v", err)
	}

	// Explicit provider means no redirect lookup.
	_, changed, err := svc.ResolveModel(core.RequestedModelSelector{Model: "fast", ExplicitProvider: true, ProviderHint: "openai"})
	if err != nil {
		t.Fatalf("ResolveModel(explicit) error = %v", err)
	}
	if changed {
		t.Fatalf("ResolveModel(explicit provider) changed = true, want false")
	}
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
	if err == nil {
		t.Fatal("Upsert(redirect with invalid user_path) error = nil, want validation error")
	}
	if !IsValidationError(err) {
		t.Fatalf("Upsert() error type = %T, want validation error", err)
	}
	if _, ok := svc.Get("smart"); ok {
		t.Fatal("invalid redirect should not have been stored")
	}
}

func TestService_ScopedRedirectAppliesOnlyToMatchingUserPath(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	// A redirect scoped to /team: applies for callers under /team, falls
	// through to the literal name for everyone else (PR #387 use case).
	if err := svc.Upsert(ctx, VirtualModel{
		Source:    "smart",
		Targets:   []Target{{Provider: "openai", Model: "gpt-4o"}},
		UserPaths: []string{"/team"},
		Enabled:   true,
	}); err != nil {
		t.Fatalf("Upsert(scoped redirect) error = %v", err)
	}

	requested := core.NewRequestedModelSelector("smart", "")

	// Matching caller: redirect applies.
	matchCtx := core.WithEffectiveUserPath(ctx, "/team/alice")
	sel, changed, err := svc.ResolveModelForUserPath(matchCtx, requested)
	if err != nil {
		t.Fatalf("ResolveModelForUserPath(/team/alice) error = %v", err)
	}
	if !changed || sel.QualifiedModel() != "openai/gpt-4o" {
		t.Fatalf("ResolveModelForUserPath(/team/alice) = %q changed=%v, want openai/gpt-4o true", sel.QualifiedModel(), changed)
	}

	// Non-matching caller: redirect does not apply, falls through to literal.
	missCtx := core.WithEffectiveUserPath(ctx, "/other")
	sel, changed, err = svc.ResolveModelForUserPath(missCtx, requested)
	if err != nil {
		t.Fatalf("ResolveModelForUserPath(/other) error = %v", err)
	}
	if changed || sel.Model != "smart" {
		t.Fatalf("ResolveModelForUserPath(/other) = %q changed=%v, want smart false (fall through)", sel.QualifiedModel(), changed)
	}

	// No user path at all: also falls through.
	if _, changed, _ := svc.ResolveModelForUserPath(ctx, requested); changed {
		t.Fatalf("ResolveModelForUserPath(no path) changed = true, want false")
	}

	// Unscoped ResolveModel still resolves regardless of user path (used by
	// Supports/exposed-model projection).
	if _, changed, _ := svc.ResolveModel(requested); !changed {
		t.Fatalf("ResolveModel(unscoped) changed = false, want true")
	}
}

func TestService_PolicyScopePrecedence(t *testing.T) {
	t.Parallel()
	catalog := fakeCatalog{
		providers: []string{"openai"},
		supported: map[string]core.Model{
			"openai/gpt-4o": {ID: "openai/gpt-4o", Object: "model", OwnedBy: "openai"},
		},
	}
	svc, err := NewService(newSQLiteVMStore(t), catalog, true)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	ctx := context.Background()

	// Global, provider-wide, and exact policies; exact must win.
	if err := svc.Upsert(ctx, VirtualModel{Source: "/", UserPaths: []string{"/global"}, Enabled: true}); err != nil {
		t.Fatalf("Upsert(global) error = %v", err)
	}
	if err := svc.Upsert(ctx, VirtualModel{Source: "openai/", UserPaths: []string{"/provider"}, Enabled: true}); err != nil {
		t.Fatalf("Upsert(provider-wide) error = %v", err)
	}
	if err := svc.Upsert(ctx, VirtualModel{Source: "openai/gpt-4o", UserPaths: []string{"/exact"}, Enabled: true}); err != nil {
		t.Fatalf("Upsert(exact) error = %v", err)
	}

	state := svc.EffectiveState(core.ModelSelector{Provider: "openai", Model: "gpt-4o"})
	if len(state.UserPaths) != 1 || state.UserPaths[0] != "/exact" {
		t.Fatalf("EffectiveState.UserPaths = %#v, want [/exact] (exact wins)", state.UserPaths)
	}

	// A model with no exact/provider match falls back to global.
	otherState := svc.EffectiveState(core.ModelSelector{Provider: "anthropic", Model: "claude"})
	if len(otherState.UserPaths) != 1 || otherState.UserPaths[0] != "/global" {
		t.Fatalf("EffectiveState(anthropic/claude).UserPaths = %#v, want [/global]", otherState.UserPaths)
	}
}

func TestService_FilterPublicModels(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Upsert(ctx, VirtualModel{Source: "openai/gpt-4o", UserPaths: []string{"/team"}, Enabled: true}); err != nil {
		t.Fatalf("Upsert(policy) error = %v", err)
	}

	models := []core.Model{{ID: "openai/gpt-4o"}}

	// Without the user path the model is filtered out.
	if got := svc.FilterPublicModels(ctx, models); len(got) != 0 {
		t.Fatalf("FilterPublicModels(no path) = %#v, want empty", got)
	}
	// With a matching ancestor the model is retained.
	allowedCtx := core.WithEffectiveUserPath(ctx, "/team/alice")
	if got := svc.FilterPublicModels(allowedCtx, models); len(got) != 1 {
		t.Fatalf("FilterPublicModels(/team/alice) = %#v, want one model", got)
	}
}

func TestService_ListViewsAndDeleteRoute(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Upsert(ctx, VirtualModel{Source: "fast", Targets: []Target{{Provider: "openai", Model: "gpt-4o"}}, Enabled: true}); err != nil {
		t.Fatalf("Upsert(redirect) error = %v", err)
	}
	if err := svc.Upsert(ctx, VirtualModel{Source: "openai/gpt-4o", UserPaths: []string{"/team"}, Enabled: true}); err != nil {
		t.Fatalf("Upsert(policy) error = %v", err)
	}

	views := svc.ListViews()
	if len(views) != 2 {
		t.Fatalf("len(ListViews()) = %d, want 2", len(views))
	}
	kinds := map[string]View{}
	for _, v := range views {
		kinds[v.Source] = v
	}
	if got := kinds["fast"]; got.Kind != KindRedirect || got.ResolvedModel != "openai/gpt-4o" || !got.Valid {
		t.Fatalf("views[fast] = %#v, want valid redirect to openai/gpt-4o", got)
	}
	if got := kinds["openai/gpt-4o"]; got.Kind != KindPolicy || got.ScopeKind == "" {
		t.Fatalf("views[openai/gpt-4o] = %#v, want policy with scope", got)
	}

	if err := svc.Delete(ctx, "fast"); err != nil {
		t.Fatalf("Delete(fast) error = %v", err)
	}
	if err := svc.Delete(ctx, "openai/gpt-4o"); err != nil {
		t.Fatalf("Delete(openai/gpt-4o) error = %v", err)
	}
	if views := svc.ListViews(); len(views) != 0 {
		t.Fatalf("len(ListViews()) after delete = %d, want 0", len(views))
	}
}

func TestService_DeleteMissingReturnsErrNotFound(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	if err := svc.Delete(context.Background(), "nope"); err != ErrNotFound {
		t.Fatalf("Delete(missing) error = %v, want ErrNotFound", err)
	}
}

func TestService_ResolveUpsertEnabled(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	if err := svc.Upsert(ctx, VirtualModel{
		Source:  "fast",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: false,
	}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	enabled := true
	if got := svc.ResolveUpsertEnabled("fast", "", &enabled); !got {
		t.Fatal("explicit request value must win over the stored flag")
	}
	if got := svc.ResolveUpsertEnabled("fast", "", nil); got {
		t.Fatal("omitted flag must preserve the stored (disabled) value")
	}
	if got := svc.ResolveUpsertEnabled("renamed", "fast", nil); got {
		t.Fatal("rename must preserve the flag of the row being renamed")
	}
	if got := svc.ResolveUpsertEnabled("brand-new", "", nil); !got {
		t.Fatal("new rows must default to enabled")
	}
}
