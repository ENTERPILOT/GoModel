package modeldata

import (
	"testing"

	"gomodel/internal/core"
)

func TestResolve_NilList(t *testing.T) {
	meta := Resolve(nil, "openai", "gpt-4o")
	if meta != nil {
		t.Error("expected nil for nil list")
	}
}

func TestResolve_NoMatch(t *testing.T) {
	list := &ModelList{
		Models:         map[string]ModelEntry{},
		ProviderModels: map[string]ProviderModelEntry{},
	}
	meta := Resolve(list, "openai", "nonexistent-model")
	if meta != nil {
		t.Error("expected nil for no match")
	}
}

func TestResolve_DirectModelMatch(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"gpt-4o": {
				DisplayName:     "GPT-4o",
				Description:     new("Flagship model"),
				Family:          new("gpt-4o"),
				Modes:           []string{"chat"},
				Tags:            []string{"flagship", "multimodal"},
				ContextWindow:   new(128000),
				MaxOutputTokens: new(16384),
				Capabilities: map[string]bool{
					"function_calling": true,
					"streaming":        true,
					"vision":           true,
				},
				Pricing: &core.ModelPricing{
					Currency:      "USD",
					InputPerMtok:  new(2.50),
					OutputPerMtok: new(10.00),
				},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{},
	}

	meta := Resolve(list, "openai", "gpt-4o")
	if meta == nil {
		t.Fatal("expected non-nil metadata")
		return
	}

	if meta.DisplayName != "GPT-4o" {
		t.Errorf("DisplayName = %s, want GPT-4o", meta.DisplayName)
	}
	if meta.Description != "Flagship model" {
		t.Errorf("Description = %s, want 'Flagship model'", meta.Description)
	}
	if meta.Family != "gpt-4o" {
		t.Errorf("Family = %s, want gpt-4o", meta.Family)
	}
	if len(meta.Modes) != 1 || meta.Modes[0] != "chat" {
		t.Errorf("Modes = %v, want [chat]", meta.Modes)
	}
	if len(meta.Tags) != 2 {
		t.Errorf("Tags len = %d, want 2", len(meta.Tags))
	}
	if *meta.ContextWindow != 128000 {
		t.Errorf("ContextWindow = %d, want 128000", *meta.ContextWindow)
	}
	if *meta.MaxOutputTokens != 16384 {
		t.Errorf("MaxOutputTokens = %d, want 16384", *meta.MaxOutputTokens)
	}
	if !meta.Capabilities["function_calling"] {
		t.Error("expected function_calling capability")
	}
	if meta.Pricing == nil {
		t.Fatal("expected non-nil pricing")
		return
	}
	if meta.Pricing.Currency != "USD" {
		t.Errorf("Currency = %s, want USD", meta.Pricing.Currency)
	}
	if *meta.Pricing.InputPerMtok != 2.50 {
		t.Errorf("InputPerMtok = %f, want 2.50", *meta.Pricing.InputPerMtok)
	}
	if *meta.Pricing.OutputPerMtok != 10.00 {
		t.Errorf("OutputPerMtok = %f, want 10.00", *meta.Pricing.OutputPerMtok)
	}
}

func TestResolve_ProviderModelOverride(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"gpt-4o": {
				DisplayName:     "GPT-4o",
				Modes:           []string{"chat"},
				ContextWindow:   new(128000),
				MaxOutputTokens: new(16384),
				Pricing: &core.ModelPricing{
					Currency:      "USD",
					InputPerMtok:  new(2.50),
					OutputPerMtok: new(10.00),
				},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{
			"azure/gpt-4o": {
				ModelRef:      "gpt-4o",
				Enabled:       true,
				ContextWindow: new(64000),
				Pricing: &core.ModelPricing{
					Currency:      "USD",
					InputPerMtok:  new(5.00),
					OutputPerMtok: new(15.00),
				},
			},
		},
	}

	meta := Resolve(list, "azure", "gpt-4o")
	if meta == nil {
		t.Fatal("expected non-nil metadata")
		return
	}

	// Provider model should override context_window
	if *meta.ContextWindow != 64000 {
		t.Errorf("ContextWindow = %d, want 64000 (override)", *meta.ContextWindow)
	}
	// max_output_tokens should come from base model (not overridden)
	if *meta.MaxOutputTokens != 16384 {
		t.Errorf("MaxOutputTokens = %d, want 16384 (base)", *meta.MaxOutputTokens)
	}
	// Pricing should be overridden
	if *meta.Pricing.InputPerMtok != 5.00 {
		t.Errorf("InputPerMtok = %f, want 5.00 (override)", *meta.Pricing.InputPerMtok)
	}
	if *meta.Pricing.OutputPerMtok != 15.00 {
		t.Errorf("OutputPerMtok = %f, want 15.00 (override)", *meta.Pricing.OutputPerMtok)
	}
	// DisplayName from base model
	if meta.DisplayName != "GPT-4o" {
		t.Errorf("DisplayName = %s, want GPT-4o (base)", meta.DisplayName)
	}
}

func TestResolve_MapsRankingsIntoMetadata(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"gpt-4o": {
				DisplayName: "GPT-4o",
				Modes:       []string{"chat"},
				Rankings: map[string]RankingEntry{
					"chatbot_arena": {
						Elo:  new(1287.0),
						Rank: new(3),
						AsOf: new("2026-02-01"),
					},
				},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{},
	}

	meta := Resolve(list, "openai", "gpt-4o")
	if meta == nil {
		t.Fatal("expected non-nil metadata")
		return
	}
	ranking, ok := meta.Rankings["chatbot_arena"]
	if !ok {
		t.Fatal("expected chatbot_arena ranking in metadata")
	}
	if ranking.Elo == nil || *ranking.Elo != 1287.0 {
		t.Fatalf("ranking.Elo = %v, want 1287.0", ranking.Elo)
	}
	if ranking.Rank == nil || *ranking.Rank != 3 {
		t.Fatalf("ranking.Rank = %v, want 3", ranking.Rank)
	}
	if ranking.AsOf != "2026-02-01" {
		t.Fatalf("ranking.AsOf = %q, want %q", ranking.AsOf, "2026-02-01")
	}
}

func TestResolve_ProviderModelWithoutBaseModel(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{},
		ProviderModels: map[string]ProviderModelEntry{
			"custom/my-model": {
				ModelRef:      "nonexistent",
				Enabled:       true,
				ContextWindow: new(32000),
				Pricing: &core.ModelPricing{
					Currency:      "USD",
					InputPerMtok:  new(1.00),
					OutputPerMtok: new(2.00),
				},
			},
		},
	}

	meta := Resolve(list, "custom", "my-model")
	if meta == nil {
		t.Fatal("expected non-nil metadata even without base model")
		return
	}

	if *meta.ContextWindow != 32000 {
		t.Errorf("ContextWindow = %d, want 32000", *meta.ContextWindow)
	}
	if *meta.Pricing.InputPerMtok != 1.00 {
		t.Errorf("InputPerMtok = %f, want 1.00", *meta.Pricing.InputPerMtok)
	}
}

func TestResolve_NilPricing(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"text-moderation": {
				DisplayName: "Text Moderation",
				Modes:       []string{"moderation"},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{},
	}

	meta := Resolve(list, "openai", "text-moderation")
	if meta == nil {
		t.Fatal("expected non-nil metadata")
		return
	}
	if meta.Pricing != nil {
		t.Error("expected nil pricing for model without pricing")
	}
}

func TestResolve_SetsCategoriesFromModes(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"gpt-4o": {
				DisplayName: "GPT-4o",
				Modes:       []string{"chat"},
			},
			"dall-e-3": {
				DisplayName: "DALL-E 3",
				Modes:       []string{"image_generation"},
			},
			"whisper-1": {
				DisplayName: "Whisper",
				Modes:       []string{"audio_transcription"},
			},
			"text-moderation": {
				DisplayName: "Moderation",
				Modes:       []string{"moderation"},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{},
	}

	tests := []struct {
		modelID  string
		wantCats []core.ModelCategory
	}{
		{"gpt-4o", []core.ModelCategory{core.CategoryTextGeneration}},
		{"dall-e-3", []core.ModelCategory{core.CategoryImage}},
		{"whisper-1", []core.ModelCategory{core.CategoryAudio}},
		{"text-moderation", []core.ModelCategory{core.CategoryUtility}},
	}

	for _, tt := range tests {
		t.Run(tt.modelID, func(t *testing.T) {
			meta := Resolve(list, "openai", tt.modelID)
			if meta == nil {
				t.Fatal("expected non-nil metadata")
				return
			}
			if len(meta.Categories) != len(tt.wantCats) {
				t.Fatalf("Categories = %v, want %v", meta.Categories, tt.wantCats)
			}
			for i, c := range meta.Categories {
				if c != tt.wantCats[i] {
					t.Errorf("Categories[%d] = %q, want %q", i, c, tt.wantCats[i])
				}
			}
		})
	}
}

// Verify Resolve handles the three-layer merge correctly:
// base model fields + provider_model overrides
func TestResolve_ThreeLayerMerge(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"claude-sonnet-4-20250514": {
				DisplayName:     "Claude Sonnet 4",
				Description:     new("Fast, intelligent model"),
				Family:          new("claude-sonnet"),
				Modes:           []string{"chat"},
				Tags:            []string{"flagship"},
				ContextWindow:   new(200000),
				MaxOutputTokens: new(16384),
				Capabilities: map[string]bool{
					"function_calling": true,
					"vision":           true,
				},
				Pricing: &core.ModelPricing{
					Currency:           "USD",
					InputPerMtok:       new(3.00),
					OutputPerMtok:      new(15.00),
					CachedInputPerMtok: new(0.30),
				},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{
			"bedrock/claude-sonnet-4-20250514": {
				ModelRef:        "claude-sonnet-4-20250514",
				Enabled:         true,
				MaxOutputTokens: new(8192),
				Pricing: &core.ModelPricing{
					Currency:           "USD",
					InputPerMtok:       new(3.00),
					OutputPerMtok:      new(15.00),
					CachedInputPerMtok: new(0.30),
				},
			},
		},
	}

	// Direct provider (anthropic) - should use base model only
	meta := Resolve(list, "anthropic", "claude-sonnet-4-20250514")
	if meta == nil {
		t.Fatal("expected non-nil metadata")
		return
	}
	if *meta.MaxOutputTokens != 16384 {
		t.Errorf("MaxOutputTokens = %d, want 16384 (base)", *meta.MaxOutputTokens)
	}

	// Bedrock - should override max_output_tokens
	meta = Resolve(list, "bedrock", "claude-sonnet-4-20250514")
	if meta == nil {
		t.Fatal("expected non-nil metadata for bedrock")
		return
	}
	if *meta.MaxOutputTokens != 8192 {
		t.Errorf("MaxOutputTokens = %d, want 8192 (bedrock override)", *meta.MaxOutputTokens)
	}
	// DisplayName should still come from base
	if meta.DisplayName != "Claude Sonnet 4" {
		t.Errorf("DisplayName = %s, want 'Claude Sonnet 4'", meta.DisplayName)
	}
}

func TestResolve_ReverseCustomModelIDLookup(t *testing.T) {
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

	// Resolve using the dated response model ID
	meta := Resolve(list, "openai", "gpt-4o-2024-08-06")
	if meta == nil {
		t.Fatal("expected non-nil metadata via reverse lookup")
		return
	}
	if meta.DisplayName != "GPT-4o" {
		t.Errorf("DisplayName = %s, want GPT-4o", meta.DisplayName)
	}
	if meta.Pricing == nil {
		t.Fatal("expected non-nil pricing via reverse lookup")
		return
	}
	if *meta.Pricing.InputPerMtok != 2.50 {
		t.Errorf("InputPerMtok = %f, want 2.50", *meta.Pricing.InputPerMtok)
	}
}

func TestResolve_ReverseIndexNotBuilt(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"gpt-4o": {
				DisplayName: "GPT-4o",
				Modes:       []string{"chat"},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{
			"openai/gpt-4o": {
				ModelRef:      "gpt-4o",
				CustomModelID: new("gpt-4o-2024-08-06"),
				Enabled:       true,
			},
		},
		// providerModelByActualID is nil (buildReverseIndex not called)
	}

	meta := Resolve(list, "openai", "gpt-4o-2024-08-06")
	if meta != nil {
		t.Error("expected nil when reverse index is not built")
	}
}

func TestResolve_ReverseIndexWithProviderModelOverride(t *testing.T) {
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
				Pricing: &core.ModelPricing{
					Currency:      "USD",
					InputPerMtok:  new(3.00),
					OutputPerMtok: new(12.00),
				},
			},
		},
	}
	list.buildReverseIndex()

	// Reverse lookup should resolve and apply provider_model pricing override
	meta := Resolve(list, "openai", "gpt-4o-2024-08-06")
	if meta == nil {
		t.Fatal("expected non-nil metadata via reverse lookup")
		return
	}
	if meta.Pricing == nil {
		t.Fatal("expected non-nil pricing")
		return
	}
	// Should use the provider_model override, not the base model pricing
	if *meta.Pricing.InputPerMtok != 3.00 {
		t.Errorf("InputPerMtok = %f, want 3.00 (provider override)", *meta.Pricing.InputPerMtok)
	}
	if *meta.Pricing.OutputPerMtok != 12.00 {
		t.Errorf("OutputPerMtok = %f, want 12.00 (provider override)", *meta.Pricing.OutputPerMtok)
	}
}

func TestResolve_ModelAliasUsesProviderOverride(t *testing.T) {
	list := &ModelList{
		Providers: map[string]ProviderEntry{
			"gemini": {DisplayName: "Gemini"},
		},
		Models: map[string]ModelEntry{
			"claude-4-opus": {
				DisplayName: "Claude 4 Opus",
				Modes:       []string{"chat"},
				Aliases:     []string{"claude-opus-4", "gemini/claude-opus-4"},
				Pricing: &core.ModelPricing{
					Currency:      "USD",
					InputPerMtok:  new(15.00),
					OutputPerMtok: new(75.00),
				},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{
			"gemini/claude-4-opus": {
				ModelRef:      "claude-4-opus",
				Enabled:       true,
				ContextWindow: new(200000),
				Pricing: &core.ModelPricing{
					Currency:      "USD",
					InputPerMtok:  new(12.00),
					OutputPerMtok: new(60.00),
				},
			},
		},
	}
	list.buildReverseIndex()

	meta := Resolve(list, "gemini", "claude-opus-4")
	if meta == nil {
		t.Fatal("expected non-nil metadata via model alias")
		return
	}
	if meta.DisplayName != "Claude 4 Opus" {
		t.Fatalf("DisplayName = %q, want Claude 4 Opus", meta.DisplayName)
	}
	if meta.ContextWindow == nil || *meta.ContextWindow != 200000 {
		t.Fatalf("ContextWindow = %v, want 200000", meta.ContextWindow)
	}
	if meta.Pricing == nil || meta.Pricing.InputPerMtok == nil || *meta.Pricing.InputPerMtok != 12.00 {
		t.Fatalf("InputPerMtok = %#v, want 12.00", meta.Pricing)
	}
}

func TestResolve_AmbiguousModelAliasReturnsNil(t *testing.T) {
	list := &ModelList{
		Models: map[string]ModelEntry{
			"model-a": {
				DisplayName: "Model A",
				Modes:       []string{"chat"},
				Aliases:     []string{"shared-alias"},
			},
			"model-b": {
				DisplayName: "Model B",
				Modes:       []string{"chat"},
				Aliases:     []string{"shared-alias"},
			},
		},
		ProviderModels: map[string]ProviderModelEntry{},
	}
	list.buildReverseIndex()

	meta := Resolve(list, "openai", "shared-alias")
	if meta != nil {
		t.Fatalf("expected nil for ambiguous alias, got %+v", meta)
	}
}
