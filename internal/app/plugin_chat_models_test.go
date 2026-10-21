package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/providers"
)

// aliasResolver maps virtual model names to concrete selectors.
type aliasResolver map[string]core.ModelSelector

func (r aliasResolver) ResolveModel(requested core.RequestedModelSelector) (core.ModelSelector, bool, error) {
	if selector, ok := r[requested.RequestedQualifiedModel()]; ok {
		return selector, true, nil
	}
	selector, err := requested.Normalize()
	return selector, false, err
}

// The checker reads the catalog entry of the model a plugin names, after
// resolving virtual models: a decision model, directly or behind a virtual
// model, cannot answer plugin inference, while chat and uncatalogued models
// pass.
func TestPluginChatModels_RefusesModelsThatCannotChat(t *testing.T) {
	registry := providers.NewModelRegistry()
	registry.RegisterProviderWithNameAndType(&runtimeRefreshMockProvider{models: &core.ModelsResponse{Object: "list", Data: []core.Model{
		{ID: "jev-latest", Object: "model", Metadata: &core.ModelMetadata{Categories: []core.ModelCategory{core.CategoryUtility}}},
	}}}, "jev", "jev")
	registry.RegisterProviderWithNameAndType(&runtimeRefreshMockProvider{models: &core.ModelsResponse{Object: "list", Data: []core.Model{
		{ID: "gpt-6-luna", Object: "model", Metadata: &core.ModelMetadata{Modes: []string{"chat", "responses"}}},
	}}}, "openai", "openai")
	require.NoError(t, registry.Initialize(context.Background()))
	router, err := providers.NewRouter(registry)
	require.NoError(t, err)

	checker := pluginChatModels{router: router, resolver: aliasResolver{"decider": {Provider: "jev", Model: "jev-latest"}}}
	assert.Contains(t, checker.ChatUnsupportedReason(context.Background(), "jev/jev-latest"), "is a decision model")
	assert.Contains(t, checker.ChatUnsupportedReason(context.Background(), "decider"), "is a decision model", "a virtual model is resolved first")
	assert.Empty(t, checker.ChatUnsupportedReason(context.Background(), "openai/gpt-6-luna"))
	assert.Empty(t, checker.ChatUnsupportedReason(context.Background(), "nowhere/unknown"), "an unknown model is accepted")
}
