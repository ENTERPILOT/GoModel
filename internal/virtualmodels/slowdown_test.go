package virtualmodels

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestResolveSlowdown_AliasOverridesConcreteModel(t *testing.T) {
	service := newSlowdownService(t,
		VirtualModel{
			Source:   "slow-alias",
			Targets:  []Target{{Provider: "openai", Model: "gpt-4o"}},
			Slowdown: 0.4,
			Enabled:  true,
		},
		VirtualModel{
			Source:       "openai/gpt-4o",
			ProviderName: "openai",
			Model:        "gpt-4o",
			Slowdown:     0.2,
			Enabled:      true,
		},
	)

	got := service.ResolveSlowdown(
		context.Background(),
		core.NewRequestedModelSelector("slow-alias", ""),
		core.ModelSelector{Provider: "openai", Model: "gpt-4o"},
	)
	if got != 0.4 {
		t.Fatalf("ResolveSlowdown(alias) = %v, want 0.4", got)
	}
}

func TestResolveSlowdown_AliasFallsBackToConcreteModel(t *testing.T) {
	service := newSlowdownService(t,
		VirtualModel{
			Source:  "plain-alias",
			Targets: []Target{{Provider: "openai", Model: "gpt-4o"}},
			Enabled: true,
		},
		VirtualModel{
			Source:       "openai/gpt-4o",
			ProviderName: "openai",
			Model:        "gpt-4o",
			Slowdown:     0.2,
			Enabled:      true,
		},
	)

	got := service.ResolveSlowdown(
		context.Background(),
		core.NewRequestedModelSelector("plain-alias", ""),
		core.ModelSelector{Provider: "openai", Model: "gpt-4o"},
	)
	if got != 0.2 {
		t.Fatalf("ResolveSlowdown(alias target) = %v, want 0.2", got)
	}
}

func TestResolveSlowdown_HonorsUserPathScope(t *testing.T) {
	service := newSlowdownService(t, VirtualModel{
		Source:       "openai/gpt-4o",
		ProviderName: "openai",
		Model:        "gpt-4o",
		UserPaths:    []string{"/team/alpha"},
		Slowdown:     0.3,
		Enabled:      true,
	})
	requested := core.NewRequestedModelSelector("openai/gpt-4o", "")
	resolved := core.ModelSelector{Provider: "openai", Model: "gpt-4o"}

	matching := core.WithEffectiveUserPath(context.Background(), "/team/alpha/member")
	if got := service.ResolveSlowdown(matching, requested, resolved); got != 0.3 {
		t.Fatalf("ResolveSlowdown(matching path) = %v, want 0.3", got)
	}
	nonMatching := core.WithEffectiveUserPath(context.Background(), "/team/beta")
	if got := service.ResolveSlowdown(nonMatching, requested, resolved); got != 0 {
		t.Fatalf("ResolveSlowdown(non-matching path) = %v, want 0", got)
	}
}

func TestUpsertRejectsSlowdownOutsideRange(t *testing.T) {
	service := newSlowdownService(t)
	for _, slowdown := range []float64{0.09, 10.01} {
		err := service.Upsert(context.Background(), VirtualModel{
			Source:       "openai/gpt-4o",
			ProviderName: "openai",
			Model:        "gpt-4o",
			Slowdown:     slowdown,
			Enabled:      true,
		})
		if err == nil || !IsValidationError(err) {
			t.Fatalf("Upsert(slowdown=%v) error = %v, want validation error", slowdown, err)
		}
	}
}

func TestUpsertRejectsProviderWideSlowdown(t *testing.T) {
	service := newSlowdownService(t)
	err := service.Upsert(context.Background(), VirtualModel{
		Source:   "openai/",
		Slowdown: 0.5,
		Enabled:  true,
	})
	if err == nil || !IsValidationError(err) {
		t.Fatalf("Upsert(provider slowdown) error = %v, want validation error", err)
	}
}

func newSlowdownService(t *testing.T, rows ...VirtualModel) *Service {
	t.Helper()
	store := newSQLVMStore(t)
	ctx := context.Background()
	for _, row := range rows {
		if err := store.Upsert(ctx, row); err != nil {
			t.Fatalf("store.Upsert(%q): %v", row.Source, err)
		}
	}
	service, err := NewService(store, testCatalog(), true)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if err := service.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return service
}
