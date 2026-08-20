package modeldata

import (
	"testing"

	"github.com/enterpilot/gomodel/internal/core"
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
		if meta.DisplayName != "GPT-4o" {
			t.Errorf("DisplayName = %s, want GPT-4o", meta.DisplayName)
		}
	}

	// Models the catalog does not know keep only what their provider reported,
	// which here is nothing.
	if meta := accessor.metadata["unknown-model"]; meta != nil {
		t.Errorf("expected unknown-model to carry no catalog metadata, got %+v", meta)
	}
	if meta := accessor.metadata["custom-finetune"]; meta != nil {
		t.Errorf("expected custom-finetune to carry no catalog metadata, got %+v", meta)
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
	if got == nil {
		t.Fatal("metadata = nil, want merged metadata")
	}
	if got.ContextWindow == nil || *got.ContextWindow != 4096 {
		t.Fatalf("context window = %v, want the running server's 4096", got.ContextWindow)
	}
	if !got.Capabilities["vision"] {
		t.Fatal("discovered capability vision was dropped")
	}
	// Fields the provider never reports still come from the catalog.
	if !got.Capabilities["tools"] {
		t.Fatal("catalog capability tools was dropped")
	}
	if got.DisplayName != "Gemma 3 4B IT" {
		t.Fatalf("display name = %q, want the catalog's", got.DisplayName)
	}
}

func TestEnrich_RepeatedPassesTrackCatalogUpdates(t *testing.T) {
	accessor := newMockAccessor(map[string]string{"gpt-4o": "openai"})
	list := &ModelList{Models: map[string]ModelEntry{
		"gpt-4o": {DisplayName: "GPT-4o", ContextWindow: new(128000)},
	}}

	Enrich(accessor, list)
	if got := accessor.metadata["gpt-4o"]; got.ContextWindow == nil || *got.ContextWindow != 128000 {
		t.Fatalf("first pass context window = %v, want 128000", got.ContextWindow)
	}

	// A later catalog refresh corrects the value. Because Enrich merges onto the
	// provider's pristine report rather than onto its own previous output, the
	// new value must win instead of being pinned by the stale one.
	list.Models["gpt-4o"] = ModelEntry{DisplayName: "GPT-4o", ContextWindow: new(200000)}
	Enrich(accessor, list)

	if got := accessor.metadata["gpt-4o"]; got.ContextWindow == nil || *got.ContextWindow != 200000 {
		t.Fatalf("second pass context window = %v, want the refreshed 200000", got.ContextWindow)
	}
}

func TestEnrich_DropsCatalogFieldsWhenEntryDisappears(t *testing.T) {
	accessor := newMockAccessor(map[string]string{"gemma-3-4b-it": "llamacpp"})
	accessor.discovered["gemma-3-4b-it"] = &core.ModelMetadata{ContextWindow: new(4096)}
	list := &ModelList{Models: map[string]ModelEntry{
		"gemma-3-4b-it": {DisplayName: "Gemma 3 4B IT", ContextWindow: new(131072)},
	}}

	Enrich(accessor, list)
	if got := accessor.metadata["gemma-3-4b-it"]; got.DisplayName != "Gemma 3 4B IT" {
		t.Fatalf("first pass display name = %q, want the catalog's", got.DisplayName)
	}

	// The catalog drops the entry on a later refresh; its fields must go with
	// it, leaving only what the provider itself reported.
	delete(list.Models, "gemma-3-4b-it")
	Enrich(accessor, list)

	got := accessor.metadata["gemma-3-4b-it"]
	if got == nil {
		t.Fatal("metadata = nil, want the provider's own report to survive")
	}
	if got.DisplayName != "" {
		t.Fatalf("display name = %q, want it dropped with the catalog entry", got.DisplayName)
	}
	if got.ContextWindow == nil || *got.ContextWindow != 4096 {
		t.Fatalf("context window = %v, want the provider's 4096", got.ContextWindow)
	}
}
