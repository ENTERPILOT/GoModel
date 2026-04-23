package config

import (
	"testing"

	"gopkg.in/yaml.v3"

	"gomodel/internal/core"
)

func TestRawProviderModel_UnmarshalYAML_String(t *testing.T) {
	const data = `- some-model`
	var models []RawProviderModel
	if err := yaml.Unmarshal([]byte(data), &models); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("len = %d, want 1", len(models))
	}
	if models[0].ID != "some-model" {
		t.Errorf("ID = %q, want some-model", models[0].ID)
	}
	if models[0].Metadata != nil {
		t.Errorf("Metadata = %+v, want nil", models[0].Metadata)
	}
}

func TestRawProviderModel_UnmarshalYAML_MappingWithMetadata(t *testing.T) {
	const data = `
- id: local-model
  metadata:
    display_name: Local Model
    context_window: 131072
    max_output_tokens: 8192
    modes: [chat]
    capabilities:
      tools: true
      vision: false
    pricing:
      currency: USD
      input_per_mtok: 0
      output_per_mtok: 0
`
	var models []RawProviderModel
	if err := yaml.Unmarshal([]byte(data), &models); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("len = %d, want 1", len(models))
	}
	m := models[0]
	if m.ID != "local-model" {
		t.Errorf("ID = %q, want local-model", m.ID)
	}
	if m.Metadata == nil {
		t.Fatal("Metadata = nil, want non-nil")
	}
	if m.Metadata.DisplayName != "Local Model" {
		t.Errorf("DisplayName = %q", m.Metadata.DisplayName)
	}
	if m.Metadata.ContextWindow == nil || *m.Metadata.ContextWindow != 131072 {
		t.Errorf("ContextWindow = %v, want 131072", m.Metadata.ContextWindow)
	}
	if m.Metadata.MaxOutputTokens == nil || *m.Metadata.MaxOutputTokens != 8192 {
		t.Errorf("MaxOutputTokens = %v, want 8192", m.Metadata.MaxOutputTokens)
	}
	if got := m.Metadata.Capabilities["tools"]; !got {
		t.Errorf("Capabilities[tools] = %v, want true", got)
	}
	if m.Metadata.Pricing == nil || m.Metadata.Pricing.Currency != "USD" {
		t.Errorf("Pricing = %+v", m.Metadata.Pricing)
	}
}

func TestRawProviderModel_UnmarshalYAML_MixedList(t *testing.T) {
	const data = `
- plain-id
- id: rich-model
  metadata:
    context_window: 4096
`
	var models []RawProviderModel
	if err := yaml.Unmarshal([]byte(data), &models); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len = %d, want 2", len(models))
	}
	if models[0].ID != "plain-id" || models[0].Metadata != nil {
		t.Errorf("models[0] = %+v", models[0])
	}
	if models[1].ID != "rich-model" || models[1].Metadata == nil {
		t.Errorf("models[1] = %+v", models[1])
	}
}

func TestRawProviderModel_UnmarshalYAML_RejectsMappingWithoutID(t *testing.T) {
	const data = `
- metadata:
    context_window: 1024
`
	var models []RawProviderModel
	err := yaml.Unmarshal([]byte(data), &models)
	if err == nil {
		t.Fatal("expected error for mapping without id, got nil")
	}
}

func TestRawProviderModel_UnmarshalYAML_RejectsEmptyScalar(t *testing.T) {
	const data = `- ""`
	var models []RawProviderModel
	err := yaml.Unmarshal([]byte(data), &models)
	if err == nil {
		t.Fatal("expected error for empty scalar id, got nil")
	}
}

func TestRawProviderModel_UnmarshalYAML_RejectsWhitespaceOnlyScalar(t *testing.T) {
	const data = `- "   "`
	var models []RawProviderModel
	err := yaml.Unmarshal([]byte(data), &models)
	if err == nil {
		t.Fatal("expected error for whitespace-only scalar id, got nil")
	}
}

func TestRawProviderModel_UnmarshalYAML_RejectsWhitespaceOnlyMappingID(t *testing.T) {
	const data = `
- id: "   "
  metadata:
    context_window: 1024
`
	var models []RawProviderModel
	err := yaml.Unmarshal([]byte(data), &models)
	if err == nil {
		t.Fatal("expected error for whitespace-only mapping id, got nil")
	}
}

func TestRawProviderModel_UnmarshalYAML_TrimsScalar(t *testing.T) {
	const data = `- "  some-model  "`
	var models []RawProviderModel
	if err := yaml.Unmarshal([]byte(data), &models); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if models[0].ID != "some-model" {
		t.Errorf("ID = %q, want some-model (trimmed)", models[0].ID)
	}
}

func TestProviderModelIDs(t *testing.T) {
	models := []RawProviderModel{
		{ID: "a"},
		{ID: "b", Metadata: &core.ModelMetadata{}},
		{ID: ""}, // filtered
	}
	ids := ProviderModelIDs(models)
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Errorf("ids = %v, want [a b]", ids)
	}
	if got := ProviderModelIDs(nil); got != nil {
		t.Errorf("nil input -> %v, want nil", got)
	}
}

func TestProviderModelMetadataOverrides(t *testing.T) {
	ctxWindow := 2048
	models := []RawProviderModel{
		{ID: "plain"},
		{ID: "rich", Metadata: &core.ModelMetadata{ContextWindow: &ctxWindow}},
		{ID: "", Metadata: &core.ModelMetadata{ContextWindow: &ctxWindow}}, // filtered
	}
	overrides := ProviderModelMetadataOverrides(models)
	if len(overrides) != 1 {
		t.Fatalf("len = %d, want 1", len(overrides))
	}
	if overrides["rich"].ContextWindow == nil || *overrides["rich"].ContextWindow != ctxWindow {
		t.Errorf("overrides[rich] = %+v", overrides["rich"])
	}
	if got := ProviderModelMetadataOverrides(nil); got != nil {
		t.Errorf("nil input -> %v, want nil", got)
	}
}
