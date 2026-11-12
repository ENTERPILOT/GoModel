package providers

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
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

func TestAllowlistKeepsLastMetadataWhenListingFails(t *testing.T) {
	contextWindow := 32768
	mock := &registryMockProvider{name: "vllm", modelsResponse: &core.ModelsResponse{
		Object: "list",
		Data:   []core.Model{{ID: "listed", Object: "model", Metadata: &core.ModelMetadata{ContextWindow: &contextWindow}}},
	}}
	registry := allowlistRegistry(t, mock)
	require.NoError(t, registry.Initialize(context.Background()))

	mock.modelsResponse = nil
	mock.err = errors.New("503 upstream unavailable")
	require.NoError(t, registry.Initialize(context.Background()))

	listed := registry.GetModel("vllm/listed")
	require.NotNil(t, listed)
	require.NotNil(t, listed.Model.Metadata)
	require.NotNil(t, listed.Model.Metadata.ContextWindow, "a failed metadata call keeps what the last one reported")
	assert.Equal(t, contextWindow, *listed.Model.Metadata.ContextWindow)
	assert.Equal(t, 2, registry.ModelCount())
}

func TestConfiguredModelsResponseFirstUpstreamDuplicateWins(t *testing.T) {
	upstream := &core.ModelsResponse{Object: "list", Data: []core.Model{
		{ID: "dup", OwnedBy: "first"},
		{ID: " dup ", OwnedBy: "second"},
	}}

	allowlist := configuredProviderModelsResponse("p", "openai", []string{"dup"}, upstream, 1)
	merged := mergeConfiguredProviderModelsResponse("p", "openai", []string{"dup"}, upstream, 1)

	require.Len(t, allowlist.Data, 1)
	require.Len(t, merged.Data, 1)
	assert.Equal(t, "first", allowlist.Data[0].OwnedBy)
	assert.Equal(t, "first", merged.Data[0].OwnedBy)
}

type panickingListProvider struct{ *registryMockProvider }

func (p *panickingListProvider) ListModels(context.Context) (*core.ModelsResponse, error) {
	panic("malformed model list")
}

func TestPanickingListingKeepsConfiguredModels(t *testing.T) {
	for _, mode := range []config.ConfiguredProviderModelsMode{
		config.ConfiguredProviderModelsModeAllowlist,
		config.ConfiguredProviderModelsModeFallback,
		config.ConfiguredProviderModelsModeMerge,
	} {
		t.Run(string(mode), func(t *testing.T) {
			registry := allowlistRegistry(t, &panickingListProvider{&registryMockProvider{name: "vllm"}})
			registry.SetProviderModelsMode("vllm", mode)

			require.NoError(t, registry.Initialize(context.Background()))

			assert.True(t, registry.Supports("vllm/listed"))
			assert.True(t, registry.Supports("vllm/configured-only"))
			snapshots := registry.ProviderRuntimeSnapshots()
			require.Len(t, snapshots, 1)
			if mode == config.ConfiguredProviderModelsModeAllowlist {
				assert.Empty(t, snapshots[0].LastModelFetchError, "in allowlist mode the listing only adds metadata")
			} else {
				assert.Equal(t, "list models panicked", snapshots[0].LastModelFetchError)
			}
		})
	}
}
