package providers

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/cache/modelcache"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/modeldata"
)

// baseURLMockProvider is a registry mock that reports its API base URL, as the
// OpenAI-compatible providers do.
type baseURLMockProvider struct {
	registryMockProvider
	baseURL string
}

func (p *baseURLMockProvider) GetBaseURL() string { return p.baseURL }

const fallbackCatalogJSON = `{"version":1,"updated_at":"2026-10-01T00:00:00Z",
	"providers":{"openai":{"display_name":"OpenAI","api_type":"openai","default_base_url":"https://api.openai.com/v1"}},
	"models":{"gpt-6-luna":{"display_name":"GPT 6 Luna"},"gpt-old":{"display_name":"Old"}},
	"provider_models":{
		"openai/gpt-6-luna":{"model_ref":"gpt-6-luna","enabled":true},
		"openai/gpt-old":{"model_ref":"gpt-old","enabled":false},
		"anthropic/claude-x":{"model_ref":"claude-x","enabled":true}}}`

var errListingDown = errors.New("models endpoint unavailable")

func newCatalogRegistry(t *testing.T, provider core.Provider) *ModelRegistry {
	t.Helper()
	registry := NewModelRegistry()
	list, err := modeldata.Parse([]byte(fallbackCatalogJSON))
	require.NoError(t, err)
	registry.SetModelList(list, []byte(fallbackCatalogJSON))
	registry.RegisterProviderWithNameAndType(provider, "openai", "openai")
	return registry
}

func modelIDs(registry *ModelRegistry) []string {
	var ids []string
	for _, model := range registry.ListModels() {
		ids = append(ids, model.ID)
	}
	return ids
}

// When a provider's model listing fails and nothing else describes its
// inventory, the catalog's enabled models for its type keep it routable, and
// the provider stays marked failed so the recheck loop keeps probing it.
func TestInitialize_CatalogFallbackWhenListingFails(t *testing.T) {
	provider := &baseURLMockProvider{err: errListingDown, baseURL: "https://api.openai.com/v1/"}
	registry := newCatalogRegistry(t, provider)

	require.NoError(t, registry.Initialize(context.Background()))

	assert.Equal(t, []string{"gpt-6-luna"}, modelIDs(registry))
	assert.NotNil(t, registry.GetModel("openai/gpt-6-luna"))
	assert.Contains(t, registry.FailedProviderNames(), "openai")
}

func TestInitialize_NoCatalogFallbackWhenOtherSourcesApply(t *testing.T) {
	tests := []struct {
		name       string
		baseURL    string
		configured []string
		listing    *core.ModelsResponse
		wantModels []string
		wantErr    bool
	}{
		{name: "custom base URL", baseURL: "https://proxy.example.com/v1", wantErr: true},
		{name: "configured models take precedence", baseURL: "https://api.openai.com/v1",
			configured: []string{"gpt-configured"}, wantModels: []string{"gpt-configured"}},
		{name: "successful listing", baseURL: "https://api.openai.com/v1",
			listing:    &core.ModelsResponse{Object: "list", Data: []core.Model{{ID: "gpt-live", Object: "model"}}},
			wantModels: []string{"gpt-live"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := registryMockProvider{modelsResponse: tt.listing}
			if tt.listing == nil {
				mock.err = errListingDown
			}
			registry := newCatalogRegistry(t, &baseURLMockProvider{registryMockProvider: mock, baseURL: tt.baseURL})
			registry.SetConfiguredProviderModelsMode(config.ConfiguredProviderModelsModeFallback)
			if tt.configured != nil {
				registry.SetProviderConfiguredModels("openai", tt.configured)
			}

			err := registry.Initialize(context.Background())
			if tt.wantErr {
				require.Error(t, err)
				assert.Zero(t, registry.ModelCount())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantModels, modelIDs(registry))
		})
	}
}

// An inventory the provider reported earlier (or that was cached) is better
// than the catalog's guess, so a later listing failure keeps it; once the
// listing recovers, the live list replaces whatever stood in for it.
func TestRefreshProviderModels_CatalogNeverReplacesKnownInventory(t *testing.T) {
	provider := &baseURLMockProvider{err: errListingDown, baseURL: "https://api.openai.com/v1"}
	registry := newCatalogRegistry(t, provider)
	require.NoError(t, registry.Initialize(context.Background()))
	require.Equal(t, []string{"gpt-6-luna"}, modelIDs(registry))

	provider.err = nil
	provider.modelsResponse = &core.ModelsResponse{Object: "list", Data: []core.Model{{ID: "gpt-live", Object: "model"}}}
	_, err := registry.RefreshProviderModels(context.Background(), "openai")
	require.NoError(t, err)
	assert.Equal(t, []string{"gpt-live"}, modelIDs(registry), "the live list replaces the catalog's")
	assert.NotContains(t, registry.FailedProviderNames(), "openai")

	provider.err = errListingDown
	_, err = registry.RefreshProviderModels(context.Background(), "openai")
	require.Error(t, err)
	assert.Equal(t, []string{"gpt-live"}, modelIDs(registry), "a known inventory is kept, not replaced by the catalog")
}

// A catalog stand-in was never confirmed by the provider, so it is not cached,
// not even after a failed recheck carries it forward; the provider's own list
// is cached once a listing succeeds.
func TestSaveToCache_SkipsCatalogStandIn(t *testing.T) {
	cacheFile := t.TempDir() + "/models.json"
	provider := &baseURLMockProvider{baseURL: "https://api.openai.com/v1"}
	provider.err = errListingDown
	registry := newCatalogRegistry(t, provider)
	registry.SetCache(modelcache.NewLocalCache(cacheFile))

	cachedModels := func() int {
		t.Helper()
		restored := NewModelRegistry()
		restored.SetCache(modelcache.NewLocalCache(cacheFile))
		restored.RegisterProviderWithNameAndType(&registryMockProvider{err: errListingDown}, "openai", "openai")
		loaded, err := restored.LoadFromCache(context.Background())
		require.NoError(t, err)
		return loaded
	}

	require.NoError(t, registry.Initialize(context.Background()))
	require.NoError(t, registry.SaveToCache(context.Background()))
	assert.Zero(t, cachedModels(), "the stand-in must not be cached")

	_, err := registry.RefreshProviderModels(context.Background(), "openai")
	require.Error(t, err)
	require.NoError(t, registry.SaveToCache(context.Background()))
	assert.Zero(t, cachedModels(), "a failed recheck keeps the stand-in uncached")

	provider.err = nil
	provider.modelsResponse = &core.ModelsResponse{Object: "list", Data: []core.Model{{ID: "gpt-live", Object: "model"}}}
	_, err = registry.RefreshProviderModels(context.Background(), "openai")
	require.NoError(t, err)
	require.NoError(t, registry.SaveToCache(context.Background()))
	assert.Equal(t, 1, cachedModels(), "the live list is cached")
}
