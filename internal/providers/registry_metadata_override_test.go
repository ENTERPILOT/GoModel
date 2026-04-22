package providers

import (
	"context"
	"testing"

	"gomodel/internal/core"
	"gomodel/internal/modeldata"
)

func ctxWindow(v int) *int { return &v }

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
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	registry.SetModelList(list, raw)

	registry.SetProviderMetadataOverrides("nippur", map[string]*core.ModelMetadata{
		"GLM-4.7-Flash": {
			DisplayName:   "GLM 4.7 Flash (local)",
			ContextWindow: ctxWindow(131072),
			Capabilities:  map[string]bool{"tools": true},
		},
	})

	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	overridden := registry.GetModel("nippur/GLM-4.7-Flash")
	if overridden == nil || overridden.Model.Metadata == nil {
		t.Fatal("expected nippur/GLM-4.7-Flash to have metadata after override")
	}
	if got := overridden.Model.Metadata.DisplayName; got != "GLM 4.7 Flash (local)" {
		t.Errorf("DisplayName = %q, want GLM 4.7 Flash (local)", got)
	}
	if overridden.Model.Metadata.ContextWindow == nil || *overridden.Model.Metadata.ContextWindow != 131072 {
		t.Errorf("ContextWindow = %v, want 131072", overridden.Model.Metadata.ContextWindow)
	}
	if !overridden.Model.Metadata.Capabilities["tools"] {
		t.Errorf("Capabilities[tools] = false, want true")
	}

	untouched := registry.GetModel("nippur/Gemma4-31B")
	if untouched == nil {
		t.Fatal("expected Gemma4-31B to be registered")
	}
	if untouched.Model.Metadata != nil {
		t.Errorf("expected nil metadata for non-overridden model, got %+v", untouched.Model.Metadata)
	}
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
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	registry.SetModelList(list, raw)

	// Override only the context window; display name should come from the remote registry.
	registry.SetProviderMetadataOverrides("openai-main", map[string]*core.ModelMetadata{
		"shared-model": {ContextWindow: ctxWindow(262144)},
	})

	if err := registry.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	info := registry.GetModel("openai-main/shared-model")
	if info == nil || info.Model.Metadata == nil {
		t.Fatal("expected metadata")
	}
	if info.Model.Metadata.DisplayName != "Remote Display" {
		t.Errorf("DisplayName = %q, want Remote Display (remote preserved)", info.Model.Metadata.DisplayName)
	}
	if info.Model.Metadata.ContextWindow == nil || *info.Model.Metadata.ContextWindow != 262144 {
		t.Errorf("ContextWindow = %v, want 262144 (override wins)", info.Model.Metadata.ContextWindow)
	}
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
				ContextWindow: ctxWindow(131072),
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
				ContextWindow: ctxWindow(131072),
				Capabilities:  map[string]bool{"tools": true},
			},
		},
	}
	replacements := make(map[*ModelInfo]*ModelInfo)
	applied := applyConfigMetadataOverrides(overrides, modelsByProvider, replacements)
	if applied != 0 {
		t.Errorf("applied = %d, want 0 for no-op merge", applied)
	}
	if got := modelsByProvider["nippur"]["same-model"]; got != existing {
		t.Errorf("ModelInfo pointer changed on no-op merge: got %p, want %p", got, existing)
	}
	if len(replacements) != 0 {
		t.Errorf("replacements = %v, want empty on no-op", replacements)
	}
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
	if applied != 1 {
		t.Errorf("applied = %d, want 1", applied)
	}
	next := modelsByProvider["p"]["m"]
	if next == existing {
		t.Error("expected a replacement ModelInfo pointer, got original")
	}
	if replacements[existing] != next {
		t.Errorf("replacements[existing] = %p, want %p", replacements[existing], next)
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
	if applied != 0 {
		t.Errorf("applied = %d, want 0 for empty override", applied)
	}
	if existing.Model.Metadata != nil {
		t.Errorf("Metadata = %+v, want nil (empty override should not publish)", existing.Model.Metadata)
	}
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
			ContextWindow: ctxWindow(4096),
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
	if stored == nil {
		t.Fatal("expected stored override for p/m")
	}
	if stored.Modes[0] != "chat" {
		t.Errorf("stored Modes mutated via caller: %v", stored.Modes)
	}
	if !stored.Capabilities["tools"] {
		t.Error("stored Capabilities mutated via caller")
	}
	if stored.ContextWindow == nil || *stored.ContextWindow != 4096 {
		t.Errorf("stored ContextWindow mutated via caller: %v", stored.ContextWindow)
	}
	if stored.Pricing == nil || stored.Pricing.Currency != "USD" {
		t.Errorf("stored Pricing mutated via caller: %+v", stored.Pricing)
	}
}
