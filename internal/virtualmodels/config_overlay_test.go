package virtualmodels

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/require"
)

func TestConfigModels_Conversion(t *testing.T) {
	t.Parallel()
	enabled := false
	got := ConfigModels([]config.VirtualModelConfig{
		{Source: "alias", Target: "openai/gpt-4o", Slowdown: new(0.5)},
		{
			Source:   "smart",
			Strategy: StrategyCost,
			Targets: []config.VirtualModelTargetConfig{
				{Provider: "openai", Model: "gpt-4o", Weight: 2},
				{Model: "groq/llama"},
			},
		},
		{Source: "off", Target: "openai/gpt-4o", Enabled: &enabled},
	})

	require.Len(t, got, 3)
	require.Equal(t, "openai/gpt-4o", got[0].Targets[0].Model)
	require.NotNil(t, got[0].Slowdown)
	require.Equal(t, 0.5, *got[0].Slowdown)
	require.True(t, got[0].Enabled)
	require.True(t, got[0].Managed, "shorthand target conversion = %#v", got[0])
	require.Len(t, got[1].Targets, 2)
	require.Equal(t, StrategyCost, got[1].Strategy)
	require.Equal(t, float64(2), got[1].Targets[0].Weight, "multi-target conversion = %#v", got[1])
	require.False(t, got[2].Enabled, "explicit enabled=false not honored: %#v", got[2])
}

func TestConfigModels_PreservesSlowdownPresence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   *float64
		want *float64
	}{
		{name: "omitted", in: nil, want: nil},
		{name: "explicit zero", in: new(0.0), want: new(0.0)},
		{name: "minimum active factor", in: new(MinSlowdownFactor), want: new(MinSlowdownFactor)},
		{name: "maximum active factor", in: new(MaxSlowdownFactor), want: new(MaxSlowdownFactor)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ConfigModels([]config.VirtualModelConfig{{
				Source: "alias", Target: "openai/gpt-4o", Slowdown: tt.in,
			}})
			require.Len(t, got, 1)

			if tt.want == nil {
				require.Nil(t, got[0].Slowdown)
				return
			}
			require.NotNil(t, got[0].Slowdown)
			require.Equal(t, *tt.want, *got[0].Slowdown)
		})
	}
}

func TestService_ConfigOverlayResolvesAndIsReadOnly(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	ctx := context.Background()

	svc.SetConfigModels(ConfigModels([]config.VirtualModelConfig{{
		Source:   "smart",
		Strategy: StrategyRoundRobin,
		Targets: []config.VirtualModelTargetConfig{
			{Provider: "openai", Model: "gpt-4o"},
			{Provider: "groq", Model: "llama"},
		},
	}}))
	err := svc.Refresh(ctx)
	require.NoError(t, err)

	// The managed redirect resolves and load balances.
	counts := countByModel(resolvedModels(t, svc, "smart", 4))
	require.Equal(t, 2, counts["openai/gpt-4o"])
	require.Equal(t, 2, counts["groq/llama"], "managed redirect distribution = %v", counts)

	// The admin view marks it managed.
	view, ok := svc.Get("smart")
	require.True(t, ok)
	require.True(t, view.Managed, "managed virtual model not marked managed: %#v", view)
	// Admin writes to a managed source are rejected.
	err = svc.Upsert(ctx, VirtualModel{
		Source:  "smart",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: true,
	})
	require.Error(t, err)
	require.True(t, IsValidationError(err))
	err = svc.Delete(ctx, "smart")
	require.Error(t, err)
	require.True(t, IsValidationError(err))
}

func TestService_ConfigOverlayOverridesStoreRow(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	ctx := context.Background()
	// A store row points "smart" at the expensive model.
	err := svc.store.Upsert(ctx, VirtualModel{
		Source:  "smart",
		Targets: []Target{{Provider: "anthropic", Model: "claude"}},
		Enabled: true,
	})
	require.NoError(t, err)

	// Config redefines "smart" to the cheap model; config must win.
	svc.SetConfigModels(ConfigModels([]config.VirtualModelConfig{{
		Source: "smart",
		Target: "groq/llama",
	}}))
	err = svc.Refresh(ctx)
	require.NoError(t, err)

	sel, _, err := svc.ResolveModel(core.NewRequestedModelSelector("smart", ""))
	require.NoError(t, err)
	require.Equal(t, "groq/llama", sel.QualifiedModel())
}

// Only STRUCTURAL problems (catalog-independent) abort startup. Catalog
// availability is checked at resolve time, not here — see
// TestService_ManagedRedirectToleratesColdCatalogAtStartup.
func TestService_ConfigOverlayRejectsInvalidRedirectTargets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		entries []config.VirtualModelConfig
		// rejectedByRefresh marks declarations the snapshot build itself
		// rejects; the others must be caught by ValidateManagedConfig.
		rejectedByRefresh bool
	}{
		{
			name:    "self target",
			entries: []config.VirtualModelConfig{{Source: "smart", Target: "smart"}},
		},
		{
			name: "chain cycle",
			entries: []config.VirtualModelConfig{
				{Source: "smart", Target: "other"},
				{Source: "other", Target: "smart"},
			},
			rejectedByRefresh: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc := newBalancingService(t)
			svc.SetConfigModels(ConfigModels(tt.entries))
			// Startup mirrors the factory: Refresh builds the snapshot (which
			// rejects chain cycles), then the managed-config check rejects the
			// remaining invalid declarations.
			err := svc.Refresh(context.Background())
			require.Equal(t, tt.rejectedByRefresh, err != nil)

			if err == nil {
				err = svc.ValidateManagedConfig(nil)
			}
			require.Error(t, err)
			require.True(t, IsValidationError(err))
		})
	}
}

// A declarative virtual model may chain through another declarative one.
func TestService_ConfigOverlayChainsVirtualModels(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	svc.SetConfigModels(ConfigModels([]config.VirtualModelConfig{
		{Source: "production", Target: "cheap"},
		{Source: "cheap", Target: "groq/llama"},
	}))
	err := svc.Refresh(context.Background())
	require.NoError(t, err)
	err = svc.ValidateManagedConfig(nil)
	require.NoError(t, err)

	sel, _, err := svc.ResolveModel(core.NewRequestedModelSelector("production", ""))
	require.NoError(t, err)
	require.Equal(t, "groq/llama", sel.QualifiedModel())
}

// Target provider names are static configuration known before any model loads,
// so startup validates them even though catalog availability is deferred: a
// name that matches no provider anywhere is a typo and aborts (issue #464); a
// name declared under providers: that did not register (unresolved credentials
// in this environment) only warns, so a config shared across environments still
// boots; targets without an explicit provider are never checked because their
// model may carry a non-provider prefix (a slash-shaped ID like "Qwen/x").
func TestService_ValidateManagedConfigTargetProviders(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		targets  []config.VirtualModelTargetConfig
		declared []string
		wantErr  string
	}{
		{
			name:    "registered provider passes",
			targets: []config.VirtualModelTargetConfig{{Provider: "openai", Model: "gpt-4o"}},
		},
		{
			name:    "misspelled provider aborts startup",
			targets: []config.VirtualModelTargetConfig{{Provider: "opnai", Model: "gpt-4o"}},
			wantErr: `unknown target provider "opnai"`,
		},
		{
			name:     "declared but unregistered provider only warns",
			targets:  []config.VirtualModelTargetConfig{{Provider: "anthropic", Model: "claude"}},
			declared: []string{"anthropic"},
		},
		{
			name:    "provider-agnostic slash model is not treated as a provider",
			targets: []config.VirtualModelTargetConfig{{Model: "Qwen/Qwen3-1.7B"}},
		},
		{
			name: "typo among several targets still aborts",
			targets: []config.VirtualModelTargetConfig{
				{Provider: "openai", Model: "gpt-4o"},
				{Provider: "uraninum", Model: "gemma"},
			},
			declared: []string{"uranium"},
			wantErr:  `unknown target provider "uraninum"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc, err := NewService(newSQLVMStore(t), testCatalog(), true)
			require.NoError(t, err)

			svc.SetConfigModels(ConfigModels([]config.VirtualModelConfig{{
				Source:  "smart",
				Targets: tt.targets,
			}}))
			err = svc.Refresh(context.Background())
			require.NoError(t, err)

			err = svc.ValidateManagedConfig(tt.declared)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
			require.True(t, IsValidationError(err))
		})
	}
}

// The admin write path rejects an explicitly-named provider that is not
// registered, with a message that points at the provider rather than the model.
func TestService_UpsertRejectsUnknownTargetProvider(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	err := svc.Upsert(context.Background(), VirtualModel{
		Source:  "smart",
		Targets: []Target{{Provider: "opnai", Model: "gpt-4o"}},
		Enabled: true,
	})
	require.ErrorContains(t, err, `unknown target provider "opnai"`)
	require.True(t, IsValidationError(err))
}

// A managed redirect declared against a model the catalog cannot serve YET — a
// cold catalog, because the provider model list loads asynchronously after
// startup — must NOT abort startup. ValidateManagedConfig checks structure only;
// the redirect simply starts unavailable and begins resolving once the catalog
// warms, exactly like the resolve path skips any unavailable target. Regression
// test for the cold-catalog startup-abort bug.
func TestService_ManagedRedirectToleratesColdCatalogAtStartup(t *testing.T) {
	t.Parallel()
	supported := map[string]core.Model{} // cold: no provider models loaded yet
	store := newSQLVMStore(t)
	svc, err := NewService(store, fakeCatalog{providers: []string{"openai"}, supported: supported}, true)
	require.NoError(t, err)

	ctx := context.Background()

	// Startup against a cold catalog must succeed for a structurally-valid target.
	svc.SetConfigModels(ConfigModels([]config.VirtualModelConfig{{Source: "smart", Target: "openai/gpt-4o"}}))
	err = svc.Refresh(ctx)
	require.NoError(t, err)
	err = svc.ValidateManagedConfig(nil)
	require.NoError(t, err)
	// While the catalog is cold the redirect is simply unavailable, not fatal.
	_, changed, _ := svc.ResolveModel(core.NewRequestedModelSelector("smart", ""))
	require.False(t, changed)

	// Once the async model load warms the catalog, the same redirect resolves —
	// supportedTargets consults the live catalog at resolve time, no refresh needed.
	supported["openai/gpt-4o"] = core.Model{ID: "openai/gpt-4o", Object: "model", OwnedBy: "openai"}
	sel, changed, _ := svc.ResolveModel(core.NewRequestedModelSelector("smart", ""))
	require.True(t, changed)
	require.Equal(t, "openai/gpt-4o", sel.QualifiedModel())
}

// The admin write path keeps the catalog-availability check: it runs against a
// warm catalog, so a target the catalog cannot serve is a caller mistake.
func TestService_UpsertRejectsUnsupportedTarget(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	err := svc.Upsert(context.Background(), VirtualModel{
		Source:  "smart",
		Targets: []Target{{Provider: "openai", Model: "unknown"}},
		Enabled: true,
	})
	require.Error(t, err)
	require.True(t, IsValidationError(err))
}

// A managed redirect target that drops out of the catalog after startup must not
// freeze the snapshot: the validation gate runs once, so a later refresh still
// swaps in store changes and only marks the affected redirect unavailable.
func TestService_ManagedRedirectToleratesTransientCatalogGapAfterStartup(t *testing.T) {
	t.Parallel()
	supported := map[string]core.Model{
		"openai/gpt-4o":      {ID: "openai/gpt-4o", Object: "model", OwnedBy: "openai"},
		"openai/gpt-4o-mini": {ID: "openai/gpt-4o-mini", Object: "model", OwnedBy: "openai"},
	}
	store := newSQLVMStore(t)
	svc, err := NewService(store, fakeCatalog{providers: []string{"openai"}, supported: supported}, true)
	require.NoError(t, err)

	ctx := context.Background()

	// Startup: the managed redirect's target is supported, so validation passes.
	svc.SetConfigModels(ConfigModels([]config.VirtualModelConfig{{Source: "smart", Target: "openai/gpt-4o"}}))
	err = svc.Refresh(ctx)
	require.NoError(t, err)
	err = svc.ValidateManagedConfig(nil)
	require.NoError(t, err)

	// A provider catalog refresh transiently drops the managed target, while an
	// unrelated store alias is added that a working refresh must surface.
	delete(supported, "openai/gpt-4o")
	err = store.Upsert(ctx, VirtualModel{
		Source:  "later",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o-mini"}},
		Enabled: true,
	})
	require.NoError(t, err)
	// The refresh must not fail despite the now-unsupported managed target.
	err = svc.Refresh(ctx)
	require.NoError(t, err)
	// The snapshot swapped: the new store alias is visible.
	_, ok := svc.Get("later")
	require.True(t, ok, "snapshot did not swap: alias %q missing after refresh", "later")
	// The managed redirect is simply unavailable while its target is gone.
	_, changed, _ := svc.ResolveModel(core.NewRequestedModelSelector("smart", ""))
	require.False(t, changed)

	// When the target returns, the managed redirect resolves again.
	supported["openai/gpt-4o"] = core.Model{ID: "openai/gpt-4o", Object: "model", OwnedBy: "openai"}
	err = svc.Refresh(ctx)
	require.NoError(t, err)
	sel, changed, _ := svc.ResolveModel(core.NewRequestedModelSelector("smart", ""))
	require.True(t, changed)
	require.Equal(t, "openai/gpt-4o", sel.QualifiedModel())
}
