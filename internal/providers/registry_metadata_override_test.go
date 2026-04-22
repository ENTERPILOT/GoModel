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
