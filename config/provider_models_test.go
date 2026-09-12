package config

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRawProviderModel_UnmarshalYAML_String(t *testing.T) {
	const data = `- some-model`
	var models []RawProviderModel
	err := yaml.Unmarshal([]byte(data), &models)
	require.NoError(t, err)
	require.Len(t, models, 1)
	assert.Equal(t, "some-model", models[0].ID)
	assert.Nil(t, models[0].Metadata)
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
	err := yaml.Unmarshal([]byte(data), &models)
	require.NoError(t, err)
	require.Len(t, models, 1)

	m := models[0]
	assert.Equal(t, "local-model", m.ID)
	require.NotNil(t, m.Metadata)
	assert.Equal(t, "Local Model", m.Metadata.DisplayName)
	assert.NotNil(t, m.Metadata.ContextWindow)
	assert.Equal(t, 131072, *m.Metadata.ContextWindow)
	assert.NotNil(t, m.Metadata.MaxOutputTokens)
	assert.Equal(t, 8192, *m.Metadata.MaxOutputTokens)
	got := m.Metadata.Capabilities["tools"]
	assert.True(t, got)
	assert.NotNil(t, m.Metadata.Pricing)
	assert.Equal(t, "USD", m.Metadata.Pricing.Currency)
}

func TestRawProviderModel_UnmarshalYAML_MixedList(t *testing.T) {
	const data = `
- plain-id
- id: rich-model
  metadata:
    context_window: 4096
`
	var models []RawProviderModel
	err := yaml.Unmarshal([]byte(data), &models)
	require.NoError(t, err)
	require.Len(t, models, 2)
	assert.Equal(t, "plain-id", models[0].ID)
	assert.Nil(t, models[0].Metadata, "models[0] = %+v", models[0])
	assert.Equal(t, "rich-model", models[1].ID)
	assert.NotNil(t, models[1].Metadata, "models[1] = %+v", models[1])
}

func TestRawProviderModel_UnmarshalYAML_RejectsMappingWithoutID(t *testing.T) {
	const data = `
- metadata:
    context_window: 1024
`
	var models []RawProviderModel
	err := yaml.Unmarshal([]byte(data), &models)
	require.Error(t, err)
}

func TestRawProviderModel_UnmarshalYAML_RejectsEmptyScalar(t *testing.T) {
	const data = `- ""`
	var models []RawProviderModel
	err := yaml.Unmarshal([]byte(data), &models)
	require.Error(t, err)
}

func TestRawProviderModel_UnmarshalYAML_RejectsWhitespaceOnlyScalar(t *testing.T) {
	const data = `- "   "`
	var models []RawProviderModel
	err := yaml.Unmarshal([]byte(data), &models)
	require.Error(t, err)
}

func TestRawProviderModel_UnmarshalYAML_RejectsWhitespaceOnlyMappingID(t *testing.T) {
	const data = `
- id: "   "
  metadata:
    context_window: 1024
`
	var models []RawProviderModel
	err := yaml.Unmarshal([]byte(data), &models)
	require.Error(t, err)
}

func TestRawProviderModel_UnmarshalYAML_TrimsScalar(t *testing.T) {
	const data = `- "  some-model  "`
	var models []RawProviderModel
	err := yaml.Unmarshal([]byte(data), &models)
	require.NoError(t, err)
	assert.Equal(t, "some-model", models[0].ID)
}

func TestProviderModelIDs(t *testing.T) {
	models := []RawProviderModel{
		{ID: "a"},
		{ID: "b", Metadata: &core.ModelMetadata{}},
		{ID: ""}, // filtered
	}
	ids := ProviderModelIDs(models)
	assert.Len(t, ids, 2)
	assert.Equal(t, "a", ids[0])
	assert.Equal(t, "b", ids[1])
	got := ProviderModelIDs(nil)
	assert.Nil(t, got)
}

func TestProviderModelMetadataOverrides(t *testing.T) {
	ctxWindow := 2048
	models := []RawProviderModel{
		{ID: "plain"},
		{ID: "rich", Metadata: &core.ModelMetadata{ContextWindow: &ctxWindow}},
		{ID: "", Metadata: &core.ModelMetadata{ContextWindow: &ctxWindow}}, // filtered
	}
	overrides := ProviderModelMetadataOverrides(models)
	require.Len(t, overrides, 1)
	assert.NotNil(t, overrides["rich"].ContextWindow)
	assert.Equal(t, ctxWindow, *overrides["rich"].ContextWindow, "overrides[rich] = %+v", overrides["rich"])
	got := ProviderModelMetadataOverrides(nil)
	assert.Nil(t, got)
}
