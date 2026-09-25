package providers

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/modeldata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newLayeredMetadataRegistry(t *testing.T) *ModelRegistry {
	t.Helper()
	registry := NewModelRegistry()

	runtimeContext := 4096
	mock := &registryMockProvider{
		name: "llamacpp",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{
					ID:      "gemma-3-4b-it",
					Object:  "model",
					OwnedBy: "llamacpp",
					Metadata: &core.ModelMetadata{
						ContextWindow: &runtimeContext,
						Capabilities:  map[string]bool{"vision": true},
					},
				},
				{ID: "nomic-embed-text", Object: "model", OwnedBy: "llamacpp"},
			},
		},
	}
	registry.RegisterProviderWithNameAndType(mock, "local", "llamacpp")
	registry.SetProviderMetadataOverrides("local", map[string]*core.ModelMetadata{
		"gemma-3-4b-it": {DisplayName: "Gemma (local)"},
	})
	require.NoError(t, registry.Initialize(context.Background()))

	raw := []byte(`{
		"version": 1,
		"updated_at": "2025-01-01T00:00:00Z",
		"models": {
			"gemma-3-4b-it": {
				"display_name": "Gemma 3 4B IT",
				"modes": ["chat"],
				"context_window": 131072
			}
		}
	}`)
	list, err := modeldata.Parse(raw)
	require.NoError(t, err)
	registry.SetModelList(list, raw)
	registry.EnrichModels()
	return registry
}

func TestModelMetadataLayers_ReturnsEveryLayerAndItsSources(t *testing.T) {
	registry := newLayeredMetadataRegistry(t)

	layers, ok := registry.ModelMetadataLayers("local", "gemma-3-4b-it")
	require.True(t, ok)
	assert.Equal(t, "local/gemma-3-4b-it", layers.Selector)

	require.NotNil(t, layers.Effective)
	assert.Equal(t, "Gemma (local)", layers.Effective.DisplayName)
	require.NotNil(t, layers.Effective.ContextWindow)
	assert.Equal(t, 4096, *layers.Effective.ContextWindow)

	require.NotNil(t, layers.Provider)
	assert.Empty(t, layers.Provider.DisplayName)
	require.NotNil(t, layers.Provider.ContextWindow)
	assert.Equal(t, 4096, *layers.Provider.ContextWindow)

	require.NotNil(t, layers.Catalog)
	assert.Equal(t, "Gemma 3 4B IT", layers.Catalog.DisplayName)
	require.NotNil(t, layers.Catalog.ContextWindow)
	assert.Equal(t, 131072, *layers.Catalog.ContextWindow)

	require.NotNil(t, layers.Config)
	assert.Equal(t, "Gemma (local)", layers.Config.DisplayName)

	assert.Equal(t, map[string]string{
		"display_name":        modeldata.MetadataSourceConfig,
		"context_window":      modeldata.MetadataSourceProvider,
		"modes":               modeldata.MetadataSourceCatalog,
		"categories":          modeldata.MetadataSourceCatalog,
		"capabilities.vision": modeldata.MetadataSourceProvider,
	}, layers.Sources)
}

func TestModelMetadataLayers_ResolvesProviderTypeSegment(t *testing.T) {
	registry := newLayeredMetadataRegistry(t)

	layers, ok := registry.ModelMetadataLayers("llamacpp", "gemma-3-4b-it")
	require.True(t, ok)
	assert.Equal(t, "local/gemma-3-4b-it", layers.Selector)
}

func TestModelMetadataLayers_UnknownLayersAreNil(t *testing.T) {
	registry := newLayeredMetadataRegistry(t)

	layers, ok := registry.ModelMetadataLayers("local", "nomic-embed-text")
	require.True(t, ok)
	assert.Nil(t, layers.Provider)
	assert.Nil(t, layers.Catalog)
	assert.Nil(t, layers.Config)
	// The catalog does not know it, so its modes were inferred from the ID.
	require.NotNil(t, layers.Effective)
	assert.Equal(t, []string{"embedding"}, layers.Effective.Modes)
	assert.Equal(t, modeldata.MetadataSourceInferred, layers.Sources["modes"])
}

func TestModelMetadataLayers_ReturnsCopies(t *testing.T) {
	registry := newLayeredMetadataRegistry(t)

	layers, ok := registry.ModelMetadataLayers("local", "gemma-3-4b-it")
	require.True(t, ok)
	layers.Effective.DisplayName = "mutated"
	layers.Provider.Capabilities["vision"] = false

	again, ok := registry.ModelMetadataLayers("local", "gemma-3-4b-it")
	require.True(t, ok)
	assert.Equal(t, "Gemma (local)", again.Effective.DisplayName)
	assert.True(t, again.Provider.Capabilities["vision"])
}

func TestModelMetadataLayers_NotFound(t *testing.T) {
	registry := newLayeredMetadataRegistry(t)

	_, ok := registry.ModelMetadataLayers("local", "missing")
	assert.False(t, ok)
	_, ok = registry.ModelMetadataLayers("", "gemma-3-4b-it")
	assert.False(t, ok)
	_, ok = NewModelRegistry().ModelMetadataLayers("local", "gemma-3-4b-it")
	assert.False(t, ok)
}
