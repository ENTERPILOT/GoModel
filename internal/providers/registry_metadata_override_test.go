package providers

import (
	"context"
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/modeldata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInitialize_AppliesConfigMetadataOverrides verifies that operator-supplied
// metadata from config.yaml takes precedence over (and merges onto) the remote
// model registry during provider initialization. Exercises the config-driven
// metadata feature for local providers like Ollama whose custom model IDs do
// not appear in the upstream registry.
func TestInitialize_AppliesConfigMetadataOverrides(t *testing.T) {
	registry := NewModelRegistry()

	local := &registryMockProvider{
		name: "provider-nippur",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "GLM-4.7-Flash", Object: "model", OwnedBy: "ollama"},
				{ID: "Gemma4-31B", Object: "model", OwnedBy: "ollama"},
			},
		},
	}
	registry.RegisterProviderWithNameAndType(local, "nippur", "ollama")

	// Empty remote model list so nothing is enriched from the registry; the
	// overrides are the only source of metadata.
	raw := []byte(`{"version":1,"updated_at":"2025-01-01T00:00:00Z","providers":{},"models":{},"provider_models":{}}`)
	list, err := modeldata.Parse(raw)
	require.NoError(t, err)

	registry.SetModelList(list, raw)

	registry.SetProviderMetadataOverrides("nippur", map[string]*core.ModelMetadata{
		"GLM-4.7-Flash": {
			DisplayName:   "GLM 4.7 Flash (local)",
			ContextWindow: new(131072),
			Capabilities:  map[string]bool{"tools": true},
			Pricing: &core.ModelPricing{
				Currency:      "USD",
				InputPerMtok:  new(float64(0)),
				OutputPerMtok: new(float64(0)),
			},
		},
	})
	err = registry.Initialize(context.Background())
	require.NoError(t, err)

	overridden := registry.GetModel("nippur/GLM-4.7-Flash")
	require.NotNil(t, overridden)
	require.NotNil(t, overridden.Model.Metadata)
	got := overridden.Model.Metadata.DisplayName
	assert.Equal(t, "GLM 4.7 Flash (local)", got)
	assert.NotNil(t, overridden.Model.Metadata.ContextWindow)
	assert.Equal(t, 131072, *overridden.Model.Metadata.ContextWindow)
	assert.True(t, overridden.Model.Metadata.Capabilities["tools"])
	got = overridden.Model.Metadata.PricingSources["input_per_mtok"]
	assert.Equal(t, core.ModelPricingSourceConfigYAML, got)
	got = overridden.Model.Metadata.PricingSources["output_per_mtok"]
	assert.Equal(t, core.ModelPricingSourceConfigYAML, got)

	untouched := registry.GetModel("nippur/Gemma4-31B")
	require.NotNil(t, untouched)
	assert.Nil(t, untouched.Model.Metadata)
}

// TestInitialize_OverrideMergesOnRemoteEnrichment verifies field-wise merging:
// fields declared in config win; unmentioned fields fall back to whatever the
// remote registry produced during enrichment.
func TestInitialize_OverrideMergesOnRemoteEnrichment(t *testing.T) {
	registry := NewModelRegistry()

	provider := &registryMockProvider{
		name: "provider-main",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "shared-model", Object: "model", OwnedBy: "openai"},
			},
		},
	}
	registry.RegisterProviderWithNameAndType(provider, "openai-main", "openai")

	raw := []byte(`{
		"version": 1,
		"updated_at": "2025-01-01T00:00:00Z",
		"providers": {"openai": {"display_name": "OpenAI", "api_type": "openai", "supported_modes": ["chat"]}},
		"models": {"shared-model": {"display_name": "Remote Display", "modes": ["chat"]}},
		"provider_models": {"openai/shared-model": {"model_ref": "shared-model", "enabled": true, "context_window": 99999}}
	}`)
	list, err := modeldata.Parse(raw)
	require.NoError(t, err)

	registry.SetModelList(list, raw)

	// Override only the context window; display name should come from the remote registry.
	registry.SetProviderMetadataOverrides("openai-main", map[string]*core.ModelMetadata{
		"shared-model": {ContextWindow: new(262144)},
	})
	err = registry.Initialize(context.Background())
	require.NoError(t, err)

	info := registry.GetModel("openai-main/shared-model")
	require.NotNil(t, info)
	require.NotNil(t, info.Model.Metadata)
	assert.Equal(t, "Remote Display", info.Model.Metadata.DisplayName)
	assert.NotNil(t, info.Model.Metadata.ContextWindow)
	assert.Equal(t, 262144, *info.Model.Metadata.ContextWindow)
}

func TestResolvePricingPrefersProviderSpecificMetadata(t *testing.T) {
	registry := NewModelRegistry()

	primary := &registryMockProvider{
		name: "provider-primary",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "shared-model", Object: "model", OwnedBy: "openai"},
			},
		},
	}
	backup := &registryMockProvider{
		name: "provider-backup",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "shared-model", Object: "model", OwnedBy: "openai"},
			},
		},
	}
	registry.RegisterProviderWithNameAndType(primary, "openai-primary", "openai")
	registry.RegisterProviderWithNameAndType(backup, "openai-backup", "openai")

	raw := []byte(`{"version":1,"updated_at":"2025-01-01T00:00:00Z","providers":{},"models":{},"provider_models":{}}`)
	list, err := modeldata.Parse(raw)
	require.NoError(t, err)

	registry.SetModelList(list, raw)

	primaryRate := 1.0
	backupRate := 2.0
	registry.SetProviderMetadataOverrides("openai-primary", map[string]*core.ModelMetadata{
		"shared-model": {Pricing: &core.ModelPricing{InputPerMtok: &primaryRate}},
	})
	registry.SetProviderMetadataOverrides("openai-backup", map[string]*core.ModelMetadata{
		"shared-model": {Pricing: &core.ModelPricing{InputPerMtok: &backupRate}},
	})
	err = registry.Initialize(context.Background())
	require.NoError(t, err)

	pricing := registry.ResolvePricing("shared-model", "openai-backup")
	require.NotNil(t, pricing)
	require.NotNil(t, pricing.InputPerMtok)
	require.Equal(t, backupRate, *pricing.InputPerMtok)
}

func TestResolvePricingPrefersProviderOwnedRawSlashMetadata(t *testing.T) {
	registry := NewModelRegistry()

	other := &registryMockProvider{
		name: "provider-other",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "openrouter/free", Object: "model", OwnedBy: "other"},
			},
		},
	}
	openRouter := &registryMockProvider{
		name: "provider-openrouter",
		modelsResponse: &core.ModelsResponse{
			Object: "list",
			Data: []core.Model{
				{ID: "free", Object: "model", OwnedBy: "openrouter"},
				{ID: "openrouter/free", Object: "model", OwnedBy: "openrouter"},
			},
		},
	}
	registry.RegisterProviderWithNameAndType(other, "other", "other")
	registry.RegisterProviderWithNameAndType(openRouter, "openrouter", "openrouter")

	raw := []byte(`{"version":1,"updated_at":"2025-01-01T00:00:00Z","providers":{},"models":{},"provider_models":{}}`)
	list, err := modeldata.Parse(raw)
	require.NoError(t, err)

	registry.SetModelList(list, raw)

	otherRate := 1.0
	openRouterRate := 2.0
	registry.SetProviderMetadataOverrides("other", map[string]*core.ModelMetadata{
		"openrouter/free": {Pricing: &core.ModelPricing{InputPerMtok: &otherRate}},
	})
	registry.SetProviderMetadataOverrides("openrouter", map[string]*core.ModelMetadata{
		"openrouter/free": {Pricing: &core.ModelPricing{InputPerMtok: &openRouterRate}},
	})
	err = registry.Initialize(context.Background())
	require.NoError(t, err)

	pricing := registry.ResolvePricing("openrouter/free", "openrouter")
	require.NotNil(t, pricing)
	require.NotNil(t, pricing.InputPerMtok)
	require.Equal(t, openRouterRate, *pricing.InputPerMtok)
}

func TestApplyConfigMetadataOverrides_MergesPricingSourcesPerField(t *testing.T) {
	baseInput := 1.0
	baseOutput := 2.0
	configInput := 3.0
	existing := &ModelInfo{
		Model: core.Model{
			ID: "priced-model",
			Metadata: &core.ModelMetadata{
				Pricing: &core.ModelPricing{
					Currency:      "USD",
					InputPerMtok:  &baseInput,
					OutputPerMtok: &baseOutput,
				},
				PricingSources: map[string]string{
					"input_per_mtok":  core.ModelPricingSourceModelRegistry,
					"output_per_mtok": core.ModelPricingSourceModelRegistry,
				},
			},
		},
		ProviderName: "openai-main",
		ProviderType: "openai",
	}
	modelsByProvider := map[string]map[string]*ModelInfo{
		"openai-main": {"priced-model": existing},
	}
	overrides := map[string]map[string]*core.ModelMetadata{
		"openai-main": {
			"priced-model": {
				Pricing: &core.ModelPricing{InputPerMtok: &configInput},
			},
		},
	}

	applied := applyConfigMetadataOverrides(overrides, modelsByProvider, nil)
	require.Equal(t, 1, applied)

	metadata := existing.Model.Metadata
	require.NotNil(t, metadata.Pricing)
	require.NotNil(t, metadata.Pricing.InputPerMtok)
	require.Equal(t, configInput, *metadata.Pricing.InputPerMtok)
	require.NotNil(t, metadata.Pricing.OutputPerMtok)
	require.Equal(t, baseOutput, *metadata.Pricing.OutputPerMtok)
	got := metadata.PricingSources["input_per_mtok"]
	assert.Equal(t, core.ModelPricingSourceConfigYAML, got)
	got = metadata.PricingSources["output_per_mtok"]
	assert.Equal(t, core.ModelPricingSourceModelRegistry, got)
}

// TestApplyConfigMetadataOverrides_NoOpPreservesPointerIdentity verifies that
// an override whose fields already match the current metadata does not
// replace the ModelInfo pointer — a concurrent reader that captured the
// pointer before re-enrichment keeps a consistent view.
func TestApplyConfigMetadataOverrides_NoOpPreservesPointerIdentity(t *testing.T) {
	existing := &ModelInfo{
		Model: core.Model{
			ID: "same-model",
			Metadata: &core.ModelMetadata{
				DisplayName:   "Same Display",
				ContextWindow: new(131072),
				Capabilities:  map[string]bool{"tools": true},
			},
		},
		ProviderName: "nippur",
		ProviderType: "ollama",
	}
	modelsByProvider := map[string]map[string]*ModelInfo{
		"nippur": {"same-model": existing},
	}
	// Override is byte-equal to existing metadata — nothing to do.
	overrides := map[string]map[string]*core.ModelMetadata{
		"nippur": {
			"same-model": {
				DisplayName:   "Same Display",
				ContextWindow: new(131072),
				Capabilities:  map[string]bool{"tools": true},
			},
		},
	}
	replacements := make(map[*ModelInfo]*ModelInfo)
	applied := applyConfigMetadataOverrides(overrides, modelsByProvider, replacements)
	assert.Equal(t, 0, applied)
	got := modelsByProvider["nippur"]["same-model"]
	assert.Equal(t, existing, got)
	assert.Empty(t, replacements)
}

// TestApplyConfigMetadataOverrides_NonNilEmptyReplacementsDoesNotPanic covers
// the case where a non-nil but empty replacements map is passed in (as
// enrichModelsLocked does when the model list produced no replacements); the
// override path must initialise reverse so the else-branch write does not
// panic on a nil map.
func TestApplyConfigMetadataOverrides_NonNilEmptyReplacementsDoesNotPanic(t *testing.T) {
	existing := &ModelInfo{
		Model: core.Model{ID: "m", Metadata: &core.ModelMetadata{DisplayName: "Old"}},
	}
	modelsByProvider := map[string]map[string]*ModelInfo{
		"p": {"m": existing},
	}
	overrides := map[string]map[string]*core.ModelMetadata{
		"p": {"m": {DisplayName: "New"}},
	}
	replacements := make(map[*ModelInfo]*ModelInfo) // non-nil, empty
	applied := applyConfigMetadataOverrides(overrides, modelsByProvider, replacements)
	assert.Equal(t, 1, applied)

	next := modelsByProvider["p"]["m"]
	assert.NotSame(t, existing, next)
	assert.Equal(t, next, replacements[existing])
}

// TestMetadataOverrideEmpty covers the reflect-based emptiness check so new
// fields on core.ModelMetadata or core.ModelPricing are picked up by the
// short-circuit without touching registry.go.
func TestMetadataOverrideEmpty(t *testing.T) {
	cases := []struct {
		name string
		in   *core.ModelMetadata
		want bool
	}{
		{"nil", nil, true},
		{"zero struct", &core.ModelMetadata{}, true},
		{"empty pricing pointer", &core.ModelMetadata{Pricing: &core.ModelPricing{}}, true},
		{"empty non-nil modes", &core.ModelMetadata{Modes: []string{}}, true},
		{"empty non-nil tags", &core.ModelMetadata{Tags: []string{}}, true},
		{"empty non-nil categories", &core.ModelMetadata{Categories: []core.ModelCategory{}}, true},
		{"empty non-nil capabilities", &core.ModelMetadata{Capabilities: map[string]bool{}}, true},
		{"empty non-nil rankings", &core.ModelMetadata{Rankings: map[string]core.ModelRanking{}}, true},
		{"pricing with empty tiers", &core.ModelMetadata{Pricing: &core.ModelPricing{Tiers: []core.ModelPricingTier{}}}, true},
		{"display name set", &core.ModelMetadata{DisplayName: "X"}, false},
		{"context window set", &core.ModelMetadata{ContextWindow: new(1024)}, false},
		{"capabilities set", &core.ModelMetadata{Capabilities: map[string]bool{"tools": true}}, false},
		{"modes set", &core.ModelMetadata{Modes: []string{"chat"}}, false},
		{"pricing currency set", &core.ModelMetadata{Pricing: &core.ModelPricing{Currency: "USD"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := metadataOverrideEmpty(tc.in)
			assert.Equal(t, tc.want, got, "metadataOverrideEmpty(%+v)", tc.in)
		})
	}
}

// TestApplyConfigMetadataOverrides_EmptyOverrideLeavesNilMetadataNil verifies
// that an override whose fields are all zero does not publish any change,
// most importantly does not turn nil current metadata into an empty &struct.
func TestApplyConfigMetadataOverrides_EmptyOverrideLeavesNilMetadataNil(t *testing.T) {
	existing := &ModelInfo{
		Model: core.Model{ID: "m", Metadata: nil},
	}
	modelsByProvider := map[string]map[string]*ModelInfo{
		"p": {"m": existing},
	}
	overrides := map[string]map[string]*core.ModelMetadata{
		"p": {"m": {}}, // non-nil, all fields zero
	}
	applied := applyConfigMetadataOverrides(overrides, modelsByProvider, nil)
	assert.Equal(t, 0, applied)
	assert.Nil(t, existing.Model.Metadata)
}

// TestSetProviderMetadataOverrides_DeepClonesExternalInput verifies that
// mutating the caller's override map/slices/pointers after handing them to
// the registry does not leak into registry state.
func TestSetProviderMetadataOverrides_DeepClonesExternalInput(t *testing.T) {
	registry := NewModelRegistry()

	external := map[string]*core.ModelMetadata{
		"m": {
			Modes:         []string{"chat"},
			Capabilities:  map[string]bool{"tools": true},
			ContextWindow: new(4096),
			Pricing:       &core.ModelPricing{Currency: "USD"},
		},
	}
	registry.SetProviderMetadataOverrides("p", external)

	// Mutate the caller's copy in every aliasable way.
	external["m"].Modes[0] = "mutated"
	external["m"].Capabilities["tools"] = false
	*external["m"].ContextWindow = 0
	external["m"].Pricing.Currency = "EUR"

	snap := registry.snapshotConfigOverrides()
	stored := snap["p"]["m"]
	require.NotNil(t, stored)
	assert.Equal(t, "chat", stored.Modes[0], "stored Modes mutated via caller: %v", stored.Modes)
	assert.True(t, stored.Capabilities["tools"])
	assert.NotNil(t, stored.ContextWindow)
	assert.Equal(t, 4096, *stored.ContextWindow)
	assert.NotNil(t, stored.Pricing)
	assert.Equal(t, "USD", stored.Pricing.Currency)
}

// TestEnrichModels_KeepsProviderDiscoveredMetadataOverCatalog covers the local
// server case: a llama.cpp alias that collides with a catalog ID must not lose
// the context window the running server actually reported.
func TestEnrichModels_KeepsProviderDiscoveredMetadataOverCatalog(t *testing.T) {
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
			},
		},
	}
	registry.RegisterProviderWithType(mock, "llamacpp")
	err := registry.Initialize(context.Background())
	require.NoError(t, err)

	// The catalog knows the bare model ID and has no llamacpp entry at all.
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

	info := registry.GetModel("gemma-3-4b-it")
	require.NotNil(t, info)
	require.NotNil(t, info.Model.Metadata)

	meta := info.Model.Metadata
	require.NotNil(t, meta.ContextWindow)
	require.Equal(t, runtimeContext, *meta.ContextWindow)
	require.True(t, meta.Capabilities["vision"])

	// The catalog still fills in what the server never reports.
	require.Equal(t, "Gemma 3 4B IT", meta.DisplayName)
	require.Len(t, meta.Modes, 1)
	require.Equal(t, "chat", meta.Modes[0])
}
