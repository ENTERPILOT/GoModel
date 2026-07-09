package virtualmodels

import (
	"context"
	"strings"
	"testing"

	"gomodel/config"
	"gomodel/internal/core"
)

func TestConfigModels_Conversion(t *testing.T) {
	t.Parallel()
	enabled := false
	got := ConfigModels([]config.VirtualModelConfig{
		{Source: "alias", Target: "openai/gpt-4o"},
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

	if len(got) != 3 {
		t.Fatalf("ConfigModels len = %d, want 3", len(got))
	}
	if got[0].Targets[0].Model != "openai/gpt-4o" || !got[0].Enabled || !got[0].Managed {
		t.Fatalf("shorthand target conversion = %#v", got[0])
	}
	if len(got[1].Targets) != 2 || got[1].Strategy != StrategyCost || got[1].Targets[0].Weight != 2 {
		t.Fatalf("multi-target conversion = %#v", got[1])
	}
	if got[2].Enabled {
		t.Fatalf("explicit enabled=false not honored: %#v", got[2])
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
	if err := svc.Refresh(ctx); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	// The managed redirect resolves and load balances.
	counts := countByModel(resolvedModels(t, svc, "smart", 4))
	if counts["openai/gpt-4o"] != 2 || counts["groq/llama"] != 2 {
		t.Fatalf("managed redirect distribution = %v", counts)
	}

	// The admin view marks it managed.
	view, ok := svc.Get("smart")
	if !ok || !view.Managed {
		t.Fatalf("managed virtual model not marked managed: %#v", view)
	}

	// Admin writes to a managed source are rejected.
	if err := svc.Upsert(ctx, VirtualModel{
		Source:  "smart",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
		Enabled: true,
	}); err == nil || !IsValidationError(err) {
		t.Fatalf("Upsert(managed) error = %v, want validation rejection", err)
	}
	if err := svc.Delete(ctx, "smart"); err == nil || !IsValidationError(err) {
		t.Fatalf("Delete(managed) error = %v, want validation rejection", err)
	}
}

func TestService_ConfigOverlayOverridesStoreRow(t *testing.T) {
	t.Parallel()
	svc := newBalancingService(t)
	ctx := context.Background()

	// A store row points "smart" at the expensive model.
	if err := svc.store.Upsert(ctx, VirtualModel{
		Source:  "smart",
		Targets: []Target{{Provider: "anthropic", Model: "claude"}},
		Enabled: true,
	}); err != nil {
		t.Fatalf("store.Upsert() error = %v", err)
	}
	// Config redefines "smart" to the cheap model; config must win.
	svc.SetConfigModels(ConfigModels([]config.VirtualModelConfig{{
		Source: "smart",
		Target: "groq/llama",
	}}))
	if err := svc.Refresh(ctx); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	sel, _, err := svc.ResolveModel(core.NewRequestedModelSelector("smart", ""))
	if err != nil {
		t.Fatalf("ResolveModel() error = %v", err)
	}
	if sel.QualifiedModel() != "groq/llama" {
		t.Fatalf("config overlay did not override store row: got %q", sel.QualifiedModel())
	}
}

// Only STRUCTURAL problems (catalog-independent) abort startup. Catalog
// availability is checked at resolve time, not here — see
// TestService_ManagedRedirectToleratesColdCatalogAtStartup.
func TestService_ConfigOverlayRejectsInvalidRedirectTargets(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		entries []config.VirtualModelConfig
	}{
		{
			name:    "self target",
			entries: []config.VirtualModelConfig{{Source: "smart", Target: "smart"}},
		},
		{
			name: "virtual model target",
			entries: []config.VirtualModelConfig{
				{Source: "smart", Target: "other"},
				{Source: "other", Target: "openai/gpt-4o"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc := newBalancingService(t)
			svc.SetConfigModels(ConfigModels(tt.entries))
			if err := svc.Refresh(context.Background()); err != nil {
				t.Fatalf("Refresh() error = %v", err)
			}
			// Startup mirrors the factory: Refresh builds the snapshot, then the
			// managed-config check rejects invalid declarations.
			err := svc.ValidateManagedConfig(nil)
			if err == nil {
				t.Fatalf("ValidateManagedConfig() error = nil, want validation failure")
			}
			if !IsValidationError(err) {
				t.Fatalf("ValidateManagedConfig() error = %v, want validation error", err)
			}
		})
	}
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
			svc, err := NewService(newSQLiteVMStore(t), testCatalog(), true)
			if err != nil {
				t.Fatalf("NewService() error = %v", err)
			}
			svc.SetConfigModels(ConfigModels([]config.VirtualModelConfig{{
				Source:  "smart",
				Targets: tt.targets,
			}}))
			if err := svc.Refresh(context.Background()); err != nil {
				t.Fatalf("Refresh() error = %v", err)
			}
			err = svc.ValidateManagedConfig(tt.declared)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateManagedConfig() error = %v, want nil", err)
				}
				return
			}
			if err == nil || !IsValidationError(err) {
				t.Fatalf("ValidateManagedConfig() error = %v, want validation error", err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateManagedConfig() error = %q, want it to contain %q", err, tt.wantErr)
			}
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
	if err == nil || !IsValidationError(err) {
		t.Fatalf("Upsert(unknown provider) error = %v, want validation rejection", err)
	}
	if !strings.Contains(err.Error(), `unknown target provider "opnai"`) {
		t.Fatalf("Upsert(unknown provider) error = %q, want unknown-target-provider message", err)
	}
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
	store := newSQLiteVMStore(t)
	svc, err := NewService(store, fakeCatalog{providers: []string{"openai"}, supported: supported}, true)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	ctx := context.Background()

	// Startup against a cold catalog must succeed for a structurally-valid target.
	svc.SetConfigModels(ConfigModels([]config.VirtualModelConfig{{Source: "smart", Target: "openai/gpt-4o"}}))
	if err := svc.Refresh(ctx); err != nil {
		t.Fatalf("startup Refresh() error = %v", err)
	}
	if err := svc.ValidateManagedConfig(nil); err != nil {
		t.Fatalf("ValidateManagedConfig() on a cold catalog error = %v, want nil", err)
	}
	// While the catalog is cold the redirect is simply unavailable, not fatal.
	if _, changed, _ := svc.ResolveModel(core.NewRequestedModelSelector("smart", "")); changed {
		t.Fatalf("redirect resolved before its target was in the catalog")
	}

	// Once the async model load warms the catalog, the same redirect resolves —
	// supportedTargets consults the live catalog at resolve time, no refresh needed.
	supported["openai/gpt-4o"] = core.Model{ID: "openai/gpt-4o", Object: "model", OwnedBy: "openai"}
	if sel, changed, _ := svc.ResolveModel(core.NewRequestedModelSelector("smart", "")); !changed || sel.QualifiedModel() != "openai/gpt-4o" {
		t.Fatalf("redirect did not resolve after catalog warm: changed=%v sel=%q", changed, sel.QualifiedModel())
	}
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
	if err == nil || !IsValidationError(err) {
		t.Fatalf("Upsert(unsupported target) error = %v, want validation rejection", err)
	}
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
	store := newSQLiteVMStore(t)
	svc, err := NewService(store, fakeCatalog{providers: []string{"openai"}, supported: supported}, true)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	ctx := context.Background()

	// Startup: the managed redirect's target is supported, so validation passes.
	svc.SetConfigModels(ConfigModels([]config.VirtualModelConfig{{Source: "smart", Target: "openai/gpt-4o"}}))
	if err := svc.Refresh(ctx); err != nil {
		t.Fatalf("startup Refresh() error = %v", err)
	}
	if err := svc.ValidateManagedConfig(nil); err != nil {
		t.Fatalf("startup ValidateManagedConfig() error = %v", err)
	}

	// A provider catalog refresh transiently drops the managed target, while an
	// unrelated store alias is added that a working refresh must surface.
	delete(supported, "openai/gpt-4o")
	if err := store.Upsert(ctx, VirtualModel{
		Source:  "later",
		Targets: []Target{{Provider: "openai", Model: "gpt-4o-mini"}},
		Enabled: true,
	}); err != nil {
		t.Fatalf("store.Upsert(later) error = %v", err)
	}

	// The refresh must not fail despite the now-unsupported managed target.
	if err := svc.Refresh(ctx); err != nil {
		t.Fatalf("Refresh() after catalog gap error = %v, want nil (snapshot must not freeze)", err)
	}
	// The snapshot swapped: the new store alias is visible.
	if _, ok := svc.Get("later"); !ok {
		t.Fatalf("snapshot did not swap: alias %q missing after refresh", "later")
	}
	// The managed redirect is simply unavailable while its target is gone.
	if _, changed, _ := svc.ResolveModel(core.NewRequestedModelSelector("smart", "")); changed {
		t.Fatalf("managed redirect resolved despite an unsupported target")
	}

	// When the target returns, the managed redirect resolves again.
	supported["openai/gpt-4o"] = core.Model{ID: "openai/gpt-4o", Object: "model", OwnedBy: "openai"}
	if err := svc.Refresh(ctx); err != nil {
		t.Fatalf("Refresh() after catalog recovery error = %v", err)
	}
	if sel, changed, _ := svc.ResolveModel(core.NewRequestedModelSelector("smart", "")); !changed || sel.QualifiedModel() != "openai/gpt-4o" {
		t.Fatalf("managed redirect did not recover: changed=%v sel=%q", changed, sel.QualifiedModel())
	}
}
