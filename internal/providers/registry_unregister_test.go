package providers

import (
	"context"
	"io"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type blockingRegistryProvider struct {
	models  *core.ModelsResponse
	started chan struct{}
	release chan struct{}
}

func (p *blockingRegistryProvider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	close(p.started)
	select {
	case <-p.release:
		return p.models, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *blockingRegistryProvider) ChatCompletion(context.Context, *core.ChatRequest) (*core.ChatResponse, error) {
	return nil, nil
}

func (p *blockingRegistryProvider) StreamChatCompletion(context.Context, *core.ChatRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (p *blockingRegistryProvider) Responses(context.Context, *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	return nil, nil
}

func (p *blockingRegistryProvider) StreamResponses(context.Context, *core.ResponsesRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (p *blockingRegistryProvider) Embeddings(context.Context, *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, nil
}

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
		err := registry.Initialize(context.Background())
		require.NoError(t, err)
		got := registry.ModelCount()
		require.Equal(t, 2, got)

		registry.UnregisterProvider("drop")
		got = registry.ProviderCount()
		require.Equal(t, 1, got)
		got = registry.ModelCount()
		require.Equal(t, 1, got)
		assert.True(t, registry.Supports("keep/keep-model"))
		assert.False(t, registry.Supports("drop/drop-model"))
		assert.Empty(t, registry.GetProviderType("drop-model"))
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
		err := registry.Initialize(context.Background())
		require.NoError(t, err)

		registry.UnregisterProvider("first")
		got := registry.GetProvider("shared")
		require.Equal(t, second, got)
		assert.False(t, registry.Supports("first/shared"))
		assert.True(t, registry.Supports("second/shared"))
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
		got := registry.ModelCount()
		require.Equal(t, 0, got)
		assert.False(t, registry.Supports("drop/stale-model"))
	})

	t.Run("an in-flight refresh cannot overwrite a same-name replacement", func(t *testing.T) {
		registry := NewModelRegistry()
		oldProvider := &blockingRegistryProvider{
			models: &core.ModelsResponse{
				Object: "list",
				Data:   []core.Model{{ID: "old-model", Object: "model", OwnedBy: "old"}},
			},
			started: make(chan struct{}),
			release: make(chan struct{}),
		}
		newProvider := &registryMockProvider{
			name: "new",
			modelsResponse: &core.ModelsResponse{
				Object: "list",
				Data:   []core.Model{{ID: "new-model", Object: "model", OwnedBy: "new"}},
			},
		}
		registry.RegisterProviderWithNameAndType(oldProvider, "shared", "test")

		refreshDone := make(chan error, 1)
		go func() {
			refreshDone <- registry.Initialize(context.Background())
		}()
		<-oldProvider.started

		registry.UnregisterProvider("shared")
		registry.RegisterProviderWithNameAndType(newProvider, "shared", "test")
		close(oldProvider.release)
		err := <-refreshDone
		require.NoError(t, err)
		assert.False(t, registry.Supports("shared/old-model"))
		got := registry.GetProvider("old-model")
		require.Nil(t, got)
		err = registry.Initialize(context.Background())
		require.NoError(t, err)
		got = registry.GetProvider("shared/new-model")
		require.Equal(t, newProvider, got)
	})

	t.Run("is a no-op for a name that was never registered", func(t *testing.T) {
		registry := NewModelRegistry()
		mock := &registryMockProvider{name: "only"}
		registry.RegisterProviderWithNameAndType(mock, "only", "test")

		registry.UnregisterProvider("never-registered")
		got := registry.ProviderCount()
		require.Equal(t, 1, got)
	})

	t.Run("empty name is a no-op", func(t *testing.T) {
		registry := NewModelRegistry()
		mock := &registryMockProvider{name: "only"}
		registry.RegisterProviderWithNameAndType(mock, "only", "test")

		registry.UnregisterProvider("")
		got := registry.ProviderCount()
		require.Equal(t, 1, got)
	})
}
