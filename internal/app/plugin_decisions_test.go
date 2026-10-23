package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
	"github.com/enterpilot/gomodel/internal/virtualmodels"
)

// virtualModelRows is a read-only virtual model store.
type virtualModelRows []virtualmodels.VirtualModel

func (r virtualModelRows) List(context.Context) ([]virtualmodels.VirtualModel, error) {
	return r, nil
}

func (r virtualModelRows) Get(_ context.Context, source string) (*virtualmodels.VirtualModel, error) {
	for _, vm := range r {
		if vm.Source == source {
			return &vm, nil
		}
	}
	return nil, virtualmodels.ErrNotFound
}

func (virtualModelRows) Upsert(context.Context, virtualmodels.VirtualModel) error { return nil }
func (virtualModelRows) Delete(context.Context, string) error                    { return nil }
func (virtualModelRows) Close() error                                            { return nil }

func catalogProvider(models ...core.Model) *runtimeRefreshMockProvider {
	return &runtimeRefreshMockProvider{models: &core.ModelsResponse{Object: "list", Data: models}}
}

// newDecisionModelsRouter lists a Kev server (a jev provider named kev),
// OpenRouter with a decision model and a chat model, and OpenAI, as their
// providers catalog them.
func newDecisionModelsRouter(t *testing.T) (*providers.Router, *providers.ModelRegistry) {
	t.Helper()
	utility := &core.ModelMetadata{Categories: []core.ModelCategory{core.CategoryUtility}}
	chat := &core.ModelMetadata{Modes: []string{"chat"}}
	registry := providers.NewModelRegistry()
	registry.RegisterProviderWithNameAndType(catalogProvider(core.Model{ID: "kev-latest", Object: "model"}), "kev", "jev")
	registry.RegisterProviderWithNameAndType(catalogProvider(
		core.Model{ID: "typesafe/jev-1.13", Object: "model", Metadata: utility},
		core.Model{ID: "openai/gpt-x", Object: "model", Metadata: chat},
	), "openrouter", "openrouter")
	registry.RegisterProviderWithNameAndType(catalogProvider(core.Model{ID: "gpt-6-luna", Object: "model", Metadata: chat}), "openai", "openai")
	require.NoError(t, registry.Initialize(context.Background()))
	router, err := providers.NewRouter(registry)
	require.NoError(t, err)
	return router, registry
}

func TestPluginDecider_DecisionModel(t *testing.T) {
	router, registry := newDecisionModelsRouter(t)
	vms, err := virtualmodels.NewService(virtualModelRows{
		{Source: "judge", Enabled: true, Targets: []virtualmodels.Target{{Provider: "kev", Model: "kev-latest"}, {Provider: "openrouter", Model: "typesafe/jev-1.13"}}},
		{Source: "mixed", Enabled: true, Targets: []virtualmodels.Target{{Provider: "kev", Model: "kev-latest"}, {Provider: "openai", Model: "gpt-6-luna"}}},
		{Source: "off", Enabled: false, Targets: []virtualmodels.Target{{Provider: "kev", Model: "kev-latest"}}},
	}, registry, true)
	require.NoError(t, err)
	require.NoError(t, vms.Refresh(context.Background()))
	decider := pluginDecider{router: router, virtualModels: vms}

	for model, want := range map[string]bool{
		"kev/kev-latest":               true,
		"kev-latest":                   true,
		"jev/kev-latest":               true,
		"kev/kev-1.0.0":                true,
		"openrouter/typesafe/jev-1.13": true,
		"openrouter/openai/gpt-x":      false,
		"openai/gpt-6-luna":            false,
		"nowhere/unknown":              false,
		"":                             false,
		"judge":                        true,
		"mixed":                        false,
		"off":                          false,
	} {
		assert.Equal(t, want, decider.DecisionModel(model), model)
	}

	assert.True(t, pluginDecider{router: router}.DecisionModel("kev/kev-latest"), "virtual models are optional")
	assert.False(t, pluginDecider{}.DecisionModel("kev/kev-latest"))
}
