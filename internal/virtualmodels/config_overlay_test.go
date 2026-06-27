package virtualmodels

import (
	"context"
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
			name:    "unknown target",
			entries: []config.VirtualModelConfig{{Source: "smart", Target: "openai/unknown"}},
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
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc := newBalancingService(t)
			svc.SetConfigModels(ConfigModels(tt.entries))
			err := svc.Refresh(context.Background())
			if err == nil {
				t.Fatalf("Refresh() error = nil, want validation failure")
			}
			if !IsValidationError(err) {
				t.Fatalf("Refresh() error = %v, want validation error", err)
			}
		})
	}
}
