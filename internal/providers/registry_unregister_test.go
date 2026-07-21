package providers

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
)

func TestModelRegistry_UnregisterProvider(t *testing.T) {
	t.Run("removes a registered provider and its models after Refresh", func(t *testing.T) {
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

		if err := registry.Refresh(context.Background()); err != nil {
			t.Fatalf("Refresh() error = %v", err)
		}
		if got := registry.ModelCount(); got != 1 {
			t.Fatalf("ModelCount() after Refresh = %d, want 1", got)
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
