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
			if got := matchesGlob(tt.pattern, tt.value); got != tt.want {
				t.Errorf("matchesGlob(%q, %q) = %v, want %v", tt.pattern, tt.value, got, tt.want)
			}
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
			if !ok {
				t.Fatalf("newModelFilter(%+v) reported an empty filter", tt.filter)
			}
			if got := filter.keep(tt.model); got != tt.want {
				t.Errorf("keep(%q) = %v, want %v", tt.model.ID, got, tt.want)
			}
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
			if _, ok := newModelFilter(tt.filter); ok != tt.want {
				t.Errorf("newModelFilter(%+v) ok = %v, want %v", tt.filter, ok, tt.want)
			}
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

	if dropped := registry.publishFilteredInventoryLocked(); dropped != 1 {
		t.Fatalf("dropped = %d, want 1", dropped)
	}
	if _, ok := registry.modelsByProvider["openrouter"]["deepseek/deepseek-r1:free"]; !ok {
		t.Error("free model was dropped, want published")
	}
	if _, ok := registry.modelsByProvider["openrouter"]["openai/gpt-4o"]; ok {
		t.Error("paid model was published, want filtered out")
	}
	if len(registry.modelsByProvider["openai"]) != 1 {
		t.Errorf("unfiltered provider models = %d, want 1", len(registry.modelsByProvider["openai"]))
	}
	// The inventory itself must not be edited: filtering is a view over it.
	if len(registry.discoveredByProvider["openrouter"]) != 2 {
		t.Errorf("inventory models = %d, want 2 retained", len(registry.discoveredByProvider["openrouter"]))
	}
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
	if !ok {
		t.Fatal("provider key was removed, want retained with an empty inventory")
	}
	if len(models) != 0 {
		t.Errorf("models = %d, want 0", len(models))
	}
}

func TestSetProviderModelFilterClears(t *testing.T) {
	registry := NewModelRegistry()
	registry.SetProviderModelFilter("openrouter", config.ModelFilter{Include: []string{"*:free"}})
	registry.SetProviderModelFilter("openrouter", config.ModelFilter{})

	if filters := registry.snapshotProviderModelFilters(); len(filters) != 0 {
		t.Errorf("filters = %+v, want cleared", filters)
	}
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

	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() failed: %v", err)
	}

	if got := registry.GetProvider("deepseek/deepseek-r1:free"); got == nil {
		t.Error("free model is not routable, want kept")
	}
	if got := registry.GetProvider("openai/gpt-4o"); got != nil {
		t.Error("model above the price cap is routable, want dropped")
	}
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

	// Unpriced at fetch, so the cap rejects it.
	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() failed: %v", err)
	}
	if registry.GetModel("openrouter/vendor/volatile") != nil {
		t.Fatal("unpriced model was admitted under a price cap, want dropped")
	}

	enrich := func(outputPerMtok string) {
		raw := []byte(`{"version":1,"updated_at":"2025-01-01T00:00:00Z","providers":{},"models":{` +
			`"vendor/volatile":{"pricing":{"currency":"USD","input_per_mtok":0.1,"output_per_mtok":` + outputPerMtok + `}}` +
			`},"provider_models":{}}`)
		list, err := modeldata.Parse(raw)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		registry.SetModelList(list, raw)
		registry.EnrichModels()
	}

	enrich("9")
	if registry.GetModel("openrouter/vendor/volatile") != nil {
		t.Fatal("model priced above the cap is in the catalog, want dropped")
	}

	// The inventory kept the model, so a price drop re-admits it.
	enrich("0.4")
	if registry.GetModel("openrouter/vendor/volatile") == nil {
		t.Error("model priced back under the cap was not re-admitted")
	}
	if registry.GetProvider("vendor/volatile") == nil {
		t.Error("re-admitted model is not routable by bare ID")
	}
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
	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() failed: %v", err)
	}
	if registry.GetModel("openrouter/openai/gpt-4o") != nil {
		t.Fatal("filtered model is in the catalog, want dropped")
	}

	registry.SetProviderModelFilter("openrouter", config.ModelFilter{})

	if registry.GetModel("openrouter/openai/gpt-4o") == nil {
		t.Error("clearing the filter did not restore the model")
	}
	if registry.GetModel("openrouter/deepseek/deepseek-r1:free") == nil {
		t.Error("clearing the filter dropped a model it had kept")
	}
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

	// The cap is applied only after enrichment supplies prices, so both models
	// must first enter the catalog with no pricing at all.
	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() failed: %v", err)
	}
	registry.SetProviderModelFilter("openrouter", config.ModelFilter{MaxPricePerMtok: new(1.0)})

	raw := []byte(`{"version":1,"updated_at":"2025-01-01T00:00:00Z","providers":{},"models":{` +
		`"vendor/cheap":{"pricing":{"currency":"USD","input_per_mtok":0.2,"output_per_mtok":0.4}},` +
		`"vendor/pricey":{"pricing":{"currency":"USD","input_per_mtok":0.2,"output_per_mtok":9}}` +
		`},"provider_models":{}}`)
	list, err := modeldata.Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	registry.SetModelList(list, raw)
	registry.EnrichModels()

	if registry.GetModel("openrouter/vendor/cheap") == nil {
		t.Error("model under the cap was dropped, want kept")
	}
	if registry.GetModel("openrouter/vendor/pricey") != nil {
		t.Error("model priced above the cap is still in the catalog, want dropped")
	}
	if registry.GetProvider("vendor/pricey") != nil {
		t.Error("model priced above the cap is still routable by bare ID, want dropped")
	}
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
	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() failed: %v", err)
	}
	if err := registry.SaveToCache(context.Background()); err != nil {
		t.Fatalf("SaveToCache() failed: %v", err)
	}

	// Restore into a registry whose filter has since been removed, with the
	// provider unreachable so only the cache can supply the catalog.
	offline := &registryMockProvider{name: "openrouter", err: errors.New("connection refused")}
	restored := NewModelRegistry()
	restored.SetCache(modelcache.NewLocalCache(cacheFile))
	restored.RegisterProviderWithNameAndType(offline, "openrouter", "openai")
	loaded, err := restored.LoadFromCache(context.Background())
	if err != nil {
		t.Fatalf("LoadFromCache() failed: %v", err)
	}
	if loaded != 2 {
		t.Fatalf("loaded = %d, want 2 models restored from cache", loaded)
	}
	if restored.GetModel("openrouter/openai/gpt-4o") == nil {
		t.Error("the filtered-out model was not persisted, want the cache to hold it")
	}
}
