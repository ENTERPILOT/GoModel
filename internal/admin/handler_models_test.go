package admin

import (
	"context"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
	"github.com/enterpilot/gomodel/internal/modeldata"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
	"github.com/stretchr/testify/require"
)

func newVMModelRegistry(t *testing.T) *providers.ModelRegistry {
	t.Helper()
	registry := providers.NewModelRegistry()
	mock := &handlerMockProvider{
		models: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "gpt-4o", Object: "model", OwnedBy: "openai"},
			},
		},
	}
	registry.RegisterProviderWithNameAndType(mock, "openai", "openai")
	err := registry.Initialize(context.Background())
	require.NoError(t, err)

	return registry
}

func newVMServiceForRegistry(t *testing.T, registry *providers.ModelRegistry, defaultEnabled bool, items ...virtualmodels.VirtualModel) *virtualmodels.Service {
	t.Helper()
	store := newVMTestStore(items...)
	service, err := virtualmodels.NewService(store, registry, defaultEnabled)
	require.NoError(t, err)
	err = service.Refresh(context.Background())
	require.NoError(t, err)

	return service
}

func TestListModels_IncludesModelAccessState(t *testing.T) {
	registry := newVMModelRegistry(t)
	service := newVMServiceForRegistry(t, registry, false, virtualmodels.VirtualModel{
		Source:    "openai/gpt-4o",
		UserPaths: []string{"/team/alpha"},
		Enabled:   true,
	})

	h := NewHandler(nil, registry, WithVirtualModels(service))
	c, rec := echotest.Get(t, "/admin/models")
	require.NoError(t, h.ListModels(c))
	require.Equal(t, http.StatusOK, rec.Code)

	body := echotest.Decode[[]modelInventoryResponse](t, rec)
	require.Len(t, body, 1)

	row := body[0]
	require.Equal(t, "openai/gpt-4o", row.Access.Selector)
	require.False(t, row.Access.DefaultEnabled)
	require.True(t, row.Access.EffectiveEnabled)
	require.Equal(t, []string{"/team/alpha"}, row.Access.UserPaths)
	require.NotNil(t, row.Access.Override)
	require.Equal(t, "openai/gpt-4o", row.Access.Override.Source)
}

func TestListModels_DisabledPolicyTurnsModelOff(t *testing.T) {
	registry := newVMModelRegistry(t)
	service := newVMServiceForRegistry(t, registry, true, virtualmodels.VirtualModel{
		Source:  "openai/gpt-4o",
		Enabled: false,
	})

	h := NewHandler(nil, registry, WithVirtualModels(service))
	c, rec := echotest.Get(t, "/admin/models")
	require.NoError(t, h.ListModels(c))

	body := echotest.Decode[[]modelInventoryResponse](t, rec)
	require.Len(t, body, 1)

	row := body[0]
	require.True(t, row.Access.DefaultEnabled)
	require.False(t, row.Access.EffectiveEnabled)
}

func TestListModels_AppliesProviderWideOverrideToConcreteModels(t *testing.T) {
	registry := newVMModelRegistry(t)
	service := newVMServiceForRegistry(t, registry, true, virtualmodels.VirtualModel{
		Source:    "openai/",
		UserPaths: []string{"/team/provider"},
		Enabled:   true,
	})

	h := NewHandler(nil, registry, WithVirtualModels(service))
	c, rec := echotest.Get(t, "/admin/models")
	require.NoError(t, h.ListModels(c))

	body := echotest.Decode[[]modelInventoryResponse](t, rec)
	require.Len(t, body, 1)

	row := body[0]
	require.Equal(t, "openai/gpt-4o", row.Access.Selector)
	require.Equal(t, []string{"/team/provider"}, row.Access.UserPaths)
	require.Nil(t, row.Access.Override)
}

func TestListModels_AppliesGlobalOverrideToConcreteModels(t *testing.T) {
	registry := newVMModelRegistry(t)
	service := newVMServiceForRegistry(t, registry, true, virtualmodels.VirtualModel{
		Source:    "/",
		UserPaths: []string{"/team/global"},
		Enabled:   true,
	})

	h := NewHandler(nil, registry, WithVirtualModels(service))
	c, rec := echotest.Get(t, "/admin/models")
	require.NoError(t, h.ListModels(c))

	body := echotest.Decode[[]modelInventoryResponse](t, rec)
	require.Len(t, body, 1)

	row := body[0]
	require.Equal(t, []string{"/team/global"}, row.Access.UserPaths)
	require.Nil(t, row.Access.Override)
}

func newLayeredModelRegistry(t *testing.T) *providers.ModelRegistry {
	t.Helper()
	registry := providers.NewModelRegistry()
	runtimeContext := 4096
	mock := &handlerMockProvider{
		models: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{
					ID:       "gemma-3-4b-it",
					Object:   "model",
					OwnedBy:  "llamacpp",
					Metadata: &core.ModelMetadata{ContextWindow: &runtimeContext},
				},
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
			"gemma-3-4b-it": {"display_name": "Gemma 3 4B IT", "modes": ["chat"], "context_window": 131072}
		}
	}`)
	list, err := modeldata.Parse(raw)
	require.NoError(t, err)
	registry.SetModelList(list, raw)
	registry.EnrichModels()
	return registry
}

func TestModelMetadataLayers_ReturnsLayersAndSources(t *testing.T) {
	h := NewHandler(nil, newLayeredModelRegistry(t))
	c, rec := echotest.Get(t, "/admin/models/metadata?provider=local&model=gemma-3-4b-it")
	require.NoError(t, h.ModelMetadataLayers(c))
	require.Equal(t, http.StatusOK, rec.Code)

	body := echotest.Decode[providers.ModelMetadataLayers](t, rec)
	require.Equal(t, "local/gemma-3-4b-it", body.Selector)
	require.NotNil(t, body.Effective)
	require.NotNil(t, body.Provider)
	require.NotNil(t, body.Catalog)
	require.NotNil(t, body.Config)
	require.Equal(t, "Gemma (local)", body.Effective.DisplayName)
	require.Equal(t, "Gemma 3 4B IT", body.Catalog.DisplayName)
	require.NotNil(t, body.Provider.ContextWindow)
	require.Equal(t, 4096, *body.Provider.ContextWindow)
	require.Equal(t, modeldata.MetadataSourceConfig, body.Sources["display_name"])
	require.Equal(t, modeldata.MetadataSourceProvider, body.Sources["context_window"])
	require.Equal(t, modeldata.MetadataSourceCatalog, body.Sources["modes"])
}

func TestModelMetadataLayers_UnknownLayersAreNull(t *testing.T) {
	h := NewHandler(nil, newVMModelRegistry(t))
	c, rec := echotest.Get(t, "/admin/models/metadata?provider=openai&model=gpt-4o")
	require.NoError(t, h.ModelMetadataLayers(c))
	require.Equal(t, http.StatusOK, rec.Code)

	body := echotest.Decode[map[string]any](t, rec)
	require.Equal(t, "openai/gpt-4o", body["selector"])
	require.Nil(t, body["provider"])
	require.Nil(t, body["catalog"])
	require.Nil(t, body["config"])
}

func TestModelMetadataLayers_Errors(t *testing.T) {
	tests := []struct {
		name     string
		registry *providers.ModelRegistry
		target   string
		status   int
	}{
		{name: "missing provider", registry: newVMModelRegistry(t), target: "/admin/models/metadata?model=gpt-4o", status: http.StatusBadRequest},
		{name: "missing model", registry: newVMModelRegistry(t), target: "/admin/models/metadata?provider=openai", status: http.StatusBadRequest},
		{name: "unknown model", registry: newVMModelRegistry(t), target: "/admin/models/metadata?provider=openai&model=nope", status: http.StatusNotFound},
		{name: "no registry", registry: nil, target: "/admin/models/metadata?provider=openai&model=gpt-4o", status: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(nil, tt.registry)
			c, rec := echotest.Get(t, tt.target)
			require.NoError(t, h.ModelMetadataLayers(c))
			require.Equal(t, tt.status, rec.Code)
		})
	}
}
