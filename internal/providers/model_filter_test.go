package providers

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/cache/modelcache"
	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/modeldata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchesGlob(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		value   string
		want    bool
	}{
		// The motivating case: `*` must cross the vendor separator, which
		// filepath.Match would refuse.
		{name: "star crosses slash", pattern: "*:free", value: "deepseek/deepseek-r1:free", want: true},
		{name: "free suffix required", pattern: "*:free", value: "deepseek/deepseek-r1", want: false},
		{name: "vendor prefix", pattern: "openai/*", value: "openai/gpt-4o", want: true},
		{name: "vendor prefix mismatch", pattern: "openai/*", value: "google/gemini-2.5-pro", want: false},
		{name: "exact literal", pattern: "gpt-4o", value: "gpt-4o", want: true},
		{name: "literal mismatch", pattern: "gpt-4o", value: "gpt-4o-mini", want: false},
		{name: "case insensitive", pattern: "*:FREE", value: "qwen/qwen3:free", want: true},
		{name: "question mark matches one", pattern: "gpt-?", value: "gpt-4", want: true},
		{name: "question mark needs a character", pattern: "gpt-?", value: "gpt-", want: false},
		{name: "multiple stars", pattern: "*qwen*free*", value: "qwen/qwen3-coder:free", want: true},
		{name: "leading star backtracks", pattern: "*-r1:free", value: "deepseek/deepseek-r1-r1:free", want: true},
		{name: "bare star matches all", pattern: "*", value: "anything/at-all", want: true},
		{name: "empty pattern matches empty", pattern: "", value: "", want: true},
		{name: "empty pattern rejects value", pattern: "", value: "gpt-4o", want: false},
		{name: "trailing stars are optional", pattern: "gpt-4o**", value: "gpt-4o", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesGlob(tt.pattern, tt.value)
			assert.Equal(t, tt.want, got, "matchesGlob(%q, %q)", tt.pattern, tt.value)
		})
	}
}

func TestModelFilterKeep(t *testing.T) {
	priced := func(input, output float64) *core.ModelMetadata {
		return &core.ModelMetadata{Pricing: &core.ModelPricing{
			Currency:      "USD",
			InputPerMtok:  &input,
			OutputPerMtok: &output,
		}}
	}

	tests := []struct {
		name   string
		filter config.ModelFilter
		model  core.Model
		want   bool
	}{
		{
			name:   "include keeps match",
			filter: config.ModelFilter{Include: []string{"*:free"}},
			model:  core.Model{ID: "deepseek/deepseek-r1:free"},
			want:   true,
		},
		{
			name:   "include drops non-match",
			filter: config.ModelFilter{Include: []string{"*:free"}},
			model:  core.Model{ID: "openai/gpt-4o"},
			want:   false,
		},
		{
			name:   "non-matching exclude keeps included model",
			filter: config.ModelFilter{Include: []string{"*:free"}, Exclude: []string{"*-preview:free"}},
			model:  core.Model{ID: "google/gemini-flash:free"},
			want:   true,
		},
		{
			name:   "exclude narrows include",
			filter: config.ModelFilter{Include: []string{"*:free"}, Exclude: []string{"*-preview:free"}},
			model:  core.Model{ID: "google/gemini-preview:free"},
			want:   false,
		},
		{
			name:   "exclude drops match",
			filter: config.ModelFilter{Include: []string{"*"}, Exclude: []string{"*preview*"}},
			model:  core.Model{ID: "google/gemini-preview:free"},
			want:   false,
		},
		{
			name:   "price cap keeps model at the cap",
			filter: config.ModelFilter{MaxPricePerMtok: new(0.0)},
			model:  core.Model{ID: "qwen/qwen3:free", Metadata: priced(0, 0)},
			want:   true,
		},
		{
			name:   "price cap uses the higher rate",
			filter: config.ModelFilter{MaxPricePerMtok: new(1.0)},
			model:  core.Model{ID: "vendor/cheap-in-pricey-out", Metadata: priced(0.1, 5)},
			want:   false,
		},
		{
			name:   "price cap keeps model under both rates",
			filter: config.ModelFilter{MaxPricePerMtok: new(1.0)},
			model:  core.Model{ID: "vendor/cheap", Metadata: priced(0.1, 0.4)},
			want:   true,
		},
		{
			// A cap that admits models of unknown price is not a cap.
			name:   "price cap drops unpriced model",
			filter: config.ModelFilter{MaxPricePerMtok: new(1.0)},
			model:  core.Model{ID: "vendor/unknown"},
			want:   false,
		},
		{
			name:   "unpriced model survives pattern-only filter",
			filter: config.ModelFilter{Include: []string{"vendor/*"}},
			model:  core.Model{ID: "vendor/unknown"},
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter, ok := newModelFilter(tt.filter)
			require.True(t, ok, "newModelFilter(%+v) reported an empty filter", tt.filter)
			got := filter.keep(tt.model)
			assert.Equal(t, tt.want, got, "keep(%q)", tt.model.ID)
		})
	}
}

func TestNewModelFilterEmpty(t *testing.T) {
	tests := []struct {
		name   string
		filter config.ModelFilter
		want   bool
	}{
		{name: "zero value", filter: config.ModelFilter{}, want: false},
		{name: "blank patterns", filter: config.ModelFilter{Include: []string{"", "  "}}, want: false},
		{name: "include", filter: config.ModelFilter{Include: []string{"*:free"}}, want: true},
		{name: "zero price cap is a real cap", filter: config.ModelFilter{MaxPricePerMtok: new(0.0)}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := newModelFilter(tt.filter)
			assert.Equal(t, tt.want, ok)
		})
	}
}

func TestPublishFilteredInventory(t *testing.T) {
	registry := NewModelRegistry()
	registry.SetProviderModelFilter("openrouter", config.ModelFilter{Include: []string{"*:free"}})
	registry.discoveredByProvider = map[string]map[string]*ModelInfo{
		"openrouter": {
			"deepseek/deepseek-r1:free": {Model: core.Model{ID: "deepseek/deepseek-r1:free"}},
			"openai/gpt-4o":             {Model: core.Model{ID: "openai/gpt-4o"}},
		},
		// A provider without a filter publishes its whole inventory.
		"openai": {
			"gpt-4o": {Model: core.Model{ID: "gpt-4o"}},
		},
	}
	dropped := registry.publishFilteredInventoryLocked()
	require.Equal(t, 1, dropped)
	_, ok := registry.modelsByProvider["openrouter"]["deepseek/deepseek-r1:free"]
	assert.True(t, ok)
	_, ok = registry.modelsByProvider["openrouter"]["openai/gpt-4o"]
	assert.False(t, ok)
	assert.Len(t, registry.modelsByProvider["openai"], 1)

	// The inventory itself must not be edited: filtering is a view over it.
	assert.Len(t, registry.discoveredByProvider["openrouter"], 2)
}

// A filter that matches nothing must leave the provider present with an empty
// published inventory: dropping the key would read as a failed refresh and
// resurrect the previous inventory through the carry-forward path.
func TestPublishFilteredInventoryKeepsEmptiedProvider(t *testing.T) {
	registry := NewModelRegistry()
	registry.SetProviderModelFilter("openrouter", config.ModelFilter{Include: []string{"*:free"}})
	registry.discoveredByProvider = map[string]map[string]*ModelInfo{
		"openrouter": {"openai/gpt-4o": {Model: core.Model{ID: "openai/gpt-4o"}}},
	}
	registry.publishFilteredInventoryLocked()

	models, ok := registry.modelsByProvider["openrouter"]
	require.True(t, ok)
	assert.Empty(t, models)
}

func TestSetProviderModelFilterClears(t *testing.T) {
	registry := NewModelRegistry()
	registry.SetProviderModelFilter("openrouter", config.ModelFilter{Include: []string{"*:free"}})
	registry.SetProviderModelFilter("openrouter", config.ModelFilter{})
	filters := registry.snapshotProviderModelFilters()
	assert.Empty(t, filters)
}

// End-to-end through Initialize: the filter must narrow what the catalog
// actually serves, not just the per-provider map, so a filtered-out model is
// unroutable rather than merely hidden from /v1/models.
func TestInitialize_AppliesProviderModelFilter(t *testing.T) {
	free := 0.0
	paid := 3.0
	provider := &registryMockProvider{
		name: "openrouter",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "deepseek/deepseek-r1:free", Object: "model", Metadata: &core.ModelMetadata{
					Pricing: &core.ModelPricing{Currency: "USD", InputPerMtok: &free, OutputPerMtok: &free},
				}},
				{ID: "openai/gpt-4o", Object: "model", Metadata: &core.ModelMetadata{
					Pricing: &core.ModelPricing{Currency: "USD", InputPerMtok: &paid, OutputPerMtok: &paid},
				}},
			},
		},
	}

	registry := NewModelRegistry()
	registry.RegisterProviderWithNameAndType(provider, "openrouter", "openrouter")
	registry.SetProviderModelFilter("openrouter", config.ModelFilter{MaxPricePerMtok: &free})
	err := registry.Initialize(context.Background())
	require.NoError(t, err)
	got := registry.GetProvider("deepseek/deepseek-r1:free")
	assert.NotNil(t, got)
	got = registry.GetProvider("openai/gpt-4o")
	assert.Nil(t, got)
}

// Filtering is a view, not a deletion: once the model list prices a model back
// under the cap, it returns to the catalog without waiting for a refetch.
func TestEnrichModels_ReadmitsModelThatPricesBackUnderCap(t *testing.T) {
	provider := &registryMockProvider{
		name: "openrouter",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data:   []core.Model{{ID: "vendor/volatile", Object: "model"}},
		},
	}

	registry := NewModelRegistry()
	registry.RegisterProviderWithNameAndType(provider, "openrouter", "openai")
	registry.SetProviderModelFilter("openrouter", config.ModelFilter{MaxPricePerMtok: new(1.0)})
	err := // Unpriced at fetch, so the cap rejects it.
		registry.Initialize(context.Background())
	require.NoError(t, err)
	require.Nil(t, registry.GetModel("openrouter/vendor/volatile"))

	enrich := func(outputPerMtok string) {
		raw := []byte(`{"version":1,"updated_at":"2025-01-01T00:00:00Z","providers":{},"models":{` +
			`"vendor/volatile":{"pricing":{"currency":"USD","input_per_mtok":0.1,"output_per_mtok":` + outputPerMtok + `}}` +
			`},"provider_models":{}}`)
		list, err := modeldata.Parse(raw)
		require.NoError(t, err)

		registry.SetModelList(list, raw)
		registry.EnrichModels()
	}

	enrich("9")
	require.Nil(t, registry.GetModel("openrouter/vendor/volatile"))

	// The inventory kept the model, so a price drop re-admits it.
	enrich("0.4")
	assert.NotNil(t, registry.GetModel("openrouter/vendor/volatile"))
	assert.NotNil(t, registry.GetProvider("vendor/volatile"))
}

// Clearing a filter must restore the models it was hiding without a refetch.
func TestSetProviderModelFilter_RepublishesCatalog(t *testing.T) {
	provider := &registryMockProvider{
		name: "openrouter",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "deepseek/deepseek-r1:free", Object: "model"},
				{ID: "openai/gpt-4o", Object: "model"},
			},
		},
	}

	registry := NewModelRegistry()
	registry.RegisterProviderWithNameAndType(provider, "openrouter", "openai")
	registry.SetProviderModelFilter("openrouter", config.ModelFilter{Include: []string{"*:free"}})
	err := registry.Initialize(context.Background())
	require.NoError(t, err)
	require.Nil(t, registry.GetModel("openrouter/openai/gpt-4o"))

	registry.SetProviderModelFilter("openrouter", config.ModelFilter{})

	assert.NotNil(t, registry.GetModel("openrouter/openai/gpt-4o"))
	assert.NotNil(t, registry.GetModel("openrouter/deepseek/deepseek-r1:free"))
}

// A model admitted while unpriced must leave the catalog once the remote model
// list prices it above the cap, rather than staying routable until the next
// provider fetch sweep.
func TestEnrichModels_ReappliesPriceCap(t *testing.T) {
	provider := &registryMockProvider{
		name: "openrouter",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "vendor/cheap", Object: "model"},
				{ID: "vendor/pricey", Object: "model"},
			},
		},
	}

	registry := NewModelRegistry()
	registry.RegisterProviderWithNameAndType(provider, "openrouter", "openai")
	err := // The cap is applied only after enrichment supplies prices, so both models
		// must first enter the catalog with no pricing at all.
		registry.Initialize(context.Background())
	require.NoError(t, err)

	registry.SetProviderModelFilter("openrouter", config.ModelFilter{MaxPricePerMtok: new(1.0)})

	raw := []byte(`{"version":1,"updated_at":"2025-01-01T00:00:00Z","providers":{},"models":{` +
		`"vendor/cheap":{"pricing":{"currency":"USD","input_per_mtok":0.2,"output_per_mtok":0.4}},` +
		`"vendor/pricey":{"pricing":{"currency":"USD","input_per_mtok":0.2,"output_per_mtok":9}}` +
		`},"provider_models":{}}`)
	list, err := modeldata.Parse(raw)
	require.NoError(t, err)

	registry.SetModelList(list, raw)
	registry.EnrichModels()

	assert.NotNil(t, registry.GetModel("openrouter/vendor/cheap"))
	assert.Nil(t, registry.GetModel("openrouter/vendor/pricey"))
	assert.Nil(t, registry.GetProvider("vendor/pricey"))
}

// The cache must hold what the provider actually served, not what the filter
// currently admits. Persisting the filtered absence would make a loosened
// filter unrecoverable on a restart where the upstream listing is unreachable.
func TestSaveToCache_PersistsUnfilteredInventory(t *testing.T) {
	cacheFile := filepath.Join(t.TempDir(), "models.json")
	provider := &registryMockProvider{
		name: "openrouter",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "deepseek/deepseek-r1:free", Object: "model", OwnedBy: "openrouter"},
				{ID: "openai/gpt-4o", Object: "model", OwnedBy: "openrouter"},
			},
		},
	}

	registry := NewModelRegistry()
	registry.SetCache(modelcache.NewLocalCache(cacheFile))
	registry.RegisterProviderWithNameAndType(provider, "openrouter", "openai")
	registry.SetProviderModelFilter("openrouter", config.ModelFilter{Include: []string{"*:free"}})
	err := registry.Initialize(context.Background())
	require.NoError(t, err)
	err = registry.SaveToCache(context.Background())
	require.NoError(t, err)

	// Restore into a registry whose filter has since been removed, with the
	// provider unreachable so only the cache can supply the catalog.
	offline := &registryMockProvider{name: "openrouter", err: errors.New("connection refused")}
	restored := NewModelRegistry()
	restored.SetCache(modelcache.NewLocalCache(cacheFile))
	restored.RegisterProviderWithNameAndType(offline, "openrouter", "openai")
	loaded, err := restored.LoadFromCache(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, loaded)
	assert.NotNil(t, restored.GetModel("openrouter/openai/gpt-4o"))
}
