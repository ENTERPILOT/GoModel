package admin

import (
	"context"
	"net/http"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/echotest"
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
