package providers

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

func allowlistRegistry(t *testing.T, provider core.Provider) *ModelRegistry {
	t.Helper()
	registry := NewModelRegistry()
	registry.RegisterProviderWithNameAndType(provider, "vllm", "vllm")
	registry.SetProviderConfiguredModels("vllm", []string{"listed", "configured-only"})
	return registry
}

func TestAllowlistTakesMetadataFromUpstream(t *testing.T) {
	contextWindow := 32768
	registry := allowlistRegistry(t, &registryMockProvider{name: "vllm", modelsResponse: &core.ModelsResponse{
		Object: "list",
		Data: []core.Model{
			{ID: "listed", Object: "model", Metadata: &core.ModelMetadata{ContextWindow: &contextWindow}},
			{ID: "upstream-only", Object: "model"},
		},
	}})

	require.NoError(t, registry.Initialize(context.Background()))

	listed := registry.GetModel("vllm/listed")
	require.NotNil(t, listed)
	require.NotNil(t, listed.Model.Metadata)
	require.NotNil(t, listed.Model.Metadata.ContextWindow)
	assert.Equal(t, contextWindow, *listed.Model.Metadata.ContextWindow)
	assert.True(t, registry.Supports("vllm/configured-only"), "a configured model upstream does not list is still served")
	assert.False(t, registry.Supports("vllm/upstream-only"), "upstream never widens an allowlist")
}

func TestAllowlistServesConfiguredModelsWhenListingFails(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "upstream error", err: errors.New("503 upstream unavailable")},
		{name: "no /models endpoint", err: core.MarkModelListingUnsupported(core.ParseProviderError("vllm", http.StatusNotFound, []byte("404"), nil))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := allowlistRegistry(t, &registryMockProvider{name: "vllm", err: tt.err})

			require.NoError(t, registry.Initialize(context.Background()))

			assert.Equal(t, 2, registry.ModelCount())
			assert.True(t, registry.Supports("vllm/listed"))
			snapshots := registry.ProviderRuntimeSnapshots()
			require.Len(t, snapshots, 1)
			assert.Empty(t, snapshots[0].LastModelFetchError, "a metadata listing failure is not a provider failure")
			assert.NotNil(t, snapshots[0].LastModelFetchSuccessAt)
			assert.False(t, snapshots[0].InventoryStale)
		})
	}
}

func TestAllowlistMetadataFetchCanBeTurnedOff(t *testing.T) {
	var listCount atomic.Int32
	mock := &countingRegistryMockProvider{
		listCount:            &listCount,
		registryMockProvider: &registryMockProvider{name: "vllm", modelsResponse: modelsList("listed")},
	}
	registry := allowlistRegistry(t, mock)
	registry.SetAllowlistUpstreamMetadata(false)

	require.NoError(t, registry.Initialize(context.Background()))

	assert.Zero(t, listCount.Load(), "a disabled metadata fetch makes no listing call")
	assert.Equal(t, 2, registry.ModelCount())
}
