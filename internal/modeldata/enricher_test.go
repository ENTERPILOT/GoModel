package modeldata

import (
	"testing"

	"gomodel/internal/core"
)

// mockAccessor implements ModelInfoAccessor for testing.
type mockAccessor struct {
	ids           []string
	providerTypes map[string]string
	metadata      map[string]*core.ModelMetadata
}

func newMockAccessor(models map[string]string) *mockAccessor {
	a := &mockAccessor{
		providerTypes: models,
		metadata:      make(map[string]*core.ModelMetadata),
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
		if meta.DisplayName != "GPT-4o" {
			t.Errorf("DisplayName = %s, want GPT-4o", meta.DisplayName)
		}
	}

	// unknown-model should NOT be enriched
	if _, ok := accessor.metadata["unknown-model"]; ok {
		t.Error("expected unknown-model to NOT be enriched")
	}

	// custom-finetune should NOT be enriched
	if _, ok := accessor.metadata["custom-finetune"]; ok {
		t.Error("expected custom-finetune to NOT be enriched")
	}
}

func TestEnrich_NilList(t *testing.T) {
	accessor := newMockAccessor(map[string]string{"gpt-4o": "openai"})
	Enrich(accessor, nil) // should not panic
	if len(accessor.metadata) != 0 {
		t.Error("expected no metadata set with nil list")
	}
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
	if meta == nil {
		t.Fatal("expected gpt-4o-2024-08-06 to be enriched via reverse index")
		return
	}
	if meta.DisplayName != "GPT-4o" {
		t.Errorf("DisplayName = %s, want GPT-4o", meta.DisplayName)
	}
	if meta.Pricing == nil || meta.Pricing.InputPerMtok == nil || *meta.Pricing.InputPerMtok != 2.50 {
		t.Error("expected pricing from base model via reverse lookup")
	}
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
	if meta == nil {
		t.Fatal("expected gpt-4o to be enriched")
		return
	}
	if *meta.ContextWindow != 64000 {
		t.Errorf("ContextWindow = %d, want 64000 (azure override)", *meta.ContextWindow)
	}
}
