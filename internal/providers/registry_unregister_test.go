package providers

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestModelRegistry_UnregisterProvider(t *testing.T) {
	t.Run("removes a registered provider and its models immediately", func(t *testing.T) {
		registry := NewModelRegistry()
		keep := &registryMockProvider{
			name: "keep",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data:   []core.Model{{ID: "keep-model", Object: "model", OwnedBy: "keep"}},
			},
		}
		drop := &registryMockProvider{
			name: "drop",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data:   []core.Model{{ID: "drop-model", Object: "model", OwnedBy: "drop"}},
			},
		}
		registry.RegisterProviderWithNameAndType(keep, "keep", "test")
		registry.RegisterProviderWithNameAndType(drop, "drop", "test")

		if err := registry.Initialize(context.Background()); err != nil {
			t.Fatalf("Initialize() error = %v", err)
		}
		if got := registry.ModelCount(); got != 2 {
			t.Fatalf("ModelCount() before unregister = %d, want 2", got)
		}

		registry.UnregisterProvider("drop")
		if got := registry.ProviderCount(); got != 1 {
			t.Fatalf("ProviderCount() after UnregisterProvider = %d, want 1", got)
		}
		if got := registry.ModelCount(); got != 1 {
			t.Fatalf("ModelCount() after UnregisterProvider = %d, want 1", got)
		}
		if !registry.Supports("keep/keep-model") {
			t.Error("Supports(keep/keep-model) = false, want true")
		}
		if registry.Supports("drop/drop-model") {
			t.Error("Supports(drop/drop-model) = true, want false (unregistered provider)")
		}
		if got := registry.GetProviderType("drop-model"); got != "" {
			t.Errorf("GetProviderType(drop-model) = %q, want empty", got)
		}
	})

	t.Run("promotes another provider for an overlapping bare model", func(t *testing.T) {
		registry := NewModelRegistry()
		first := &registryMockProvider{
			name: "first",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data:   []core.Model{{ID: "shared", Object: "model", OwnedBy: "first"}},
			},
		}
		second := &registryMockProvider{
			name: "second",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data:   []core.Model{{ID: "shared", Object: "model", OwnedBy: "second"}},
			},
		}
		registry.RegisterProviderWithNameAndType(first, "first", "test")
		registry.RegisterProviderWithNameAndType(second, "second", "test")
		if err := registry.Initialize(context.Background()); err != nil {
			t.Fatalf("Initialize() error = %v", err)
		}

		registry.UnregisterProvider("first")

		if got := registry.GetProvider("shared"); got != second {
			t.Fatalf("GetProvider(shared) = %T %p, want second provider %p", got, got, second)
		}
		if registry.Supports("first/shared") {
			t.Error("Supports(first/shared) = true, want false")
		}
		if !registry.Supports("second/shared") {
			t.Error("Supports(second/shared) = false, want true")
		}
	})

	t.Run("an in-flight refresh cannot restore a removed provider", func(t *testing.T) {
		registry := NewModelRegistry()
		drop := &registryMockProvider{name: "drop"}
		registry.RegisterProviderWithNameAndType(drop, "drop", "test")
		registry.UnregisterProvider("drop")

		staleModel := &ModelInfo{
			Model:        core.Model{ID: "stale-model", Object: "model", OwnedBy: "drop"},
			Provider:     drop,
			ProviderName: "drop",
			ProviderType: "test",
		}
		registry.applyFetchedInventory(map[core.Provider]string{drop: "test"}, fetchedInventory{
			models:           map[string]*ModelInfo{"stale-model": staleModel},
			modelsByProvider: map[string]map[string]*ModelInfo{"drop": {"stale-model": staleModel}},
			runtimeUpdates:   map[string]providerRuntimeState{"drop": {registered: true}},
			totalModels:      1,
		}, 1)

		if got := registry.ModelCount(); got != 0 {
			t.Fatalf("ModelCount() after stale refresh result = %d, want 0", got)
		}
		if registry.Supports("drop/stale-model") {
			t.Error("Supports(drop/stale-model) = true, want false")
		}
	})

	t.Run("is a no-op for a name that was never registered", func(t *testing.T) {
		registry := NewModelRegistry()
		mock := &registryMockProvider{name: "only"}
		registry.RegisterProviderWithNameAndType(mock, "only", "test")

		registry.UnregisterProvider("never-registered")

		if got := registry.ProviderCount(); got != 1 {
			t.Fatalf("ProviderCount() = %d, want 1 (unaffected)", got)
		}
	})

	t.Run("empty name is a no-op", func(t *testing.T) {
		registry := NewModelRegistry()
		mock := &registryMockProvider{name: "only"}
		registry.RegisterProviderWithNameAndType(mock, "only", "test")

		registry.UnregisterProvider("")

		if got := registry.ProviderCount(); got != 1 {
			t.Fatalf("ProviderCount() = %d, want 1 (unaffected)", got)
		}
	})
}
