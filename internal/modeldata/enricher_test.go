package modeldata

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockAccessor implements ModelInfoAccessor for testing.
type mockAccessor struct {
	ids           []string
	providerTypes map[string]string
	metadata      map[string]*core.ModelMetadata
	discovered    map[string]*core.ModelMetadata
}

func newMockAccessor(models map[string]string) *mockAccessor {
	a := &mockAccessor{
		providerTypes: models,
		metadata:      make(map[string]*core.ModelMetadata),
		discovered:    make(map[string]*core.ModelMetadata),
	}
	for id := range models {
		a.ids = append(a.ids, id)
	}
	return a
}

func (a *mockAccessor) ModelIDs() []string                    { return a.ids }
func (a *mockAccessor) GetProviderType(modelID string) string { return a.providerTypes[modelID] }
func (a *mockAccessor) SetMetadata(modelID string, meta *core.ModelMetadata) {
	a.metadata[modelID] = meta
}

func (a *mockAccessor) DiscoveredMetadata(modelID string) *core.ModelMetadata {
	return a.discovered[modelID]
}

func TestEnrich_MatchedAndUnmatched(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"gpt-4o": {
				DisplayName:   "GPT-4o",
				Modes:         []string{"chat"},
				ContextWindow: new(128000),
				Pricing: &core.ModelPricing{
					Currency:      "USD",
					InputPerMtok:  new(2.50),
					OutputPerMtok: new(10.00),
				},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{},
	}

	accessor := newMockAccessor(map[string]string{
		"gpt-4o":          "openai",
		"unknown-model":   "openai",
		"custom-finetune": "custom",
	})

	Enrich(accessor, list)

	// gpt-4o should be enriched
	if meta, ok := accessor.metadata["gpt-4o"]; !ok || meta == nil {
		t.Error("expected gpt-4o to be enriched")
	} else {
		assert.Equal(t, "GPT-4o", meta.DisplayName)
	}
	meta := // Models the catalog does not know keep only what their provider reported,
		// which here is nothing.
		accessor.metadata["unknown-model"]
	assert.Nil(t, meta)
	meta = accessor.metadata["custom-finetune"]
	assert.Nil(t, meta)
}

func TestEnrich_NilList(t *testing.T) {
	accessor := newMockAccessor(map[string]string{"gpt-4o": "openai"})
	Enrich(accessor, nil) // should not panic
	assert.Empty(t, accessor.metadata)
}

func TestEnrich_NilAccessor(t *testing.T) {
	list := &ModelList{}
	Enrich(nil, list) // should not panic
}

func TestEnrich_ReverseCustomModelIDLookup(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"gpt-4o": {
				DisplayName:   "GPT-4o",
				Modes:         []string{"chat"},
				ContextWindow: new(128000),
				Pricing: &core.ModelPricing{
					Currency:      "USD",
					InputPerMtok:  new(2.50),
					OutputPerMtok: new(10.00),
				},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{
			"openai/gpt-4o": {
				ModelRef:      "gpt-4o",
				CustomModelID: new("gpt-4o-2024-08-06"),
				Enabled:       true,
			},
		},
	}
	list.buildReverseIndex()

	// Registry has the dated response model ID, not the canonical one
	accessor := newMockAccessor(map[string]string{
		"gpt-4o-2024-08-06": "openai",
	})

	Enrich(accessor, list)

	meta := accessor.metadata["gpt-4o-2024-08-06"]
	require.NotNil(t, meta, "expected gpt-4o-2024-08-06 to be enriched via reverse index")
	assert.Equal(t, "GPT-4o", meta.DisplayName)
	require.NotNil(t, meta.Pricing)
	require.NotNil(t, meta.Pricing.InputPerMtok)
	assert.Equal(t, 2.50, *meta.Pricing.InputPerMtok)
}

func TestEnrich_ProviderModelOverride(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"gpt-4o": {
				DisplayName:   "GPT-4o",
				Modes:         []string{"chat"},
				ContextWindow: new(128000),
			},
		},
		ProviderModels: map[string]ProviderModelEntry{
			"azure/gpt-4o": {
				ModelRef:      "gpt-4o",
				Enabled:       true,
				ContextWindow: new(64000),
			},
		},
	}

	accessor := newMockAccessor(map[string]string{
		"gpt-4o": "azure",
	})

	Enrich(accessor, list)

	meta := accessor.metadata["gpt-4o"]
	require.NotNil(t, meta, "expected gpt-4o to be enriched")
	require.NotNil(t, meta.ContextWindow)
	assert.Equal(t, 64000, *meta.ContextWindow)
}

func TestEnrich_ProviderDiscoveryWinsFieldWiseOverCatalog(t *testing.T) {
	accessor := newMockAccessor(map[string]string{"gemma-3-4b-it": "llamacpp"})
	// What a local server reported about the model it is actually running.
	accessor.discovered["gemma-3-4b-it"] = &core.ModelMetadata{
		ContextWindow: new(4096),
		Capabilities:  map[string]bool{"vision": true},
	}
	// A catalog entry that knows the bare ID, with no llamacpp entry at all.
	list := &ModelList{Models: map[string]ModelEntry{
		"gemma-3-4b-it": {
			DisplayName:   "Gemma 3 4B IT",
			ContextWindow: new(131072),
			Capabilities:  map[string]bool{"tools": true},
		},
	}}

	Enrich(accessor, list)

	got := accessor.metadata["gemma-3-4b-it"]
	require.NotNil(t, got)
	require.NotNil(t, got.ContextWindow)
	require.Equal(t, 4096, *got.ContextWindow)
	require.True(t, got.Capabilities["vision"])

	// Fields the provider never reports still come from the catalog.
	require.True(t, got.Capabilities["tools"])
	require.Equal(t, "Gemma 3 4B IT", got.DisplayName)
}

func TestEnrich_RepeatedPassesTrackCatalogUpdates(t *testing.T) {
	accessor := newMockAccessor(map[string]string{"gpt-4o": "openai"})
	list := &ModelList{Models: map[string]ModelEntry{
		"gpt-4o": {DisplayName: "GPT-4o", ContextWindow: new(128000)},
	}}

	Enrich(accessor, list)
	got := accessor.metadata["gpt-4o"]
	require.NotNil(t, got)
	require.NotNil(t, got.ContextWindow)
	require.Equal(t, 128000, *got.ContextWindow)

	// A later catalog refresh corrects the value. Because Enrich merges onto the
	// provider's pristine report rather than onto its own previous output, the
	// new value must win instead of being pinned by the stale one.
	list.Models["gpt-4o"] = ModelEntry{DisplayName: "GPT-4o", ContextWindow: new(200000)}
	Enrich(accessor, list)
	got = accessor.metadata["gpt-4o"]
	require.NotNil(t, got)
	require.NotNil(t, got.ContextWindow)
	require.Equal(t, 200000, *got.ContextWindow)
}

func TestEnrich_DropsCatalogFieldsWhenEntryDisappears(t *testing.T) {
	accessor := newMockAccessor(map[string]string{"gemma-3-4b-it": "llamacpp"})
	accessor.discovered["gemma-3-4b-it"] = &core.ModelMetadata{ContextWindow: new(4096)}
	list := &ModelList{Models: map[string]ModelEntry{
		"gemma-3-4b-it": {DisplayName: "Gemma 3 4B IT", ContextWindow: new(131072)},
	}}

	Enrich(accessor, list)
	got := accessor.metadata["gemma-3-4b-it"]
	require.NotNil(t, got)
	require.Equal(t, "Gemma 3 4B IT", got.DisplayName)

	// The catalog drops the entry on a later refresh; its fields must go with
	// it, leaving only what the provider itself reported.
	delete(list.Models, "gemma-3-4b-it")
	Enrich(accessor, list)

	got = accessor.metadata["gemma-3-4b-it"]
	require.NotNil(t, got)
	require.Empty(t, got.DisplayName)
	require.NotNil(t, got.ContextWindow)
	require.Equal(t, 4096, *got.ContextWindow)
}
