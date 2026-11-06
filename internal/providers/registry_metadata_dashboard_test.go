package providers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/modeldata"
)

func newDashboardOverrideRegistry(t *testing.T, withCatalog bool) *ModelRegistry {
	t.Helper()
	registry := NewModelRegistry()
	provider := &registryMockProvider{
		name: "provider-pollinations",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "flux", Object: "model", OwnedBy: "pollinations"},
				{ID: "openai", Object: "model", OwnedBy: "pollinations", Metadata: &core.ModelMetadata{
					Modes:      []string{"chat"},
					Categories: []core.ModelCategory{core.CategoryTextGeneration},
				}},
			},
		},
	}
	registry.RegisterProviderWithNameAndType(provider, "pollinations", "openai")
	if withCatalog {
		raw := []byte(`{
			"version": 1,
			"updated_at": "2025-01-01T00:00:00Z",
			"providers": {"openai": {"display_name": "OpenAI", "api_type": "openai", "supported_modes": ["chat"]}},
			"models": {"openai": {"display_name": "Catalog OpenAI", "modes": ["chat"], "capabilities": {"vision": false}}},
			"provider_models": {"openai/openai": {"model_ref": "openai", "enabled": true, "context_window": 128000}}
		}`)
		list, err := modeldata.Parse(raw)
		require.NoError(t, err)
		registry.SetModelList(list, raw)
	}
	require.NoError(t, registry.Initialize(context.Background()))
	return registry
}

func TestSetDashboardMetadataOverrides_AppliesAndReverts(t *testing.T) {
	for _, withCatalog := range []bool{true, false} {
		name := "without catalog"
		if withCatalog {
			name = "with catalog"
		}
		t.Run(name, func(t *testing.T) {
			registry := newDashboardOverrideRegistry(t, withCatalog)

			registry.SetDashboardMetadataOverrides(map[string]map[string]*core.ModelMetadata{
				"pollinations": {
					"flux":   {Categories: []core.ModelCategory{core.CategoryImage}},
					"openai": {ContextWindow: new(32000), Capabilities: map[string]bool{"vision": true}},
				},
			})

			image := registry.ListModelsWithProviderByCategory(core.CategoryImage)
			require.Len(t, image, 1)
			assert.Equal(t, "flux", image[0].Model.ID)

			info := registry.GetModel("pollinations/openai")
			require.NotNil(t, info)
			require.NotNil(t, info.Model.Metadata)
			require.NotNil(t, info.Model.Metadata.ContextWindow)
			assert.Equal(t, 32000, *info.Model.Metadata.ContextWindow)
			assert.True(t, info.Model.Metadata.Capabilities["vision"])
			assert.Equal(t, []core.ModelCategory{core.CategoryTextGeneration}, info.Model.Metadata.Categories)

			registry.SetDashboardMetadataOverrides(nil)

			assert.Empty(t, registry.ListModelsWithProviderByCategory(core.CategoryImage))
			info = registry.GetModel("pollinations/openai")
			require.NotNil(t, info)
			require.NotNil(t, info.Model.Metadata)
			assert.False(t, info.Model.Metadata.Capabilities["vision"])
			if withCatalog {
				require.NotNil(t, info.Model.Metadata.ContextWindow)
				assert.Equal(t, 128000, *info.Model.Metadata.ContextWindow)
			} else {
				assert.Nil(t, info.Model.Metadata.ContextWindow)
			}
		})
	}
}

func TestSetDashboardMetadataOverrides_WinsOverConfigFieldByField(t *testing.T) {
	registry := NewModelRegistry()
	provider := &registryMockProvider{
		name: "provider-local",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data:   []core.Model{{ID: "llava", Object: "model", OwnedBy: "ollama"}},
		},
	}
	registry.RegisterProviderWithNameAndType(provider, "local", "ollama")
	registry.SetProviderMetadataOverrides("local", map[string]*core.ModelMetadata{
		"llava": {ContextWindow: new(4096), MaxOutputTokens: new(1024), Capabilities: map[string]bool{"vision": false}},
	})
	registry.SetDashboardMetadataOverrides(map[string]map[string]*core.ModelMetadata{
		"local": {"llava": {ContextWindow: new(8192), Capabilities: map[string]bool{"vision": true}}},
	})
	require.NoError(t, registry.Initialize(context.Background()))

	meta := registry.GetModel("local/llava").Model.Metadata
	require.NotNil(t, meta)
	assert.Equal(t, 8192, *meta.ContextWindow)
	assert.Equal(t, 1024, *meta.MaxOutputTokens)
	assert.True(t, meta.Capabilities["vision"])

	layers, ok := registry.ModelMetadataLayers("local", "llava")
	require.True(t, ok)
	require.NotNil(t, layers.Dashboard)
	assert.Equal(t, modeldata.MetadataSourceDashboard, layers.Sources["context_window"])
	assert.Equal(t, modeldata.MetadataSourceConfig, layers.Sources["max_output_tokens"])
	assert.Equal(t, modeldata.MetadataSourceDashboard, layers.Sources["capabilities.vision"])
}

func TestSetDashboardMetadataOverrides_DeepClonesInput(t *testing.T) {
	registry := newDashboardOverrideRegistry(t, false)
	input := map[string]map[string]*core.ModelMetadata{
		"pollinations": {"flux": {ContextWindow: new(1000)}},
	}
	registry.SetDashboardMetadataOverrides(input)

	*input["pollinations"]["flux"].ContextWindow = 1

	meta := registry.GetModel("pollinations/flux").Model.Metadata
	require.NotNil(t, meta)
	assert.Equal(t, 1000, *meta.ContextWindow)
}
